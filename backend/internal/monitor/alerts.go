package monitor

import (
	"alliance/internal/resources"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed alerts.sql
var alertQuery string

type Alerts struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}

type alertDelivery struct {
	id, rule, lease, channel, message string
	webhook, signature                []byte
	attempts                          int
}

func (a Alerts) Serve(ctx context.Context) {
	checks := time.NewTicker(time.Minute)
	sends := time.NewTicker(5 * time.Second)
	defer checks.Stop()
	defer sends.Stop()
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	a.check(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-checks.C:
			a.check(ctx)
		case <-sends.C:
			if didWork, err := a.next(ctx, client); err != nil && ctx.Err() == nil {
				slog.Warn("monitor alert delivery pending")
			} else if didWork {
				// Drain a few more deliveries while reserving time for the next check.
				for range 3 {
					active, err := a.next(ctx, client)
					if err != nil || !active {
						break
					}
				}
			}
		}
	}
}

func (a Alerts) check(ctx context.Context) {
	check, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := pgx.BeginFunc(check, a.DB, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(check, `select pg_try_advisory_xact_lock(17952109)`).Scan(&locked); err != nil || !locked {
			return err
		}
		_, err := tx.Exec(check, alertQuery, time.Now().UTC())
		return err
	})
	if err != nil && ctx.Err() == nil {
		slog.Warn("monitor alert check pending")
	}
}

func (a Alerts) next(ctx context.Context, client *http.Client) (bool, error) {
	var delivery alertDelivery
	err := a.DB.QueryRow(ctx, `with uncertain as (
	 update alert_deliveries set state='unknown',lease=null where state='sending' and next_at<=now()
	), retired as (
	 update alert_deliveries d set state='cancelled' from alert_rules r
	 where d.rule_id=r.id and d.state='pending' and (not r.enabled or d.rule_revision<>r.revision)
	), due as (
	 select d.id,r.channel,r.encrypted_webhook,r.encrypted_signature from alert_deliveries d join alert_rules r on r.id=d.rule_id
	 where d.state='pending' and d.next_at<=now() and r.enabled and d.rule_revision=r.revision
	 order by d.next_at,d.id limit 1 for update of d skip locked
	)
	update alert_deliveries d set state='sending',lease=gen_random_uuid(),next_at=now()+interval '30 seconds',attempts=attempts+1
	 from due where d.id=due.id returning d.id,d.rule_id,d.lease,due.channel,d.message,due.encrypted_webhook,due.encrypted_signature,d.attempts`).
		Scan(&delivery.id, &delivery.rule, &delivery.lease, &delivery.channel, &delivery.message, &delivery.webhook, &delivery.signature, &delivery.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	state := a.deliver(ctx, client, delivery)
	if delivery.attempts >= 5 && state == "pending" {
		state = "failed"
	}
	_, err = a.DB.Exec(ctx, `update alert_deliveries set state=$3,lease=null,
	 delivered_at=case when $3='sent' then now() end,next_at=now()+$4::int*interval '1 second'
	 where id=$1 and lease=$2`, delivery.id, delivery.lease, state, (1<<min(delivery.attempts, 5))*5)
	return true, err
}

func alertScope(id, purpose string) string { return "monitor-alert/" + id + "/" + purpose }

func (a Alerts) deliver(ctx context.Context, client *http.Client, d alertDelivery) string {
	endpoint, err := a.Vault.Open(d.webhook, alertScope(d.rule, "webhook"))
	if err != nil {
		return "failed"
	}
	var secret string
	if len(d.signature) != 0 {
		secret, err = a.Vault.Open(d.signature, alertScope(d.rule, "signature"))
		if err != nil {
			return "failed"
		}
	}
	req, err := alertRequest(ctx, d.channel, endpoint, secret, d.message, time.Now())
	if err != nil {
		return "failed"
	}
	resp, err := client.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		return "pending"
	}
	if resp.StatusCode >= 500 {
		return "unknown"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "failed"
	}
	var ack struct {
		Code       *int `json:"code"`
		StatusCode *int `json:"StatusCode"`
		ErrorCode  *int `json:"errcode"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&ack); err != nil {
		return "unknown"
	}
	code := ack.ErrorCode
	if d.channel == "feishu" {
		code = ack.Code
		if code == nil {
			code = ack.StatusCode
		}
	}
	if code == nil {
		return "unknown"
	}
	if *code == 0 {
		return "sent"
	}
	return "failed"
}

var webhookToken = regexp.MustCompile(`^[A-Za-z0-9_-]{1,512}$`)

func alertWebhook(channel, target string) (*url.URL, error) {
	bad := errors.New("invalid notification webhook")
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" ||
		(u.Port() != "" && u.Port() != "443") || u.RawPath != "" {
		return nil, bad
	}
	switch channel {
	case "feishu":
		path, ok := strings.CutPrefix(u.Path, "/open-apis/bot/v2/hook/")
		if (u.Hostname() != "open.feishu.cn" && u.Hostname() != "open.larksuite.com") || !ok || !webhookToken.MatchString(path) || u.RawQuery != "" || u.ForceQuery {
			return nil, bad
		}
	case "dingtalk":
		if u.Hostname() != "oapi.dingtalk.com" || u.Path != "/robot/send" {
			return nil, bad
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(q) != 1 || len(q["access_token"]) != 1 || !webhookToken.MatchString(q.Get("access_token")) {
			return nil, bad
		}
	default:
		return nil, bad
	}
	return u, nil
}

func alertRequest(ctx context.Context, channel, endpoint, secret, message string, now time.Time) (*http.Request, error) {
	u, err := alertWebhook(channel, endpoint)
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if channel == "feishu" {
		body["msg_type"] = "text"
		body["content"] = map[string]string{"text": message}
		if secret != "" {
			stamp := strconv.FormatInt(now.Unix(), 10)
			mac := hmac.New(sha256.New, []byte(stamp+"\n"+secret))
			body["timestamp"] = stamp
			body["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
	} else {
		body["msgtype"] = "text"
		body["text"] = map[string]string{"content": message}
		if secret != "" {
			stamp := strconv.FormatInt(now.UnixMilli(), 10)
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(stamp + "\n" + secret))
			query := u.Query()
			query.Set("timestamp", stamp)
			query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
			u.RawQuery = query.Encode()
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

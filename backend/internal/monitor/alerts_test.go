package monitor

import (
	"alliance/internal/database"
	"alliance/internal/resources"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAlertWebhookRequestsCarryScopedSignatures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	feishu, err := alertRequest(t.Context(), "feishu", "https://open.feishu.cn/open-apis/bot/v2/hook/fixture", "signing-secret", "test notification", now)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.NewDecoder(feishu.Body).Decode(&body); err != nil || body["timestamp"] != "1700000000" || body["sign"] == nil || body["msg_type"] != "text" {
		t.Fatalf("invalid Feishu webhook body: %+v %v", body, err)
	}
	ding, err := alertRequest(t.Context(), "dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=fixture", "signing-secret", "test notification", now)
	if err != nil || ding.URL.Query().Get("timestamp") != "1700000000000" || ding.URL.Query().Get("access_token") != "fixture" || ding.URL.Query().Get("sign") == "" {
		t.Fatalf("invalid DingTalk webhook request: %v", err)
	}
	for _, invalid := range []string{
		"https://open.feishu.cn.example.test/open-apis/bot/v2/hook/fixture",
		"https://anything@open.feishu.cn/open-apis/bot/v2/hook/fixture",
		"https://open.feishu.cn:1880/open-apis/bot/v2/hook/fixture",
		"https://open.feishu.cn/open-apis/bot/v2/hook/%2e%2e",
	} {
		if _, err := alertWebhook("feishu", invalid); err == nil {
			t.Errorf("accepted unexpected endpoint %s", invalid)
		}
	}
}

type mockAlertTransport func(*http.Request) (*http.Response, error)

func (m mockAlertTransport) RoundTrip(r *http.Request) (*http.Response, error) { return m(r) }

func TestAlertDeliveryDistinguishesAcknowledgmentAndUnknownOutcome(t *testing.T) {
	vault, err := resources.NewVault(strings.Repeat("bc", 32))
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.Seal("https://open.feishu.cn/open-apis/bot/v2/hook/test-token", alertScope("rule", "webhook"))
	if err != nil {
		t.Fatal(err)
	}
	a := Alerts{Vault: vault}
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{200, `{"code":0}`, "sent"},
		{200, `{"code":19021}`, "failed"},
		{200, `{"unrelated":0}`, "unknown"},
		{429, "", "pending"},
		{0, "", "unknown"},
	} {
		transport := mockAlertTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "open.feishu.cn" || r.Method != http.MethodPost {
				t.Fatal("alert delivery used unexpected request")
			}
			if tc.status == 0 {
				return nil, errors.New("fixture transport did not report outcome")
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		got := a.deliver(t.Context(), &http.Client{Transport: transport}, alertDelivery{rule: "rule", channel: "feishu", webhook: key, message: "test"})
		if got != tc.want {
			t.Errorf("HTTP %d body=%q classified %s, want %s", tc.status, tc.body, got, tc.want)
		}
	}
}

func TestAlertEvaluationSuppressesRepeatedBudgetNotices(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, soft, hard := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`insert into teams(id,name) values($1,$2)`, team, "alert-test-"+team)
	exec(`insert into alert_rules(id,name,kind,team_id,threshold,window_minutes,silence_minutes,channel,encrypted_webhook)
	 values($1,'soft notification','budget_limit',$3,80,5,60,'feishu','\x01'),($2,'hard notification','budget_limit',$3,100,5,60,'dingtalk','\x02')`, soft, hard, team)
	exec(`insert into model_monthly_budget_totals(scope,scope_id,month_start) values('team',$1,'2026-01-01')`, team)
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	exec(`insert into model_budget_alert_events(scope,scope_id,month_start,threshold,limit_micro_cny,used_micro_cny,occurred_at)
	 values('team',$1,'2026-01-01',80,100,83,$2),('team',$1,'2026-01-01',100,100,100,$2)`, team, at)
	exec(alertQuery, at)
	exec(alertQuery, at.Add(time.Minute))
	exec(alertQuery, at.Add(2*time.Hour))
	var count int
	if err := tx.QueryRow(ctx, `select count(*) from alert_deliveries where rule_id in ($1,$2) and state='pending'`, soft, hard).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected independent 80 and 100 percent outbox entries, got %d: %v", count, err)
	}
}

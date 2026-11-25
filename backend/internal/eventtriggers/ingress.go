package eventtriggers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/web"
	"cel.dev/cel-go/cel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const eventLimit = 48 << 10

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{27,96}$`)

func signatureScope(id string) string { return "business-event/" + id + "/signature" }
func sampleScope(id string) string    { return "business-event/" + id + "/sample" }

func compileRules(filter, key string) (cel.Program, cel.Program, error) {
	if !validText(filter, 600) || !validText(key, 400) {
		return nil, nil, errors.New("event expression exceeds input limit")
	}
	env, err := cel.NewEnv(cel.Variable("event", cel.MapType(cel.StringType, cel.DynType)))
	if err != nil {
		return nil, nil, err
	}
	compile := func(src string, required *cel.Type) (cel.Program, error) {
		ast, issues := env.Compile(src)
		if issues.Err() != nil {
			return nil, issues.Err()
		}
		if !required.IsAssignableType(ast.OutputType()) && ast.OutputType() != cel.DynType {
			return nil, errors.New("unexpected event expression result type")
		}
		return env.Program(ast, cel.CostLimit(5000), cel.InterruptCheckFrequency(20), cel.MemoryLimit(20000))
	}
	f, err := compile(filter, cel.BoolType)
	if err != nil {
		return nil, nil, err
	}
	k, err := compile(key, cel.StringType)
	return f, k, err
}

func verifiedSignature(key, timestamp, signature string, body []byte, now time.Time) bool {
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strconv.FormatInt(unix, 10) != timestamp || unix < now.Unix()-300 || unix > now.Unix()+60 {
		return false
	}
	signature, ok := strings.CutPrefix(signature, "sha256=")
	if !ok || len(signature) != 64 {
		return false
	}
	raw, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	sign := hmac.New(sha256.New, []byte(key))
	sign.Write([]byte(timestamp + "."))
	sign.Write(body)
	return hmac.Equal(sign.Sum(nil), raw)
}

func normalizeEvent(raw any, depth int, count *int) (any, bool) {
	*count = *count + 1
	if depth > 12 || *count > 1200 {
		return nil, false
	}
	switch item := raw.(type) {
	case map[string]any:
		for key, field := range item {
			value, valid := normalizeEvent(field, depth+1, count)
			if !valid || !validText(key, 160) {
				return nil, false
			}
			item[key] = value
		}
	case []any:
		for i, field := range item {
			value, valid := normalizeEvent(field, depth+1, count)
			if !valid {
				return nil, false
			}
			item[i] = value
		}
	case json.Number:
		if n, err := item.Int64(); err == nil {
			return n, true
		}
		n, err := item.Float64()
		return n, err == nil
	case string:
		return raw, len(item) <= 8192
	}
	return raw, true
}

func selectEventFields(event map[string]any, names []string) map[string]any {
	fields := make(map[string]any)
	for _, name := range names {
		switch value := event[name].(type) {
		case string:
			if validText(value, 480) {
				fields[name] = value
			}
		case int64, float64, bool:
			fields[name] = value
		}
	}
	return fields
}

func (s Server) ingress(w http.ResponseWriter, r *http.Request) error {
	token := r.PathValue("token")
	if !tokenPattern.MatchString(token) {
		return web.Fail(404, "事件触发器不可用")
	}
	ctx := r.Context()
	var id, filter, key string
	var version, cooldown int
	var fields []string
	var encrypted []byte
	err := s.DB.QueryRow(ctx, `select t.id,t.revision,t.filter_cel,t.dedup_cel,t.cooldown_seconds,t.event_fields,t.hmac_cipher
	 from event_triggers t where t.webhook_token_hash=$1 and t.enabled and space_role(t.space_id,t.maintainer) in ('member','space_admin')
	 and (t.connector_id is null or exists(select from business_connectors c where c.id=t.connector_id and c.enabled))`,
		web.Hash(token)).Scan(&id, &version, &filter, &key, &cooldown, &fields, &encrypted)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "事件触发器不可用")
	}
	if err != nil {
		return err
	}
	secret, err := s.Vault.Open(encrypted, signatureScope(id))
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, eventLimit+1))
	if err != nil || len(body) == 0 || len(body) > eventLimit {
		return web.Fail(413, "业务事件内容过长或不可读取")
	}
	if !verifiedSignature(secret, r.Header.Get("X-Alliance-Timestamp"), r.Header.Get("X-Alliance-Signature"), body, time.Now()) {
		return web.Fail(403, "业务事件签名无效")
	}
	if !json.Valid(body) {
		return web.Fail(422, "业务事件须为 JSON 对象")
	}
	var event map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&event); err != nil || event == nil {
		return web.Fail(422, "业务事件须为 JSON 对象")
	}
	var nodes int
	if _, valid := normalizeEvent(event, 0, &nodes); !valid {
		return web.Fail(422, "业务事件结构超出允许范围")
	}
	filterRule, dedupRule, err := compileRules(filter, key)
	if err != nil {
		return err
	}
	evaluate, stop := context.WithTimeout(ctx, 70*time.Millisecond)
	defer stop()
	args := map[string]any{"event": event}
	match, _, err := filterRule.ContextEval(evaluate, args)
	if err != nil {
		return web.Fail(422, "事件字段与过滤条件不匹配")
	}
	allowed, typed := match.Value().(bool)
	if !typed {
		return web.Fail(422, "事件过滤必须返回布尔值")
	}
	if !allowed {
		return web.JSON(w, 202, map[string]string{"status": "ignored"})
	}
	value, _, err := dedupRule.ContextEval(evaluate, args)
	if err != nil {
		return web.Fail(422, "事件缺少有效的去重编号")
	}
	eventKey, typed := value.Value().(string)
	if !typed || !validText(eventKey, 240) {
		return web.Fail(422, "事件缺少有效的去重编号")
	}
	sample, err := json.Marshal(selectEventFields(event, fields))
	if err != nil {
		return err
	}
	status, err := s.recordEvent(ctx, id, version, cooldown, web.Hash(eventKey), sample)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "事件触发器不可用")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]string{"status": status})
}

func (s Server) recordEvent(ctx context.Context, id string, revision, cooldown int, fingerprint, sample []byte) (string, error) {
	state := "accepted"
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var active string
		if err := tx.QueryRow(ctx, `select t.id from event_triggers t where (t.id,t.revision)=($1,$2)
		 and t.enabled and space_role(t.space_id,t.maintainer) in ('member','space_admin')
		 and (t.connector_id is null or exists(select from business_connectors c where c.id=t.connector_id and c.enabled))
		 for update of t`, id, revision).Scan(&active); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "insert into event_trigger_deliveries(trigger_id,event_key_hash) values($1,$2) on conflict do nothing", id, fingerprint)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			state = "duplicate"
			return nil
		}
		var batch string
		err = tx.QueryRow(ctx, `select id from event_trigger_batches where trigger_id=$1 and trigger_revision=$2 and status='queued'
		 and ready_at>clock_timestamp() order by ready_at,id limit 1 for update`, id, revision).Scan(&batch)
		if err == nil {
			_, err = tx.Exec(ctx, "update event_trigger_batches set hits=hits+1,last_at=clock_timestamp() where id=$1", batch)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		batch = uuid.NewString()
		cipher, err := s.Vault.Seal(string(sample), sampleScope(batch))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into event_trigger_batches(id,trigger_id,trigger_revision,ready_at,sample_cipher)
		 values($1,$2,$3,clock_timestamp()+make_interval(secs=>$4::int),$5)`, batch, id, revision, cooldown, cipher)
		return err
	})
	return state, err
}

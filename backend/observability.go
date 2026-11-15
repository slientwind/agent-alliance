package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type LangfuseConfig struct {
	Enabled        bool   `json:"enabled"`
	BaseURL        string `json:"baseUrl"`
	PublicKey      string `json:"publicKey"`
	SecretKey      string `json:"secretKey,omitempty"`
	HasSecret      bool   `json:"hasSecret"`
	IncludeContent bool   `json:"includeContent"`
	ProjectID      string `json:"projectId"`
}
type observationEnvelope struct {
	Config LangfuseConfig  `json:"config"`
	Body   json.RawMessage `json:"body"`
}

func loadLangfuse(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, tenant string) (LangfuseConfig, error) {
	var c LangfuseConfig
	var b []byte
	e := q.QueryRowContext(ctx, `SELECT config FROM alliance_integrations WHERE tenant_id=$1 AND kind='langfuse'`, tenant).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	return c, e
}
func (s *Server) langfuseRoute(w http.ResponseWriter, r *http.Request, action string) (int, any, error) {
	if s.store.db == nil {
		return 0, nil, bad("需要 PostgreSQL 工作区")
	}
	ctx := r.Context()
	p, _ := ctx.Value(principalKey{}).(Principal)
	c, e := loadLangfuse(ctx, s.store.db, s.store.tenantID)
	if e != nil {
		return 0, nil, e
	}
	if action == "" && r.Method == "GET" {
		c.HasSecret = c.SecretKey != ""
		c.SecretKey = ""
		return 200, c, nil
	}
	if action == "exports" && r.Method == "GET" {
		rows, e := s.store.db.QueryContext(ctx, `SELECT id,status,attempts,last_error,created_at FROM alliance_outbox WHERE tenant_id=$1 AND kind='langfuse' ORDER BY created_at DESC LIMIT 100`, s.store.tenantID)
		if e != nil {
			return 0, nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, status, last string
			var attempts int
			var created time.Time
			if e = rows.Scan(&id, &status, &attempts, &last, &created); e != nil {
				return 0, nil, e
			}
			out = append(out, map[string]any{"id": id, "status": status, "attempts": attempts, "lastError": last, "createdAt": created})
		}
		return 200, out, rows.Err()
	}
	if p.Role != "admin" {
		return 0, nil, forbidden()
	}
	if action == "" && r.Method == "PUT" {
		var next LangfuseConfig
		if e = decode(w, r, &next); e != nil {
			return 0, nil, e
		}
		next.BaseURL = strings.TrimRight(next.BaseURL, "/")
		if e = validateURL(next.BaseURL); e != nil {
			return 0, nil, bad(e.Error())
		}
		if len(next.ProjectID) > 128 || strings.ContainsAny(next.ProjectID, "/?#") {
			return 0, nil, bad("项目 ID 无效")
		}
		if next.SecretKey == "" {
			next.SecretKey = c.SecretKey
		} else {
			next.SecretKey, e = s.store.encrypt(next.SecretKey)
			if e != nil {
				return 0, nil, e
			}
		}
		if next.Enabled && (next.PublicKey == "" || next.SecretKey == "") {
			return 0, nil, bad("启用时需要项目公钥和私钥")
		}
		b, _ := json.Marshal(next)
		_, e = s.store.db.ExecContext(ctx, `INSERT INTO alliance_integrations(tenant_id,kind,config) VALUES($1,'langfuse',$2) ON CONFLICT(tenant_id,kind) DO UPDATE SET config=excluded.config`, s.store.tenantID, b)
		next.HasSecret = next.SecretKey != ""
		next.SecretKey = ""
		return 200, next, e
	}
	if action == "retry" && r.Method == "POST" {
		_, e = s.store.db.ExecContext(ctx, `UPDATE alliance_outbox SET status='pending',attempts=0,available_at=now(),last_error='' WHERE tenant_id=$1 AND kind='langfuse' AND status='failed'`, s.store.tenantID)
		return 200, map[string]bool{"ok": true}, e
	}
	return 0, nil, missing()
}
func stableHex(value string, n int) string {
	b := sha256.Sum256([]byte(value))
	return hex.EncodeToString(b[:n])
}

var credentialPattern = regexp.MustCompile(`(?i)(bearer\s+[^\s"']+|sk-[a-z0-9_-]{8,}|api[_-]?key\s*[:=]\s*[^\s,;]+)`)

func redactObservation(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return credentialPattern.ReplaceAllString(value, "[REDACTED]")
}
func otlpBody(tenant string, run Run, include bool, secrets []string) json.RawMessage {
	traceID := stableHex(tenant+":"+run.TraceID, 16)
	rootID := stableHex(run.ID, 8)
	attr := func(k, v string) map[string]any {
		return map[string]any{"key": k, "value": map[string]string{"stringValue": v}}
	}
	makeSpan := func(id, parent, name, status, input, output, errText, typ string, start time.Time, end *time.Time, version int, tokens *int) map[string]any {
		ended := now()
		if end != nil {
			ended = *end
		}
		attrs := []map[string]any{attr("langfuse.observation.type", typ), attr("langfuse.trace.name", run.TargetName), attr("langfuse.session.id", run.ID), attr("langfuse.user.id", run.ActorID), attr("langfuse.version", strconv.Itoa(version)), attr("langfuse.trace.metadata.workspaceId", tenant), attr("langfuse.trace.metadata.runId", run.ID), attr("langfuse.trace.metadata.status", run.Status), attr("alliance.workspace.id", tenant)}
		if include {
			attrs = append(attrs, attr("langfuse.observation.input", redactObservation(input, secrets)), attr("langfuse.observation.output", redactObservation(output, secrets)))
		}
		if tokens != nil {
			attrs = append(attrs, attr("langfuse.observation.usage_details", fmt.Sprintf(`{"total":%d}`, *tokens)))
		}
		code := 1
		if status == "failed" || status == "cancelled" {
			code = 2
			msg := "运行失败或已取消（正文导出关闭）"
			if include {
				msg = redactObservation(errText, secrets)
			}
			attrs = append(attrs, attr("langfuse.observation.level", "ERROR"), attr("langfuse.observation.status_message", msg))
		}
		span := map[string]any{"traceId": traceID, "spanId": id, "name": name, "kind": 1, "startTimeUnixNano": strconv.FormatInt(start.UnixNano(), 10), "endTimeUnixNano": strconv.FormatInt(ended.UnixNano(), 10), "attributes": attrs, "status": map[string]any{"code": code}}
		if parent != "" {
			span["parentSpanId"] = parent
		}
		return span
	}
	spans := []map[string]any{makeSpan(rootID, "", run.TargetName, run.Status, run.Input, run.Output, run.Error, "agent", run.StartedAt, run.EndedAt, run.Version, nil)}
	for _, sp := range run.Spans {
		typ := "span"
		if sp.Tokens != nil {
			typ = "generation"
		}
		child := makeSpan(stableHex(sp.ID, 8), rootID, sp.Name, sp.Status, sp.Input, sp.Output, sp.Error, typ, sp.StartedAt, sp.EndedAt, sp.AgentVersion, sp.Tokens)
		if sp.Model != "" {
			child["attributes"] = append(child["attributes"].([]map[string]any), attr("langfuse.observation.model.name", sp.Model))
		}
		spans = append(spans, child)
	}
	b, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{"resource": map[string]any{"attributes": []any{attr("service.name", "agent-alliance"), attr("service.version", "production-v2")}}, "scopeSpans": []any{map[string]any{"scope": map[string]string{"name": "agent-alliance.runtime"}, "spans": spans}}}}})
	return b
}

// Runs and their export event commit atomically. A stable outbox ID prevents duplicate creation.
func (s *Store) enqueueObservations(ctx context.Context, tx *sql.Tx, st State) error {
	c, e := loadLangfuse(ctx, tx, s.tenantID)
	if e != nil || !c.Enabled {
		return e
	}
	var secrets []string
	if c.IncludeContent {
		for _, encrypted := range st.Secrets {
			v, e := s.decrypt(encrypted)
			if e != nil {
				return e
			}
			secrets = append(secrets, v)
		}
	}
	var previousState State
	var oldRaw []byte
	if e = tx.QueryRowContext(ctx, `SELECT data FROM alliance_states WHERE tenant_id=$1`, s.tenantID).Scan(&oldRaw); e != nil {
		return e
	}
	if e = json.Unmarshal(oldRaw, &previousState); e != nil {
		return e
	}
	for _, run := range st.Runs {
		if !terminal(run.Status) {
			continue
		}
		eventID := "lf_" + stableHex(s.tenantID+":"+run.ID, 16)
		// Avoid rebuilding payloads and exporting historical runs when enabling an integration.
		if terminal(previousState.Runs[run.ID].Status) {
			continue
		}
		// Include frozen credentials: a key may have been rotated during this run.
		runSecrets := append([]string{}, secrets...)
		if c.IncludeContent {
			var jobRaw []byte
			err := tx.QueryRowContext(ctx, `SELECT payload FROM alliance_jobs WHERE tenant_id=$1 AND run_id=$2`, s.tenantID, run.ID).Scan(&jobRaw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				var job JobPayload
				if err = json.Unmarshal(jobRaw, &job); err != nil {
					return err
				}
				for _, enc := range job.Secrets {
					v, err := s.decrypt(enc)
					if err != nil {
						return err
					}
					runSecrets = append(runSecrets, v)
				}
			}
		}
		b, _ := json.Marshal(observationEnvelope{c, otlpBody(s.tenantID, run, c.IncludeContent, runSecrets)})
		if _, e = tx.ExecContext(ctx, `INSERT INTO alliance_outbox(id,tenant_id,kind,payload) VALUES($1,$2,'langfuse',$3) ON CONFLICT(id) DO NOTHING`, eventID, s.tenantID, b); e != nil {
			return e
		}
	}
	return nil
}
func (w *Worker) exportObservation(ctx context.Context) error {
	var eventID, tenant string
	var b []byte
	var attempt int
	// Attempt number doubles as fencing token for exporter lease completion.
	e := w.DB.QueryRowContext(ctx, `WITH candidate AS (SELECT o.id FROM alliance_outbox o JOIN alliance_integrations i ON i.tenant_id=o.tenant_id AND i.kind=o.kind WHERE o.kind='langfuse' AND (i.config->>'enabled')::boolean AND (o.status='pending' OR (o.status='sending' AND o.lease_until<now())) AND o.available_at<=now() ORDER BY o.created_at FOR UPDATE OF o SKIP LOCKED LIMIT 1) UPDATE alliance_outbox o SET status='sending',owner=$1,attempts=attempts+1,lease_until=now()+interval '60 seconds' FROM candidate c WHERE o.id=c.id RETURNING o.id,o.tenant_id,o.payload,o.attempts`, w.ID).Scan(&eventID, &tenant, &b, &attempt)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var payload observationEnvelope
	e = json.Unmarshal(b, &payload)
	if e == nil {
		if attempt > 1 {
			payload.Body, e = w.reconcileObservation(ctx, tenant, payload)
		}
		if e == nil && len(payload.Body) > 0 {
			e = w.sendObservation(ctx, tenant, payload)
		}
	}
	status, last := "sent", ""
	delay := max(30*time.Second, time.Duration(1<<min(attempt, 10))*time.Second)
	if e != nil {
		status = "pending"
		last = "Langfuse 摄入失败，请检查连接与项目凭据"
		if attempt >= 10 {
			status = "failed"
		}
	}
	_, saveErr := w.DB.ExecContext(ctx, `UPDATE alliance_outbox SET status=$4,last_error=$5,available_at=now()+($6*interval '1 millisecond'),lease_until=NULL WHERE id=$1 AND owner=$2 AND attempts=$3 AND status='sending'`, eventID, w.ID, attempt, status, last, delay.Milliseconds())
	return saveErr
}
func (w *Worker) sendObservation(ctx context.Context, tenant string, p observationEnvelope) error {
	st, e := newDatabaseStore(w.DB, tenant, w.Key)
	if e != nil {
		return e
	}
	secret, e := st.decrypt(p.Config.SecretKey)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", p.Config.BaseURL+"/api/public/otel/v1/traces", bytes.NewReader(p.Body))
	if e != nil {
		return e
	}
	req.SetBasicAuth(p.Config.PublicKey, secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-langfuse-ingestion-version", "4")
	client := httpClient(w.AllowPrivate)
	defer client.CloseIdleConnections()
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	body, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil {
		return e
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("ingestion HTTP %d", res.StatusCode)
	}
	var result struct {
		PartialSuccess struct {
			Rejected json.Number `json:"rejectedSpans"`
		} `json:"partialSuccess"`
	}
	if len(body) > 0 {
		if e = json.Unmarshal(body, &result); e != nil {
			return e
		}
		if result.PartialSuccess.Rejected != "" && result.PartialSuccess.Rejected != "0" {
			return fmt.Errorf("ingestion rejected spans")
		}
	}
	return nil
}

// Check accepted IDs before retrying an uncertain delivery. This reduces duplicates; OTLP has no
// transactional acknowledgement across systems, so delayed downstream visibility remains a documented limit.
func (w *Worker) reconcileObservation(ctx context.Context, tenant string, p observationEnvelope) (json.RawMessage, error) {
	var body struct {
		ResourceSpans []struct {
			Resource   json.RawMessage `json:"resource"`
			ScopeSpans []struct {
				Scope json.RawMessage  `json:"scope"`
				Spans []map[string]any `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if e := json.Unmarshal(p.Body, &body); e != nil {
		return nil, e
	}
	if len(body.ResourceSpans) != 1 || len(body.ResourceSpans[0].ScopeSpans) != 1 {
		return nil, fmt.Errorf("unexpected export layout")
	}
	spans := body.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) == 0 {
		return nil, nil
	}
	trace, _ := spans[0]["traceId"].(string)
	st, e := newDatabaseStore(w.DB, tenant, w.Key)
	if e != nil {
		return nil, e
	}
	secret, e := st.decrypt(p.Config.SecretKey)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", p.Config.BaseURL+"/api/public/v2/observations?traceId="+trace+"&limit=1000", nil)
	if e != nil {
		return nil, e
	}
	req.SetBasicAuth(p.Config.PublicKey, secret)
	client := httpClient(w.AllowPrivate)
	defer client.CloseIdleConnections()
	res, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("cannot reconcile ingestion HTTP %d", res.StatusCode)
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Meta struct {
			Cursor string `json:"cursor"`
		} `json:"meta"`
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 4*1024*1024)).Decode(&result); e != nil {
		return nil, e
	}
	if result.Meta.Cursor != "" {
		return nil, fmt.Errorf("observation reconciliation needs pagination")
	}
	existing := map[string]bool{}
	for _, o := range result.Data {
		existing[o.ID] = true
	}
	remaining := []map[string]any{}
	for _, sp := range spans {
		sid, _ := sp["spanId"].(string)
		if !existing[sid] {
			remaining = append(remaining, sp)
		}
	}
	if len(remaining) == 0 {
		return nil, nil
	}
	body.ResourceSpans[0].ScopeSpans[0].Spans = remaining
	return json.Marshal(body)
}

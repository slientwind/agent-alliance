// Package telemetry sends committed operational spans to an enterprise-configured
// OTLP receiver. Business execution does not wait for diagnostic delivery.
package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type job struct {
	run, lease                    string
	ordinal, attempt, callAttempt int
}
type exporter struct {
	db       *pgxpool.Pool
	endpoint string
	client   *http.Client
}

func Serve(ctx context.Context, db *pgxpool.Pool) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		return
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		slog.Error("invalid traces receiver endpoint")
		return
	}
	e := exporter{db, endpoint, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	defer e.client.CloseIdleConnections()
	for ctx.Err() == nil {
		didWork, err := e.next(ctx)
		modelWork, modelErr := e.nextGenAI(ctx)
		if (err != nil || modelErr != nil) && ctx.Err() == nil {
			slog.Warn("trace export pending")
		}
		if err != nil || modelErr != nil || !didWork && !modelWork {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

func (e exporter) next(ctx context.Context) (bool, error) {
	var j job
	err := e.db.QueryRow(ctx, `with due as (select o.run_id,o.ordinal,o.call_attempt from telemetry_outbox o join runs r on r.id=o.run_id where o.outcome='pending' and o.next_at<=now() and (r.trace_expires_at is null or r.trace_expires_at>clock_timestamp()) order by next_at for update of o skip locked limit 1)
 update telemetry_outbox o set next_at=now()+interval '30 seconds',lease=gen_random_uuid(),attempts=attempts+1 from due where(o.run_id,o.ordinal,o.call_attempt)=(due.run_id,due.ordinal,due.call_attempt) returning o.run_id,o.ordinal,o.attempts,o.lease,o.call_attempt`).Scan(&j.run, &j.ordinal, &j.attempt, &j.lease, &j.callAttempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	span, err := e.span(ctx, j)
	if err != nil {
		return true, err
	}
	outcome, retry, err := e.exportSpan(ctx, span)
	if err != nil {
		return true, err
	}
	if retry <= 0 {
		retry = time.Duration(1<<min(j.attempt, 8)) * time.Second
	}
	_, err = e.db.Exec(ctx, `update telemetry_outbox set outcome=$1,lease=null,next_at=now()+$2*interval '1 second' where run_id=$3 and ordinal=$4 and lease=$5 and call_attempt=$6`, outcome, max(1, int(retry.Seconds())), j.run, j.ordinal, j.lease, j.callAttempt)
	return true, err
}

func (e exporter) exportSpan(ctx context.Context, span *trace.Span) (string, time.Duration, error) {
	payload := &collector.ExportTraceServiceRequest{ResourceSpans: []*trace.ResourceSpans{{
		Resource: &resource.Resource{Attributes: []*common.KeyValue{textAttr("service.name", "alliance-worker")}},
		ScopeSpans: []*trace.ScopeSpans{{
			Scope: &common.InstrumentationScope{Name: "alliance.runtime"}, Spans: []*trace.Span{span},
		}},
	}}}
	body, err := proto.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	outcome, retry := e.send(ctx, body)
	return outcome, retry, nil
}

func textAttr(key, value string) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}}
}
func intAttr(key string, value int64) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: value}}}
}
func spanID(run string, ordinal int) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", run, ordinal)))
	return sum[:8]
}

// TraceParent identifies the same run and step as the exported client span.
func TraceParent(run string, ordinal int) string {
	return "00-" + strings.ReplaceAll(run, "-", "") + "-" + hex.EncodeToString(spanID(run, ordinal)) + "-01"
}
func (e exporter) span(ctx context.Context, j job) (*trace.Span, error) {
	tid, err := hex.DecodeString(strings.ReplaceAll(j.run, "-", ""))
	if err != nil {
		return nil, err
	}
	s := &trace.Span{TraceId: tid, SpanId: spanID(j.run, j.ordinal), Attributes: []*common.KeyValue{textAttr("alliance.run.id", j.run)}}
	var started, ended time.Time
	var status string
	if j.ordinal == 0 {
		s.Name = "agent.run"
		s.Kind = trace.Span_SPAN_KIND_INTERNAL
		var plan int64
		err = e.db.QueryRow(ctx, "select created_at,finished_at,status,plan_version from runs where id=$1", j.run).Scan(&started, &ended, &status, &plan)
		s.Attributes = append(s.Attributes, intAttr("alliance.plan.version", plan))
	} else {
		s.Name = "agent.invoke"
		s.Kind = trace.Span_SPAN_KIND_CLIENT
		s.ParentSpanId = spanID(j.run, 0)
		var kind, agent, model string
		var revision int64
		var tokens *int64
		var deps [][]byte
		err = e.db.QueryRow(ctx, `select i.started_at,i.ended_at,i.status,i.tokens,call_span_id(i.run_id,i.ordinal,i.attempt_no),n.agent_id,n.agent_revision,
   case when ar.tool is not null then 'tool' when ar.card is not null then 'external' else 'model' end,coalesce(mr.model,''),
   array(select call_span_id(dep.run_id,dep.ordinal,dep.attempt_no) from node_edges ed join invocations dep on(dep.run_id,dep.ordinal)=(r.id,ed.depends_on) where(ed.project_id,ed.plan_version,ed.ordinal)=(n.project_id,n.plan_version,n.ordinal) and not exists(select from change_batches where run_id=r.id) order by depends_on)
   from call_attempts i join runs r on r.id=i.run_id join effective_run_nodes n on(n.run_id,n.ordinal)=(r.id,i.ordinal)
   join agent_revisions ar on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision)
   left join model_revisions mr on (mr.resource_id,mr.revision)=(ar.resource_id,ar.resource_revision)
   where i.run_id=$1 and i.ordinal=$2 and i.attempt_no=$3`, j.run, j.ordinal, j.callAttempt).
			Scan(&started, &ended, &status, &tokens, &s.SpanId, &agent, &revision, &kind, &model, &deps)
		s.Attributes = append(s.Attributes, intAttr("alliance.call.attempt", int64(j.callAttempt)), intAttr("alliance.step", int64(j.ordinal)), textAttr("alliance.agent.id", agent), intAttr("alliance.agent.revision", revision), textAttr("alliance.operation", kind))
		if tokens != nil {
			s.Attributes = append(s.Attributes, intAttr("alliance.tokens", *tokens))
		}
		if kind == "model" && model != "" {
			s.Attributes = append(s.Attributes, textAttr("gen_ai.request.model", model))
		}
		for _, parent := range deps {
			s.Links = append(s.Links, &trace.Span_Link{TraceId: tid, SpanId: parent})
		}
	}
	if err != nil {
		return nil, err
	}
	s.Attributes = append(s.Attributes, textAttr("alliance.status", status))
	s.StartTimeUnixNano = uint64(started.UnixNano())
	s.EndTimeUnixNano = uint64(ended.UnixNano())
	if status == "completed" {
		s.Status = &trace.Status{Code: trace.Status_STATUS_CODE_OK}
	} else if status == "unknown" || status == "failed" {
		s.Status = &trace.Status{Code: trace.Status_STATUS_CODE_ERROR}
	}
	rows, err := e.db.Query(ctx, `select occurred_at,kind,code from trace_events where run_id=$1 and coalesce(ordinal,0)=$2 and coalesce(call_attempt,1)=$3 order by seq desc limit 128`, j.run, j.ordinal, j.callAttempt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var at time.Time
		var kind, code string
		if err := rows.Scan(&at, &kind, &code); err != nil {
			return nil, err
		}
		s.Events = append(s.Events, &trace.Span_Event{TimeUnixNano: uint64(at.UnixNano()), Name: kind, Attributes: []*common.KeyValue{textAttr("alliance.state", code)}})
	}
	for i, k := 0, len(s.Events)-1; i < k; i, k = i+1, k-1 {
		s.Events[i], s.Events[k] = s.Events[k], s.Events[i]
	}
	return s, rows.Err()
}
func (e exporter) send(ctx context.Context, body []byte) (string, time.Duration) {
	req, err := http.NewRequestWithContext(ctx, "POST", e.endpoint, bytes.NewReader(body))
	if err != nil {
		return "rejected", 0
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	res, err := e.client.Do(req)
	if err != nil {
		return "pending", 0
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 429, 502, 503, 504:
		raw := res.Header.Get("Retry-After")
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			return "pending", time.Duration(min(seconds, 86400)) * time.Second
		}
		if until, err := http.ParseTime(raw); err == nil {
			return "pending", min(time.Until(until), 24*time.Hour)
		}
		return "pending", 0
	case 200:
		buf, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
		if err != nil {
			return "pending", 0
		}
		var ack collector.ExportTraceServiceResponse
		if len(buf) > 1<<20 || proto.Unmarshal(buf, &ack) != nil {
			return "rejected", 0
		}
		if ack.GetPartialSuccess().GetRejectedSpans() > 0 {
			return "partial", 0
		}
		return "delivered", 0
	default:
		return "rejected", 0
	}
}

package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
)

type genAIJob struct {
	id, run, lease string
	operation      *string
	score          *int
	occurred       time.Time
	attempts       int
}

func (e exporter) nextGenAI(ctx context.Context) (bool, error) {
	var j genAIJob
	err := e.db.QueryRow(ctx, `with due as (
		select o.id from genai_telemetry_outbox o join runs r on r.id=o.run_id
		where o.outcome='pending' and o.next_at<=now() and (r.trace_expires_at is null or r.trace_expires_at>clock_timestamp())
		order by next_at,o.id for update of o skip locked limit 1
	)
	update genai_telemetry_outbox o set next_at=now()+interval '30 seconds',lease=gen_random_uuid(),attempts=attempts+1
	from due where o.id=due.id returning o.id,o.run_id,o.lease,o.operation_key,o.evaluation_score,o.occurred_at,o.attempts`).
		Scan(&j.id, &j.run, &j.lease, &j.operation, &j.score, &j.occurred, &j.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	span, err := e.genAISpan(ctx, j)
	if err != nil {
		return true, err
	}
	outcome, retry, err := e.exportSpan(ctx, span)
	if err != nil {
		return true, err
	}
	if retry <= 0 {
		retry = time.Duration(1<<min(j.attempts, 8)) * time.Second
	}
	_, err = e.db.Exec(ctx, `update genai_telemetry_outbox set outcome=$1,lease=null,next_at=now()+$2*interval '1 second'
		where id=$3 and lease=$4`, outcome, max(1, int(retry.Seconds())), j.id, j.lease)
	return true, err
}

func (e exporter) genAISpan(ctx context.Context, j genAIJob) (*trace.Span, error) {
	id, err := hex.DecodeString(strings.ReplaceAll(j.run, "-", ""))
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte("genai/" + j.id))
	span := &trace.Span{TraceId: id, SpanId: key[:8], ParentSpanId: spanID(j.run, 0),
		Attributes: []*common.KeyValue{textAttr("alliance.run.id", j.run)}}
	if j.score != nil {
		at := uint64(j.occurred.UnixNano())
		span.Name, span.Kind = "gen_ai.evaluation.result", trace.Span_SPAN_KIND_INTERNAL
		span.StartTimeUnixNano, span.EndTimeUnixNano = at, at
		labels := [2]string{"thumbs_down", "thumbs_up"}
		attrs := []*common.KeyValue{
			textAttr("gen_ai.evaluation.name", "user_feedback"),
			doubleAttr("gen_ai.evaluation.score.value", float64(*j.score)),
			textAttr("gen_ai.evaluation.score.label", labels[*j.score]),
		}
		span.Attributes = append(span.Attributes, attrs...)
		span.Events = []*trace.Span_Event{{Name: span.Name, TimeUnixNano: at, Attributes: attrs}}
		return span, nil
	}
	var t inference
	err = e.db.QueryRow(ctx, `select m.upstream_model,p.kind,c.tier,a.status,a.attempt,a.turn,a.fallback,a.streamed,
		c.started_at,a.started_at,a.finished_at,a.first_chunk_at,c.prompt_tokens,c.completion_tokens,c.cached_tokens,a.provider_status
		from model_gateway_attempts a join model_gateway_charges c using(operation_key)
		join model_catalog m on m.id=a.model_id join model_providers p on p.id=m.provider_id
		where a.operation_key=$1 and a.run_id=$2`, *j.operation, j.run).Scan(
		&t.model, &t.provider, &t.tier, &t.status, &t.attempt, &t.turn, &t.fallback, &t.streamed,
		&t.reserved, &t.started, &t.ended, &t.first, &t.input, &t.output, &t.cached, &t.providerStatus)
	if err != nil {
		return nil, err
	}
	t.addSpanData(span)
	return span, nil
}

type inference struct {
	model, provider, tier, status         string
	attempt                               int64
	turn                                  *int64
	fallback, streamed                    bool
	reserved, started, ended              time.Time
	first                                 *time.Time
	input, output, cached, providerStatus *int64
}

func (t inference) addSpanData(span *trace.Span) {
	operation := "chat"
	switch t.tier {
	case "image":
		operation = "generate_content"
	case "embedding":
		operation = "embeddings"
	}
	span.Name, span.Kind = operation+" "+t.model, trace.Span_SPAN_KIND_CLIENT
	span.StartTimeUnixNano, span.EndTimeUnixNano = uint64(t.started.UnixNano()), uint64(t.ended.UnixNano())
	span.Attributes = append(span.Attributes,
		textAttr("gen_ai.operation.name", operation), textAttr("gen_ai.provider.name", t.provider),
		textAttr("gen_ai.request.model", t.model), textAttr("alliance.model.tier", t.tier),
		textAttr("alliance.status", t.status), intAttr("alliance.call.attempt", t.attempt),
		boolAttr("gen_ai.request.stream", t.streamed), boolAttr("alliance.model.fallback", t.fallback),
		doubleAttr("alliance.model.admission_to_start.seconds", max(0, t.started.Sub(t.reserved).Seconds())),
		doubleAttr("alliance.model.inference.seconds", t.ended.Sub(t.started).Seconds()))
	if t.first != nil {
		span.Attributes = append(span.Attributes,
			doubleAttr("gen_ai.response.time_to_first_chunk", t.first.Sub(t.started).Seconds()),
			doubleAttr("alliance.model.after_first_chunk.seconds", t.ended.Sub(*t.first).Seconds()))
	}
	for _, v := range []struct {
		key   string
		value *int64
	}{
		{"gen_ai.usage.input_tokens", t.input},
		{"gen_ai.usage.output_tokens", t.output},
		{"gen_ai.usage.cache_read.input_tokens", t.cached},
		{"http.response.status_code", t.providerStatus},
		{"alliance.turn", t.turn},
	} {
		if v.value != nil {
			span.Attributes = append(span.Attributes, intAttr(v.key, *v.value))
		}
	}
	if t.status == "succeeded" {
		span.Status = &trace.Status{Code: trace.Status_STATUS_CODE_OK}
	} else {
		span.Status = &trace.Status{Code: trace.Status_STATUS_CODE_ERROR}
	}
}

func doubleAttr(key string, value float64) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_DoubleValue{DoubleValue: value}}}
}

func boolAttr(key string, value bool) *common.KeyValue {
	return &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_BoolValue{BoolValue: value}}}
}

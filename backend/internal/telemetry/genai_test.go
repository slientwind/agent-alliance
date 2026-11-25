package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	trace "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestGenAITraceExportPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, stop := context.WithTimeout(t.Context(), 15*time.Second)
	defer stop()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	member, team, project, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	provider, model, price, operation := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'测试员工','unused','member')", member, member+"@example.invalid")
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'Trace 示例')", project, member, team)
	exec("insert into plans(project_id,version,brief) values($1,1,'trace fixture')", project)
	exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls,finished_at)
		values($1,$2,1,$3,'completed',$4,$5,digest('usage','sha256'),1,now())`, run, project, member, team, run)
	exec("insert into model_providers(id,name,kind,endpoint) values($1,$2,'openai','https://private-provider.example')", provider, provider)
	exec("insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens) values($1,$2,'trace-model','{text}',8000,500)", model, provider)
	exec(`insert into model_pricing(id,model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
		values($1,$2,now(),1,1,0,0)`, price, model)
	started := time.Now().UTC().Truncate(time.Microsecond)
	exec(`insert into model_gateway_charges(operation_key,team_id,member_id,price_id,tier,month_start,reserved_micro_cny,cost_micro_cny,
		prompt_tokens,completion_tokens,cached_tokens,state,started_at,settled_at)
		values($1,$2,$3,$4,'reasoning',date_trunc('month',now())::date,0,0,50,10,5,'settled',$5,$6)`,
		operation, team, member, price, started.Add(-100*time.Millisecond), started.Add(time.Second))
	exec(`insert into model_gateway_attempts(operation_key,model_id,attempt,fallback,status,started_at,finished_at,run_id,turn,first_chunk_at,streamed)
		values($1,$2,2,true,'succeeded',$3,$4,$5,1,$6,true)`, operation, model, started, started.Add(time.Second), run, started.Add(250*time.Millisecond))
	exec("insert into run_quality_signals(run_id,actor,kind) values($1,$2,'feedback_thumbs_up')", run, member)

	spans := make(chan *trace.Span, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		var request collector.ExportTraceServiceRequest
		if err != nil || proto.Unmarshal(payload, &request) != nil || r.Header.Get("Content-Type") != "application/x-protobuf" {
			w.WriteHeader(400)
			return
		}
		item := request.ResourceSpans[0].ScopeSpans[0].Spans[0]
		spans <- item
		w.WriteHeader(200)
	}))
	defer server.Close()
	e := exporter{db: db, endpoint: server.URL, client: server.Client()}
	var modelSpan, evaluation *trace.Span
	for range 2 {
		ok, err := e.nextGenAI(ctx)
		if err != nil || !ok {
			t.Fatalf("missing queued span: %t %v", ok, err)
		}
		select {
		case span := <-spans:
			switch span.Name {
			case "chat trace-model":
				modelSpan = span
			case "gen_ai.evaluation.result":
				evaluation = span
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if modelSpan == nil || evaluation == nil || string(modelSpan.TraceId) != string(evaluation.TraceId) {
		t.Fatal("expected inference and evaluation on one trace")
	}
	attributes := func(span *trace.Span) map[string]*common.AnyValue {
		all := map[string]*common.AnyValue{}
		for _, pair := range span.Attributes {
			all[pair.Key] = pair.Value
		}
		return all
	}
	inference := attributes(modelSpan)
	if inference["gen_ai.usage.input_tokens"].GetIntValue() != 50 ||
		inference["gen_ai.usage.output_tokens"].GetIntValue() != 10 ||
		inference["gen_ai.usage.cache_read.input_tokens"].GetIntValue() != 5 ||
		inference["gen_ai.response.time_to_first_chunk"].GetDoubleValue() != 0.25 ||
		inference["alliance.model.after_first_chunk.seconds"].GetDoubleValue() != 0.75 ||
		!inference["alliance.model.fallback"].GetBoolValue() {
		t.Fatalf("missing exact model measurements: %v", inference)
	}
	score := attributes(evaluation)["gen_ai.evaluation.score.value"].GetDoubleValue()
	if score != 1 || len(evaluation.Events) != 1 || strings.Contains(evaluation.String(), member) ||
		strings.Contains(modelSpan.String(), "private-provider") {
		t.Fatal("wrong evaluation score or leaked internal caller metadata")
	}
	var delivered int
	if err := db.QueryRow(ctx, "select count(*) from genai_telemetry_outbox where run_id=$1 and outcome='delivered'", run).Scan(&delivered); err != nil || delivered != 2 {
		t.Fatalf("export receipts = %d, error = %v", delivered, err)
	}
}

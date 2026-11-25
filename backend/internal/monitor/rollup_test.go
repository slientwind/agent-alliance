package monitor

import (
	"alliance/internal/database"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMonitorRollupUpdatesDirtyHoursAndReportsCosts(t *testing.T) {
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
	team, owner, project, run, agent := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	provider, model, price := uuid.NewString(), uuid.NewString(), uuid.NewString()
	bucket := time.Date(2019, 5, 2, 16, 0, 0, 0, time.UTC)
	exec(`insert into teams(id,name) values($1,$2)`, team, "rollup-"+team)
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'rollup test','','member')`, owner, owner+"@example.test")
	exec(`insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'rollup test')`, project, owner, team)
	exec(`insert into plans(project_id,version,brief) values($1,1,'rollup test')`, project)
	exec(`insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,status,quality_track,created_at)
	 values($1,$2,1,$3,$4,'rollup-test','\x00',2,'completed','lightweight',$5)`, run, project, owner, team, bucket)
	exec(`insert into agents(id,owner,team_id) values($1,$2,$3)`, agent, owner, team)
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://model.example.test')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'rollup-model',array['text'],4096,512)`, model, provider)
	exec(`insert into model_pricing(id,model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,$2,now(),1,1,0,0)`, price, model)

	key1, key2 := uuid.NewString(), uuid.NewString()
	for i, key := range []string{key1, key2} {
		started := bucket.Add(time.Duration(i) * time.Minute)
		ended := started.Add(time.Duration(100+200*i) * time.Millisecond)
		exec(`insert into model_gateway_charges(operation_key,team_id,member_id,price_id,tier,month_start,reserved_micro_cny,started_at)
		 values($1,$2,$3,$4,'fast','2019-05-01',50,$5)`, key, team, owner, price, started)
		status := "failed"
		if i == 1 {
			status = "succeeded"
		}
		exec(`insert into model_gateway_attempts(operation_key,model_id,attempt,fallback,status,started_at,finished_at,run_id,agent_id)
		 values($1,$2,$3,$4,$5,$6,$7,$8,$9)`, key, model, i+1, i > 0, status, started, ended, run, agent)
	}
	flush := func() {
		t.Helper()
		for range 20 {
			did, err := rollupOnce(ctx, tx)
			if err != nil {
				t.Fatal(err)
			}
			if !did {
				return
			}
		}
		t.Fatal("rollup queue did not drain")
	}
	flush()
	var calls, succeeded, failed, fallback, p50, p95 int
	if err := tx.QueryRow(ctx, `select calls_total,calls_succeeded,calls_failed,fallback_count,p50_ms,p95_ms
	 from monitor_model_hourly_stats where bucket_hour=$1 and team_id=$2 and model_id=$3`, bucket, team, model).
		Scan(&calls, &succeeded, &failed, &fallback, &p50, &p95); err != nil || calls != 2 || succeeded != 1 || failed != 1 || fallback != 1 || p50 != 200 || p95 != 290 {
		t.Fatalf("calls=%d succeeded=%d failed=%d fallback=%d p50=%d p95=%d err=%v", calls, succeeded, failed, fallback, p50, p95, err)
	}
	exec(`update model_gateway_charges set state='settled',settled_at=now(),cost_micro_cny=31,
	 prompt_tokens=32,completion_tokens=10,cached_tokens=4,image_count=0 where operation_key=$1`, key2)
	exec(`insert into run_quality_signals(run_id,actor,kind) values($1,$2,'reply_copied')`, run, owner)
	flush()
	var matched bool
	if err := tx.QueryRow(ctx, `select runs_total=1 and runs_succeeded=1 and calls_total=2 and lightweight_total=1 and lightweight_effective=1
	 and prompt_tokens=32 and completion_tokens=10 and cached_tokens=4 and cost_micro_cny=31
	 from monitor_agent_hourly_stats where bucket_hour=$1 and team_id=$2 and agent_id=$3`, bucket, team, agent).
		Scan(&matched); err != nil || !matched {
		t.Fatalf("agent snapshot did not reflect late billing and positivity: %v", err)
	}
	var overview struct {
		KPI struct {
			Calls        int
			CostMicroCny string
		}
		Funnel struct{ Submitted, Completed, OutputPassed, Accepted int }
	}
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, dashboardQuery, bucket, bucket.Add(time.Hour), team).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &overview); err != nil || overview.KPI.Calls != 2 || overview.KPI.CostMicroCny != "31" ||
		overview.Funnel.Submitted != 1 || overview.Funnel.Completed != 1 || overview.Funnel.OutputPassed != 1 || overview.Funnel.Accepted != 1 {
		t.Fatalf("wrong dashboard summary %s: %v", raw, err)
	}
	if err := tx.QueryRow(ctx, agentBoardQuery, bucket, bucket.Add(time.Hour), team, "model", "p95", 0).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var board []struct {
		AgentID string
		P95Ms   int
	}
	if err := json.Unmarshal(raw, &board); err != nil || len(board) != 1 || board[0].AgentID != agent || board[0].P95Ms != 300 {
		t.Fatalf("wrong agent board %s: %v", raw, err)
	}
}

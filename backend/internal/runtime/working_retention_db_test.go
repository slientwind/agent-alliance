package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetireDerivedWorkingPayloadsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a2", 32))
	if err != nil {
		t.Fatal(err)
	}
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	seed, _ := seedLookupTurn(t, ctx, worker, "https://provider.example.invalid/v1")
	run := seed.Run
	var project, actor, agent string
	if err := db.QueryRow(ctx, `select r.project_id,r.actor,n.agent_id from runs r
     join nodes n on (n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,1) where r.id=$1`, run).Scan(&project, &actor, &agent); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, `insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision,lookup_phase)
     select $1,1,step,$2,1,'' from generate_series(3,8) step`, project, agent)
	lookupTestSQL(t, ctx, db, "update runs set max_calls=8 where id=$1", run)
	lookupTestSQL(t, ctx, db, `insert into invocations(run_id,ordinal,status,tokens,result) values
     ($1,2,'completed',21,null),($1,3,'submitted',null,null),($1,4,'completed',8,null),
     ($1,5,'submitted',null,null),($1,6,'completed',7,'未采纳的短期结果'),($1,7,'completed',9,null)`, run)
	lookupTestSQL(t, ctx, db, `insert into agent_build_manifests(agent_id,revision,digest,definition)
     select $1,1,agent_build_digest(definition),definition from agent_build_definition($1,1) definition`, agent)
	lookupTestSQL(t, ctx, db, `insert into run_working_sets(run_id,ordinal,attempt_no,plan_version,control_revision,build_agent,build_revision,build_digest)
     select $1,s,1,1,1,a.agent_id,a.revision,a.digest from generate_series(1,7) s cross join agent_build_manifests a where a.agent_id=$2`, run, agent)
	// Only the first attempt reads the preference directly. Several later
	// attempts receive its result through a chain of captured predecessors.
	lookupTestSQL(t, ctx, db, `insert into run_working_parents values
     ($1,2,1,$1,1,1),($1,4,1,$1,2,1),($1,5,1,$1,2,1),($1,6,1,$1,1,1),($1,7,1,$1,2,1)`, run)
	preference := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into preferences(id,member_id,body,agent_scope) values($1,$2,'短期工作习惯',$3)", preference, actor, agent)
	lookupTestSQL(t, ctx, db, "insert into plan_scoped_preferences values($1,1,$2,1,$3)", project, preference, agent)
	lookupTestSQL(t, ctx, db, "insert into run_working_inputs values($1,1,1,'preference',$2,1)", run, preference)
	lookupTestSQL(t, ctx, db, `insert into text_reply_checkpoints(run_id,ordinal,attempt_no,payload) values($1,2,1,decode('ef','hex'))`, run)
	lookupTestSQL(t, ctx, db, `insert into lookup_turns(run_id,ordinal,attempt_no,plan_version,decision,initial_evidence,refinement_evidence)
     values($1,4,1,1,decode('ab','hex'),'[]','[]')`, run)
	lookupTestSQL(t, ctx, db, `insert into local_action_observations(run_id,ordinal,attempt_no,control_revision,payload,outcome)
     values($1,4,1,1,decode('cd','hex'),'lookup_refined')`, run)
	lookupTestSQL(t, ctx, db, `insert into lookup_question_handoffs(run_id,target,parent,parent_attempt,plan_version,question,citations)
     values($1,8,7,1,1,'需要补充哪一版报价的原句？','[]')`, run)
	lookupTestSQL(t, ctx, db, `insert into artifact_versions(artifact_id,version,body,reason)
     select id,1,'先前已独立采纳的成果','original' from artifacts where run_id=$1`, run)
	lookupTestSQL(t, ctx, db, `insert into reviews(artifact_id,version,reviewer)
     select id,1,$2 from artifacts where run_id=$1`, run, actor)
	lookupTestSQL(t, ctx, db, `insert into spend(op_key,team_id,actor,project_id,run_id,kind,amount_cny,unknown_authorized,settled,resource_budget,source_revision,source_name)
     select r.id::text||'-2',r.charge_team,r.actor,r.project_id,r.id,'model',0.2,false,true,b.budget_id,b.source_revision,b.source_name
     from runs r join agent_budget_bindings b on (b.agent_id,b.revision)=($2,1) where r.id=$1`, run, agent)
	const obsolete, independent = "受影响的草稿", "独立步骤的草稿"
	for step, draft := range map[int]string{3: independent, 5: obsolete} {
		if written, err := recordHistoricalDraft(ctx, db, Step{run, step}, 1, 1, 0, draft); err != nil || !written {
			t.Fatalf("write initial draft %d: written=%t err=%v", step, written, err)
		}
	}

	routes := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(routes, identity.Server{DB: db})
	api := httptest.NewServer(routes)
	defer api.Close()
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(token), actor)
	stream, closeStream := openDraftResponse(t, ctx, api.URL+"/api/projects/"+project+"/session-events", token, "")
	defer closeStream()
	snapshot := func() string {
		return readDraftFrame(t, stream, func(frame string) bool { return strings.Contains(frame, "event: session.snapshot") })
	}
	if got := snapshot(); !strings.Contains(got, obsolete) || !strings.Contains(got, independent) {
		t.Fatalf("both authorized drafts should be visible: %q", got)
	}

	// Reaching the deadline does not mutate the preference revision or fire
	// the explicit edit trigger. Reads still have to hide affected payloads.
	lookupTestSQL(t, ctx, db, "update preferences set expires_at=now()-interval '1 second' where id=$1", preference)
	if got := snapshot(); strings.Contains(got, obsolete) || !strings.Contains(got, independent) {
		t.Fatalf("expiry did not replace just the affected part of the stream: %q", got)
	}
	if written, err := recordHistoricalDraft(ctx, db, Step{run, 5}, 1, 1, len(obsolete), "迟到的流式片段"); err != nil || written {
		t.Fatalf("obsolete stream accepted another fragment: written=%t err=%v", written, err)
	}
	check := func(reason, query string, args ...any) {
		t.Helper()
		var ok bool
		if err := db.QueryRow(ctx, query, args...).Scan(&ok); err != nil || !ok {
			t.Fatalf("%s: ok=%t err=%v", reason, ok, err)
		}
	}
	check("request is already unusable before cleanup", `select not run_working_set_usable($1,2,1)
     and not usable and question is not null from lookup_question_availability where run_id=$1`, run)
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "select from runs where id=$1 for update", run); err != nil {
		t.Fatal(err)
	}
	if err := worker.retireWorkingPayloads(ctx, 2); err != nil {
		t.Fatal(err)
	}
	check("locked run was left for the next sweep", "select state='pending' from text_reply_checkpoints where run_id=$1", run)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Small batches exercise draining more than one transaction per sweep.
	if err := worker.retireWorkingPayloads(ctx, 2); err != nil {
		t.Fatal(err)
	}
	check("transient payloads retired", `select
     (select state='discarded' and payload is null from text_reply_checkpoints where run_id=$1)
     and (select decision is null and initial_evidence is null and refinement_evidence is null and outcome='invalidated' from lookup_turns where run_id=$1)
     and (select discarded_at is not null and payload is null from local_action_observations where run_id=$1)
     and (select question is null and citations='[]' and not usable from lookup_question_availability where run_id=$1)
     and (select result is null from invocations where (run_id,ordinal)=($1,6))
     and (select array_agg(ordinal)=array[3] from run_draft_fragments where run_id=$1)`, run)
	check("approved artifact, effective sibling, budget and original links preserved", `select
     (select v.body='先前已独立采纳的成果' from artifacts a join artifact_versions v on v.artifact_id=a.id
        join reviews approved using(artifact_id,version) where a.run_id=$1)
     and (select result is not null from invocations where (run_id,ordinal)=($1,1))
     and (select status='completed' and tokens=21 from invocations where (run_id,ordinal)=($1,2))
     and (select count(*)=1 and sum(amount_cny)=0.2 from spend where run_id=$1 and settled)
     and (select count(*)=5 from run_working_parents where run_id=$1)
     and (select count(*)=1 from run_working_inputs where run_id=$1 and kind='preference')
     and run_working_set_usable($1,3,1)`, run)
	update := readDraftFrame(t, stream, func(frame string) bool { return strings.Contains(frame, "event: project.changed") })
	if strings.Contains(update, obsolete) || strings.Contains(update, "需要补充") {
		t.Fatalf("cleanup event contained retired content: %q", update)
	}
	var remaining int
	if err := db.QueryRow(ctx, "select retire_invalid_working_payloads(128)").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("cleanup replay did work: remaining=%d err=%v", remaining, err)
	}
}

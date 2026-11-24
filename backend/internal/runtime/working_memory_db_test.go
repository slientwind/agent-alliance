package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/memory"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkingMemoryPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	prompts := make(chan string, 8)
	releases := make(chan chan struct{}, 8)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "missing request", 400)
			return
		}
		prompts <- string(body)
		select {
		case ch := <-releases:
			select {
			case <-ch:
			case <-r.Context().Done():
				return
			}
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已核对工作结果"}}],"usage":{"total_tokens":21}}`)
	}))
	defer func() {
		provider.CloseClientConnections()
		provider.Close()
	}()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	mux := http.NewServeMux()
	access := identity.Server{DB: db}
	knowledge.Server{DB: db}.Routes(mux, access)
	projects.Server{DB: db}.Routes(mux, access)
	const private = "优先按我的自定义顺序排列"
	const ruleBody = "先检查团队必需报价字段"
	type fixture struct {
		step                                   Step
		project, team, actor, preference, rule string
		token                                  string
	}
	makeFixture := func(t *testing.T) fixture {
		t.Helper()
		step, _ := seedLookupTurn(t, ctx, worker, provider.URL)
		f := fixture{step: step, preference: uuid.NewString(), rule: uuid.NewString(), token: web.Token()}
		if err := db.QueryRow(ctx, "select project_id,charge_team,actor from runs where id=$1", f.step.Run).Scan(&f.project, &f.team, &f.actor); err != nil {
			t.Fatal(err)
		}
		lookupTestSQL(t, ctx, db, "update nodes set lookup_phase='' where project_id=$1 and ordinal=$2", f.project, f.step.Ordinal)
		lookupTestSQL(t, ctx, db, "insert into preferences(id,member_id,body) values($1,$2,$3)", f.preference, f.actor, private)
		lookupTestSQL(t, ctx, db, "insert into plan_preferences(project_id,plan_version,preference_id,revision) values($1,1,$2,1)", f.project, f.preference)
		lookupTestSQL(t, ctx, db, "insert into team_rules(id,team_id,author) values($1,$2,$3)", f.rule, f.team, f.actor)
		lookupTestSQL(t, ctx, db, "insert into rule_versions(rule_id,revision,body,author,review_at) values($1,1,$2,$3,now()+interval '90 days')", f.rule, ruleBody, f.actor)
		lookupTestSQL(t, ctx, db, "update team_rules set published_revision=1 where id=$1", f.rule)
		lookupTestSQL(t, ctx, db, "insert into plan_rules(project_id,plan_version,rule_id,rule_revision) values($1,1,$2,1)", f.project, f.rule)
		lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(f.token), f.actor)
		return f
	}
	t.Run("scoped rules gate only their step and recorded dependents", func(t *testing.T) {
		f := makeFixture(t)
		run := f.step.Run
		var target string
		var control int
		if err := db.QueryRow(ctx, `select n.agent_id,r.control_revision from runs r
            join nodes n on(n.project_id,n.plan_version)=(r.project_id,r.plan_version)
            where r.id=$1 and n.ordinal=2`, run).Scan(&target, &control); err != nil {
			t.Fatal(err)
		}
		other := uuid.NewString()
		lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", f.actor)
		lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", other, f.actor, f.team)
		lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
            select $1,1,'Independent Agent','独立整理',resource_id,resource_revision,created_by from agent_revisions
            where agent_id=$2 and revision=1`, other, target)
		lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", other, f.actor)
		lookupTestSQL(t, ctx, db, "insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,1,3,$2,1),($1,1,4,$3,1),($1,1,5,$2,1)", f.project, other, target)
		lookupTestSQL(t, ctx, db, "insert into node_edges values($1,1,5,2)", f.project)
		lookupTestSQL(t, ctx, db, "update runs set status='queued',max_calls=5 where id=$1", run)
		lookupTestSQL(t, ctx, db, `update rule_versions set agent_scope=array[$2::uuid],expires_at=now()-interval '1 minute'
            where (rule_id,revision)=($1,1)`, f.rule, target)
		const nextRule = "新版目标规则要求单独核对回执。"
		rulePath := "/api/teams/" + f.team + "/rules"
		skillTestRequest(t, mux, f.token, "POST", rulePath, map[string]any{
			"id": f.rule, "expected": 1, "body": nextRule, "agentScope": []string{uuid.NewString()},
		}, 422)
		// Older rule editors leave the scope untouched when saving a new revision.
		skillTestRequest(t, mux, f.token, "POST", rulePath, map[string]any{"id": f.rule, "expected": 1, "body": nextRule}, 201)
		skillTestRequest(t, mux, f.token, "POST", rulePath+"/"+f.rule+"/publish", map[string]int{"revision": 2}, 204)
		var pins string
		if err := db.QueryRow(ctx, "select planning_rule_pins($1,$2)::text", f.project, f.actor).Scan(&pins); err != nil || pins != "[]" {
			t.Fatalf("planner received an Agent-only rule: %s %v", pins, err)
		}
		advance := func(step int, park bool) {
			t.Helper()
			out, err := worker.ExecuteStep(ctx, Step{run, step})
			if err != nil || out.Wait != park {
				t.Fatalf("step %d did not match its wait: %+v %v", step, out, err)
			}
		}
		advance(2, true)
		var wait, pinned, available int
		err := db.QueryRow(ctx, `select wait_revision,rule_revision,available_revision from step_rule_recovery_choices
            where (run_id,ordinal)=($1,2)`, run).Scan(&wait, &pinned, &available)
		if err != nil || pinned != 1 || available != 2 || len(prompts) != 0 {
			t.Fatalf("expired rule was not held before dispatch: %d %d %v", pinned, available, err)
		}
		release := make(chan struct{})
		close(release)
		releases <- release
		advance(3, false)
		if prompt := <-prompts; strings.Contains(prompt, ruleBody) || strings.Contains(prompt, nextRule) {
			t.Fatalf("scoped rules entered an independent prompt: %s", prompt)
		}
		doRecheck := func(key string, targetRevision, status int) {
			t.Helper()
			body, err := json.Marshal(projects.RuleRecheckInput{Step: 2, Expected: control, WaitRevision: wait, RuleRevision: targetRevision})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/api/runs/"+run+"/rules/recheck", bytes.NewReader(body)).WithContext(ctx)
			req.AddCookie(&http.Cookie{Name: "aa_session", Value: f.token})
			req.Header.Set("Idempotency-Key", key)
			res := httptest.NewRecorder()
			mux.ServeHTTP(res, req)
			if res.Code != status {
				t.Fatalf("recheck %s: status=%d want=%d response=%s", key, res.Code, status, res.Body.String())
			}
		}
		doRecheck("expired-rule", 1, 409)
		doRecheck("restore-step", 2, 202)
		doRecheck("restore-step", 2, 202)
		doRecheck("stale-wait", 2, 409)
		var wakes int
		if err := db.QueryRow(ctx, `select count(*) from controls where run_id=$1 and action='rule.step.recheck'
            and wake_ordinal=2 and revision=$2 and sent_at is null`, run, control).Scan(&wakes); err != nil || wakes != 1 {
			t.Fatalf("rule recheck did not persist exactly one step wake: %d %v", wakes, err)
		}
		releases <- release
		advance(2, false)
		if prompt := <-prompts; !strings.Contains(prompt, nextRule) || strings.Contains(prompt, ruleBody) {
			t.Fatalf("step did not use its approved replacement: %s", prompt)
		}
		var artifact string
		if err := db.QueryRow(ctx, "select id from artifacts where run_id=$1 and ordinal=2", run).Scan(&artifact); err != nil {
			t.Fatal(err)
		}
		// A new branch still has its own pin. Test a paid attempt under the
		// current revision and a separate dependent on the completed draft.
		lookupTestSQL(t, ctx, db, "insert into step_rule_replacements values($1,4,$2,2)", run, f.rule)
		providerReply := make(chan struct{})
		releases <- providerReply
		done := make(chan error, 1)
		go func() {
			_, err := worker.ExecuteStep(ctx, Step{run, 4})
			done <- err
		}()
		select {
		case prompt := <-prompts:
			if !strings.Contains(prompt, nextRule) {
				t.Fatal("in-flight fixture did not use the scoped rule")
			}
		case err := <-done:
			t.Fatalf("in-flight fixture ended early: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		skillTestRequest(t, mux, f.token, "POST", rulePath+"/"+f.rule+"/publish", map[string]any{"revision": 2, "disabled": true}, 204)
		close(providerReply)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		advance(5, true)
		var running, hidden, refused, independentReadable bool
		var rebuilds, charges, globalControl int
		err = db.QueryRow(ctx, `select r.status='running',not distribution_readable($2,1,$3),
            (select result is null from invocations where run_id=r.id and ordinal=4),
            run_working_set_usable(r.id,3,1),
            (select count(*) from step_rule_waits where run_id=r.id and rebuild_required),
            (select count(*) from spend where run_id=r.id),r.control_revision
            from runs r where r.id=$1`, run, artifact, f.actor).
			Scan(&running, &hidden, &refused, &independentReadable, &rebuilds, &charges, &globalControl)
		if err != nil || !running || !hidden || !refused || !independentReadable || rebuilds != 2 || charges != 3 || globalControl != control || len(prompts) != 0 {
			t.Fatalf("scoped withdrawal: running=%v hidden=%v refused=%v independent=%v rebuilds=%d spend=%d control=%d %v",
				running, hidden, refused, independentReadable, rebuilds, charges, globalControl, err)
		}
		skillTestRequest(t, mux, f.token, "POST", rulePath+"/"+f.rule+"/publish", map[string]int{"revision": 2}, 204)
		if err := db.QueryRow(ctx, "select not distribution_readable($1,1,$2)", artifact, f.actor).Scan(&hidden); err != nil || !hidden {
			t.Fatalf("republishing revived a revoked draft: %v %v", hidden, err)
		}
	})
	t.Run("editing a preference discards the in-flight draft but preserves the receipt", func(t *testing.T) {
		f := makeFixture(t)
		release := make(chan struct{})
		releases <- release
		done := make(chan error, 1)
		go func() {
			_, err := worker.ExecuteStep(ctx, f.step)
			done <- err
		}()
		select {
		case body := <-prompts:
			if !strings.Contains(body, private) || !strings.Contains(body, ruleBody) {
				t.Fatalf("pinned memory absent from the provider request: %s", body)
			}
		case err := <-done:
			t.Fatalf("execution ended before reaching the provider: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		detail := skillTestRequest(t, mux, f.token, "GET", "/api/projects/"+f.project, nil, 200)
		var snapshot struct {
			Run struct {
				Attempts []struct {
					WorkingSet *struct {
						Sources, Preferences, Rules, Parents int
						Usable                               bool
					}
				}
			}
		}
		if err := json.Unmarshal(detail, &snapshot); err != nil || len(snapshot.Run.Attempts) != 2 ||
			snapshot.Run.Attempts[1].WorkingSet == nil {
			t.Fatalf("working set not exposed in run detail: %v", err)
		}
		set := snapshot.Run.Attempts[1].WorkingSet
		if !set.Usable || set.Sources != 1 || set.Preferences != 1 || set.Rules != 1 || set.Parents != 1 {
			t.Fatalf("incorrect working set: %+v", set)
		}
		skillTestRequest(t, mux, f.token, "PATCH", "/api/preferences/"+f.preference,
			map[string]any{"expected": 1, "body": "采用更正后的顺序", "enabled": true}, 200)
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		var blocked, absent, invalid bool
		var tokens, charges int
		err := db.QueryRow(ctx, `select r.status='blocked',i.result is null,i.tokens,
            c.invalidated_at is not null,(select count(*) from spend where run_id=r.id)
            from runs r join invocations i on i.run_id=r.id join run_working_sets c using(run_id,ordinal,attempt_no)
            where r.id=$1 and i.ordinal=$2`, f.step.Run, f.step.Ordinal).Scan(&blocked, &absent, &tokens, &invalid, &charges)
		if err != nil || !blocked || !absent || !invalid || tokens != 21 || charges != 1 {
			t.Fatalf("blocked=%t absent=%t invalid=%t tokens=%d charges=%d err=%v", blocked, absent, invalid, tokens, charges, err)
		}
		lookupTestSQL(t, ctx, db, "update plan_preferences set revision=2 where project_id=$1", f.project)
		lookupTestSQL(t, ctx, db, "update runs set status='running' where id=$1", f.step.Run)
		result, err := worker.ExecuteStep(ctx, f.step)
		if err != nil || !result.Wait || len(prompts) != 0 {
			t.Fatalf("restoration accepted the stale receipt: %+v %v", result, err)
		}
	})
	t.Run("a repinned plan cannot consume an invalidated upstream observation", func(t *testing.T) {
		f := makeFixture(t)
		release := make(chan struct{})
		close(release)
		releases <- release
		result, err := worker.ExecuteStep(ctx, f.step)
		if err != nil || result.Wait || len(prompts) != 1 {
			t.Fatalf("first outcome: %+v %v", result, err)
		}
		<-prompts
		lookupTestSQL(t, ctx, db, "update team_rules set disabled=true where id=$1", f.rule)
		lookupTestSQL(t, ctx, db, "delete from plan_rules where project_id=$1 and rule_id=$2", f.project, f.rule)
		lookupTestSQL(t, ctx, db, `insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision,lookup_phase)
            select project_id,plan_version,3,agent_id,agent_revision,'' from nodes where project_id=$1 and ordinal=2`, f.project)
		lookupTestSQL(t, ctx, db, "insert into node_edges(project_id,plan_version,ordinal,depends_on) values($1,1,3,2)", f.project)
		result, err = worker.ExecuteStep(ctx, Step{f.step.Run, 3})
		if err != nil || !result.Wait || len(prompts) != 0 {
			t.Fatalf("downstream used revoked context: %+v %v", result, err)
		}
		var prior string
		if err := db.QueryRow(ctx, "select result from invocations where run_id=$1 and ordinal=2", f.step.Run).Scan(&prior); err != nil ||
			prior != "已核对工作结果" {
			t.Fatalf("the stored business candidate was changed: %q %v", prior, err)
		}
	})
	t.Run("independent applications do not inherit personal preferences", func(t *testing.T) {
		f := makeFixture(t)
		service, app := uuid.NewString(), uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into services(id,team_id,name,owner) values($1,$2,'Memory fixture',$3)", service, f.team, f.actor)
		lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role,kind) values($1,$2,'App','unused','member','application')", app, app+"@example.invalid")
		lookupTestSQL(t, ctx, db, "insert into applications(id,service_id,token_hash,expires_at) values($1,$2,$3,now()+interval '1 hour')", app, service, web.Hash(web.Token()))
		lookupTestSQL(t, ctx, db, "update runs set app_id=$2 where id=$1", f.step.Run, app)
		if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
			text, err := memory.Recall(ctx, tx, f.step.Run)
			if err != nil {
				return err
			}
			if strings.Contains(text, private) || !strings.Contains(text, ruleBody) {
				t.Fatalf("wrong application context: %q", text)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkflowTemplateBuildsPostgres(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("aa", 32))
	if err != nil {
		t.Fatal(err)
	}
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	seed, _ := seedLookupTurn(t, ctx, worker, "https://fixture.invalid")
	var origin, actor, team, agent, model, originalInstructions string
	err = db.QueryRow(ctx, `select p.id,p.owner,p.team_id,n.agent_id,v.resource_id,v.instructions from runs r
 join projects p on p.id=r.project_id join nodes n on (n.project_id,n.plan_version,n.ordinal)=(p.id,r.plan_version,1)
 join agent_revisions v on(v.agent_id,v.revision)=(n.agent_id,n.agent_revision) where r.id=$1`, seed.Run).
		Scan(&origin, &actor, &team, &agent, &model, &originalInstructions)
	if err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, `update members set role='admin' where id=$1`, actor)
	lookupTestSQL(t, ctx, db, `insert into enterprise(name,default_team) values('Fixture',$1) on conflict do nothing`, team)
	lookupTestSQL(t, ctx, db, `update agents set published_revision=1 where id=$1`, agent)
	lookupTestSQL(t, ctx, db, `update nodes set lookup_phase='' where project_id=$1`, origin)
	lookupTestSQL(t, ctx, db, `delete from plan_sources where project_id=$1`, origin)
	lookupTestSQL(t, ctx, db, `update projects set stage='delivered' where id=$1`, origin)
	token := web.Token()
	lookupTestSQL(t, ctx, db, `insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')`, web.Hash(token), actor)
	mux := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(mux, identity.Server{DB: db})
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, "/api/"+path, bytes.NewReader(data)).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", web.Token())
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w.Body.Bytes()
	}
	decode := func(body []byte, dest any) {
		t.Helper()
		if err := json.Unmarshal(body, dest); err != nil {
			t.Fatal(err)
		}
	}
	projectRevision := func(project string) int {
		t.Helper()
		var revision int
		if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	type templateRef struct {
		ID      string
		Version int
	}
	save := func(previous templateRef, instructions string) templateRef {
		t.Helper()
		var result templateRef
		decode(call("POST", "projects/"+origin+"/workflow-templates", map[string]any{
			"replace": previous.ID, "expectedVersion": previous.Version, "expected": projectRevision(origin),
			"name": "核对并复查", "instructions": instructions, "inputs": "本次事实", "outcomes": "核对结果",
		}, 201), &result)
		return result
	}
	first := save(templateRef{}, "先整理事实，再独立核对。")
	path := "workflow-templates/" + first.ID
	view := func(version int) (result struct {
		ManifestDigest string
		Nodes          []struct{ Ready, BuildMatches bool }
	}) {
		t.Helper()
		decode(call("GET", path+"?version="+strconv.Itoa(version), nil, 200), &result)
		return result
	}
	publication := func(version int, action string, want int) {
		t.Helper()
		call("POST", path+"/publication", map[string]any{"version": version, "action": action}, want)
	}
	change := func(instructions string) {
		t.Helper()
		lookupTestSQL(t, ctx, db, "update agent_revisions set instructions=$2 where agent_id=$1 and revision=1", agent, instructions)
	}
	initial := view(1)
	var distinct int
	if err := db.QueryRow(ctx, `select count(*) from workflow_template_builds where(template_id,version)=($1,1)`, first.ID).Scan(&distinct); err != nil ||
		len(initial.ManifestDigest) != 64 || distinct != 1 || len(initial.Nodes) != 2 || !initial.Nodes[1].Ready {
		t.Fatalf("the template should record one build for two steps: %+v, %d, %v", initial, distinct, err)
	}
	publication(1, "request", 204)
	change("定义已变化")
	publication(1, "publish", 409)
	stale := view(1)
	if stale.ManifestDigest != initial.ManifestDigest || stale.Nodes[0].Ready || stale.Nodes[0].BuildMatches {
		t.Fatalf("compile did not preserve its original build: %+v", stale)
	}
	change(originalInstructions)
	publication(1, "publish", 204)
	newProject := func(goal string) string {
		t.Helper()
		var result struct{ ID string }
		decode(call("POST", path+"/projects", map[string]any{"version": 1, "goal": goal}, 201), &result)
		return result.ID
	}
	planInput := func(project, selected string) projects.PlanInput {
		return projects.PlanInput{Expected: projectRevision(project), Brief: "对照事实并复核",
			Agents: []string{selected, selected}, AgentRevisions: []int{1, 1}}
	}
	savePlan := func(project, selected string, want int) {
		t.Helper()
		call("POST", "projects/"+project+"/plan", planInput(project, selected), want)
	}
	project := newProject("用原模板核对")
	savePlan(project, agent, 204)
	const updatedInstructions = "新版核对要求"
	change(updatedInstructions)
	second := save(first, "增加一轮人工审阅。")
	if second.Version != 2 || view(2).ManifestDigest == initial.ManifestDigest {
		t.Fatalf("a new workflow definition must have a new manifest: %+v", second)
	}
	var comparison struct{ ChangedSteps []int }
	decode(call("GET", "projects/"+project+"/workflow-template-update", nil, 200), &comparison)
	if len(comparison.ChangedSteps) != 2 || comparison.ChangedSteps[0] != 1 || comparison.ChangedSteps[1] != 2 {
		t.Fatalf("build changes were hidden in the version comparison: %+v", comparison)
	}
	call("POST", "projects/"+project+"/workflow-template-update", map[string]any{
		"fromVersion": 1, "toVersion": 2, "plan": planInput(project, agent),
	}, 200)
	var before, after int
	if err := db.QueryRow(ctx, `select min(template_version),max(template_version) from plan_workflow_templates where project_id=$1`, project).
		Scan(&before, &after); err != nil || before != 1 || after != 2 {
		t.Fatalf("existing plans should retain the template used by each plan: %d %d %v", before, after, err)
	}
	change("发布后资源定义变化")
	savePlan(project, agent, 409)
	start := func(want int) []byte {
		t.Helper()
		return call("POST", "projects/"+project+"/runs", map[string]any{"expected": projectRevision(project), "authorizeCalls": 2}, want)
	}
	start(409)
	change(updatedInstructions)
	var run struct{ ID string }
	decode(start(202), &run)
	change("派发之前构建又变化")
	result, err := worker.ExecuteStep(ctx, Step{run.ID, 1})
	if err != nil || !result.Wait {
		t.Fatalf("runtime did not wait on the frozen build: %+v, %v", result, err)
	}
	var status string
	var buildBlocked bool
	var spends int
	if err := db.QueryRow(ctx, `select r.status,exists(select from trace_events where run_id=r.id and code='template_build_changed'),
 (select count(*) from spend where run_id=r.id) from runs r where r.id=$1`, run.ID).Scan(&status, &buildBlocked, &spends); err != nil ||
		status != "blocked" || !buildBlocked || spends != 0 {
		t.Fatalf("runtime accepted a drifted build: %s, %t, %d, %v", status, buildBlocked, spends, err)
	}
	replacement := uuid.NewString()
	lookupTestSQL(t, ctx, db, `insert into agents(id,team_id,owner) values($1,$2,$3)`, replacement, team, actor)
	lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
 values($1,1,'现行核对能力','核对事实',$2,1,$3)`, replacement, model, actor)
	lookupTestSQL(t, ctx, db, `insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')`, replacement, actor)
	lookupTestSQL(t, ctx, db, `update agents set published_revision=1 where id=$1`, replacement)
	savePlan(newProject("显式替换不再适用的步骤"), replacement, 204)

	tool := func(group string) string {
		t.Helper()
		var id string
		err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
			var err error
			id, _, err = worker.Catalog.SaveToolDraftTx(ctx, tx, actor, resources.ToolDraft{
				Name: "按" + group + "汇总", Description: "汇总 CSV 中的金额", Team: team,
				Tool: resources.Tool{Operation: "aggregate", Config: map[string]string{"group": group, "amount": "金额"}},
			})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		lookupTestSQL(t, ctx, db, `insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')`, id, actor)
		lookupTestSQL(t, ctx, db, `update agents set published_revision=1 where id=$1`, id)
		return id
	}
	primary, alternative := tool("地区"), tool("部门")
	lookupTestSQL(t, ctx, db, `update nodes set agent_id=$2 where project_id=$1 and ordinal=2`, origin, primary)
	lookupTestSQL(t, ctx, db, `insert into plan_tool_choices values($1,1,2,1,$2,1)`, origin, alternative)
	withChoice := save(templateRef{}, "根据表头选择汇总口径。")
	var alternativeBound bool
	if err := db.QueryRow(ctx, `select exists(select from workflow_template_builds
 where (template_id,version,agent_id,agent_revision)=($1,1,$2,1))`, withChoice.ID, alternative).Scan(&alternativeBound); err != nil || !alternativeBound {
		t.Fatalf("missing the unsent alternative tool's build: %t, %v", alternativeBound, err)
	}
	toolPath := "workflow-templates/" + withChoice.ID + "/publication"
	call("POST", toolPath, map[string]any{"version": 1, "action": "request"}, 204)
	lookupTestSQL(t, ctx, db, `update agent_revisions set instructions='不同的汇总约定' where agent_id=$1 and revision=1`, alternative)
	call("POST", toolPath, map[string]any{"version": 1, "action": "publish"}, 409)
}

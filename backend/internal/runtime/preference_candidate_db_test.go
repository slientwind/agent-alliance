package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPreferenceCandidateRecoveryPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("c4", 32))
	if err != nil {
		t.Fatal(err)
	}
	const private, oldBody, goodBody = "请用私有的甲列标记", "按甲列标记的旧候选", "本次核对已完成"
	requests := make(chan string, 10)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []resources.Message }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "bad fixture request", 400)
			return
		}
		var prompt strings.Builder
		for _, m := range input.Messages {
			prompt.WriteString(m.Content)
		}
		text := prompt.String()
		requests <- text
		reply := goodBody
		if strings.Contains(text, private) || strings.Contains(text, oldBody) {
			reply = oldBody
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": reply}}},
			"usage":   map[string]int{"total_tokens": 23},
		})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	t.Setenv("OBJECT_ROOT", t.TempDir())
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	seed, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	run := seed.Run
	var project, actor, team, generic string
	if err := db.QueryRow(ctx, `select r.project_id,r.actor,r.charge_team,n.agent_id from runs r
      join nodes n on (n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,1) where r.id=$1`, run).
		Scan(&project, &actor, &team, &generic); err != nil {
		t.Fatal(err)
	}
	target := uuid.NewString()
	lookupTestSQL(t, ctx, db, "delete from artifacts where run_id=$1", run)
	lookupTestSQL(t, ctx, db, "delete from invocations where run_id=$1", run)
	lookupTestSQL(t, ctx, db, "delete from node_edges where project_id=$1", project)
	lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", target, actor, team)
	lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
      select $1,1,'限定偏好的 Agent','完成合同甲项',resource_id,resource_revision,created_by from agent_revisions where (agent_id,revision)=($2,1)`, target, generic)
	lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", target, actor)
	lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", target)
	lookupTestSQL(t, ctx, db, "update nodes set lookup_phase='',agent_id=case when ordinal=2 then $2 else agent_id end where project_id=$1", project, target)
	lookupTestSQL(t, ctx, db, "insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,1,3,$2,1),($1,1,4,$2,1)", project, generic)
	lookupTestSQL(t, ctx, db, "insert into node_edges values($1,1,3,1),($1,1,3,2)", project)
	lookupTestSQL(t, ctx, db, "update runs set max_calls=4 where id=$1", run)
	preference := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into preferences(id,member_id,body,agent_scope) values($1,$2,$3,$4)", preference, actor, private, target)
	lookupTestSQL(t, ctx, db, "insert into plan_scoped_preferences values($1,1,$2,1,$3)", project, preference, target)
	advance := func(id string, ordinal int) string {
		t.Helper()
		result, err := worker.AdvanceTextStep(ctx, Step{id, ordinal})
		if err == nil && result.TextReply != nil {
			result, err = worker.CommitTextReply(ctx, *result.TextReply)
		}
		if err != nil || result.Wait || result.Stop {
			t.Fatalf("execute %d: %+v %v", ordinal, result, err)
		}
		select {
		case text := <-requests:
			return text
		default:
			t.Fatalf("no provider call for step %d", ordinal)
			return ""
		}
	}
	for _, ordinal := range []int{1, 2, 3, 4} {
		prompt := advance(run, ordinal)
		if strings.Contains(prompt, private) != (ordinal == 2) || strings.Contains(prompt, oldBody) != (ordinal == 3) {
			t.Fatalf("original prompt for step %d: %s", ordinal, prompt)
		}
	}
	rows, err := db.Query(ctx, "select id from artifacts where run_id=$1 order by ordinal", run)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(artifacts) != 4 {
		t.Fatalf("original artifacts: %v %v", artifacts, err)
	}
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		hash, _, err := knowledge.ConfiguredObjects().Put(ctx, tx, strings.NewReader(oldBody), 4096)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into artifact_files values($1,1,$2,'candidate.txt','text/plain')", artifacts[2], hash)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	routes := http.NewServeMux()
	access := identity.Server{DB: db}
	projects.Server{DB: db, Vault: vault}.Routes(routes, access)
	knowledge.Server{DB: db, Objects: knowledge.ConfiguredObjects()}.Routes(routes, access)
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
	root := "/api/projects/" + project
	candidate := func(i int) string { return root + "/artifacts/" + artifacts[i-1] }
	request := func(method, url string, body any, want int) []byte {
		t.Helper()
		return skillTestRequest(t, routes, token, method, url, body, want)
	}
	request("POST", candidate(4)+"/review", map[string]int{"version": 1}, 204)
	request("POST", candidate(1)+"/revise", map[string]any{"version": 1, "body": "独立人工修订的引用", "reason": "核对来源"}, 204)
	if before := request("GET", candidate(3)+"/file?version=2", nil, 200); !bytes.Equal(before, []byte(oldBody)) {
		t.Fatalf("expected automatic copy of the downstream file: %q", before)
	}

	api := httptest.NewServer(routes)
	defer api.Close()
	stream, stop := openDraftResponse(t, ctx, api.URL+root+"/session-events", token, "")
	defer stop()
	readSnapshot := func() string {
		return readDraftFrame(t, stream, func(frame string) bool { return strings.Contains(frame, "event: session.snapshot") })
	}
	before := readSnapshot()
	lookupTestSQL(t, ctx, db, "update preferences set expires_at=now()-interval '1 second' where id=$1", preference)
	if after := readSnapshot(); after == before || strings.Contains(after, oldBody) {
		t.Fatalf("expiry did not invalidate the project session scope: %q", after)
	}
	var projectView struct {
		Artifacts []struct {
			Ordinal          int
			Body, DraftBlock string
			Approved         bool
		}
	}
	detail := request("GET", root, nil, 200)
	if err := json.Unmarshal(detail, &projectView); err != nil || len(projectView.Artifacts) != 4 || bytes.Contains(detail, []byte(oldBody)) {
		t.Fatalf("affected and valid candidates not separated: %+v %v", projectView, err)
	}
	for _, a := range projectView.Artifacts {
		if (a.DraftBlock == "preference") != (a.Ordinal == 2 || a.Ordinal == 3) ||
			a.Ordinal == 4 && (!a.Approved || a.Body != goodBody) {
			t.Fatalf("wrong artifact access: %+v", a)
		}
	}
	request("GET", candidate(3)+"/file?version=2", nil, 404)
	request("GET", candidate(3)+"/versions?version=1", nil, 404)
	request("GET", candidate(3)+"/versions?version=2", nil, 404)
	request("GET", candidate(4)+"/versions?version=1", nil, 200)
	request("POST", candidate(2)+"/review", map[string]int{"version": 1}, 409)
	request("POST", candidate(3)+"/revise", map[string]any{"version": 2, "body": "尝试从旧候选修订", "reason": "核对"}, 409)
	if _, err := db.Exec(ctx, "insert into reviews(artifact_id,version,reviewer) values($1,2,$2)", artifacts[2], actor); err == nil ||
		!strings.Contains(err.Error(), "review_candidate_preference_unavailable") {
		t.Fatalf("obsolete copy escaped the database review gate: %v", err)
	}

	path := "/api/runs/" + run + "/rules/rebuild"
	var preview projects.RuleBranchPreview
	if err := json.Unmarshal(request("GET", path, nil, 200), &preview); err != nil || !preview.SkipUnavailablePreferences || preview.Calls != 2 ||
		len(preview.Steps) != 4 || preview.Steps[0].Mode != "reuse" || preview.Steps[1].Mode != "rebuild" ||
		preview.Steps[2].Mode != "rebuild" || preview.Steps[3].Mode != "reuse" {
		t.Fatalf("wrong rebuild scope: %+v %v", preview, err)
	}
	confirm := func() string {
		t.Helper()
		body, _ := json.Marshal(projects.RuleBranchInput{Digest: preview.Digest, AuthorizeCalls: preview.Calls})
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", "preference-recovery-test")
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, req)
		var admitted struct{ Run string }
		if response.Code != 202 || json.Unmarshal(response.Body.Bytes(), &admitted) != nil || admitted.Run == "" {
			t.Fatalf("recovery admission: %d %s", response.Code, response.Body)
		}
		return admitted.Run
	}
	next := confirm()
	if confirm() != next {
		t.Fatal("repeated authorization created a second batch")
	}
	for _, ordinal := range []int{2, 3} {
		prompt := advance(next, ordinal)
		if strings.Contains(prompt, private) || strings.Contains(prompt, oldBody) {
			t.Fatalf("recovered step %d used unavailable content: %s", ordinal, prompt)
		}
	}
	for _, item := range []struct{ step, version int }{{2, 2}, {3, 3}} {
		request("POST", candidate(item.step)+"/review", map[string]int{"version": item.version}, 204)
	}
	request("GET", candidate(3)+"/versions?version=2", nil, 404)
	request("GET", candidate(4)+"/versions?version=1", nil, 200)
	var correct bool
	if err := db.QueryRow(ctx, `select r.status='completed' and r.skip_unavailable_preferences and
      (select count(*) from spend where run_id=$1)=4 and (select count(*) from spend where run_id=$2)=2 and
      (select array_agg(ordinal order by ordinal) from invocations where run_id=$2)=array[2,3]
      from runs r where r.id=$2`, run, next).Scan(&correct); err != nil || !correct {
		t.Fatalf("bad recovery receipts: ok=%t err=%v", correct, err)
	}
}

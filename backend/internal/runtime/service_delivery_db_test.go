package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/services"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublishedServiceKeepsDeliveryRequirementsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use an isolated database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a3", 32))
	if err != nil {
		t.Fatal(err)
	}
	prompts := make(chan string, 8)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Messages []resources.Message }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Messages) < 2 {
			http.Error(w, "missing fixture prompt", http.StatusBadRequest)
			return
		}
		prompts <- in.Messages[1].Content
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "请审核本次工作候选。"}}}})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	seed, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var actor, team, agent string
	if err := db.QueryRow(ctx, `select r.actor,r.charge_team,n.agent_id from runs r
 join nodes n on(n.project_id,n.plan_version)=(r.project_id,r.plan_version) and n.ordinal=1 where r.id=$1`, seed.Run).Scan(&actor, &team, &agent); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", actor)
	lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", agent)
	const privateBrief = "仅源项目可见的邮件编号 AA-153"
	source := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'来源项目')", source, actor, team)
	lookupTestSQL(t, ctx, db, "insert into project_memberships values($1,$2,true) on conflict do nothing", source, actor)
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(token), actor)
	mux := http.NewServeMux()
	auth := identity.Server{DB: db}
	projects.Server{DB: db}.Routes(mux, auth)
	services.Server{DB: db}.Routes(mux, auth)
	do := func(method, path, bearer, idem string, body any, status int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
		if bearer == "" {
			req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		} else {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: %d != %d, %s", method, path, res.Code, status, res.Body)
		}
		return res.Body.Bytes()
	}
	member := func(method, path string, body any, status int) []byte {
		t.Helper()
		return do(method, path, "", "", body, status)
	}
	decode := func(raw []byte, out any) {
		t.Helper()
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("invalid snapshot: %s (%v)", raw, err)
		}
	}
	const first, second = "甲报价总价", "乙报价有效期"
	path := "/api/projects/" + source
	member("POST", path+"/plan", projects.PlanInput{
		Expected: 1, Brief: privateBrief, Agents: []string{agent, agent}, AgentRevisions: []int{1, 1},
		DeliveryContract: &projects.DeliveryContract{AllowPartial: true, Items: []projects.DeliveryRequirement{{Step: 1, Label: first}, {Step: 2, Label: second}}},
	}, 204)
	var plan struct{ Revision, PlanVersion int }
	decode(member("GET", path, nil, 200), &plan)
	do("POST", path+"/runs", "", uuid.NewString(), map[string]int{"expected": plan.Revision, "authorizeCalls": 2}, 202)
	preview := member("GET", "/api/service-publications/projects/"+source, nil, 200)
	if bytes.Contains(preview, []byte(privateBrief)) || !bytes.Contains(preview, []byte(first)) {
		t.Fatalf("bad public preview %s", preview)
	}
	publish := map[string]any{"name": "报价核对", "instructions": "依本次资料逐项核对", "project": source}
	member("POST", "/api/services", publish, 409)
	publish["expectedPlanVersion"] = plan.PlanVersion
	member("POST", "/api/services", publish, 422)
	publish["confirmDeliveryContract"] = true
	var service struct{ ID string }
	decode(member("POST", "/api/services", publish, 201), &service)
	servicePath := "/api/services/" + service.ID
	type contract struct {
		DeliveryContract services.ServiceDeliveryContract
	}
	var registered contract
	entry := member("GET", servicePath+"/entry", nil, 200)
	decode(entry, &registered)
	if bytes.Contains(entry, []byte(privateBrief)) || !registered.DeliveryContract.AllowsPartial ||
		len(registered.DeliveryContract.Items) != 2 || registered.DeliveryContract.Items[1].Label != second {
		t.Fatalf("wrong published requirements: %s", entry)
	}
	var summary []contract
	decode(member("GET", "/api/services", nil, 200), &summary)
	if len(summary) != 1 || len(summary[0].DeliveryContract.Items) != 2 {
		t.Fatalf("publisher cannot see the prior contract: %+v", summary)
	}
	_, err = db.Exec(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,service_id,service_version)
 select r.project_id,r.plan_version,r.actor,r.charge_team,$2,'old-api',2,$3,1 from runs r where r.id=$1`, seed.Run, uuid.NewString(), service.ID)
	var constraint *pgconn.PgError
	if !errors.As(err, &constraint) || constraint.ConstraintName != "run_service_delivery_contract" {
		t.Fatalf("an old API could accept the service without its checklist: %v", err)
	}
	var app struct{ Token string }
	decode(member("POST", servicePath+"/applications", map[string]any{"name": "采购应用", "days": 1}, 201), &app)
	v1 := "/api/v1/services/" + service.ID
	var described contract
	decode(do("GET", v1, app.Token, "", nil, 200), &described)
	if len(described.DeliveryContract.Items) != 2 || !described.DeliveryContract.AllowsPartial {
		t.Fatalf("signed description lost requirements: %+v", described)
	}

	type receipt struct{ ID, Project string }
	type coverageView struct {
		Revision         int
		NextAction       string
		DeliveryCoverage struct {
			AllowPartial bool
			Items        []struct {
				Item, Step, Version         int
				Label, State, Run, Artifact string
			}
		}
	}
	runInput := map[string]any{"input": "本次请逐项检查", "version": 1, "authorizeCalls": 2}
	var strict, optional receipt
	key := uuid.NewString()
	decode(do("POST", servicePath+"/chat", "", key, runInput, 202), &strict)
	runInput["allowPartialDelivery"] = true
	do("POST", servicePath+"/chat", "", key, runInput, 409)
	decode(do("POST", v1+"/runs", app.Token, uuid.NewString(), runInput, 202), &optional)

	for _, item := range []struct {
		run     receipt
		partial bool
	}{{strict, false}, {optional, true}} {
		projectPath := "/api/projects/" + item.run.Project
		view := func() coverageView {
			t.Helper()
			var result coverageView
			decode(member("GET", projectPath, nil, 200), &result)
			return result
		}
		for step, label := range []string{first, second} {
			done, err := worker.ExecuteStep(ctx, Step{item.run.ID, step + 1})
			if err != nil || done.Wait {
				t.Fatalf("service execution did not complete: %+v %v", done, err)
			}
			prompt := <-prompts
			if !strings.Contains(prompt, label) || strings.Contains(prompt, privateBrief) {
				t.Fatalf("wrong authorized work input: %s", prompt)
			}
		}
		snap := view()
		if snap.DeliveryCoverage.AllowPartial != item.partial || len(snap.DeliveryCoverage.Items) != 2 {
			t.Fatalf("service lost its fixed inputs or opt-in: %+v", snap)
		}
		for index, coverage := range snap.DeliveryCoverage.Items {
			assessment := map[string]any{"expected": view().Revision, "item": coverage.Item, "run": coverage.Run,
				"artifact": coverage.Artifact, "version": coverage.Version, "state": "covered", "reason": "核对当前候选和原件"}
			if index == 1 {
				assessment["state"], assessment["reason"] = "missing", "乙方缺有效期"
				assessment["responsible"], assessment["continueWhen"] = actor, "乙方书面确认后继续"
			}
			member("POST", projectPath+"/delivery/assessments", assessment, 204)
			if index == 0 {
				member("POST", projectPath+"/artifacts/"+coverage.Artifact+"/review", map[string]int{"version": coverage.Version}, 204)
			}
		}
		member("POST", projectPath+"/deliver", map[string]any{"expected": view().Revision}, 409)
		if item.partial {
			var status coverageView
			raw := do("GET", "/api/v1/runs/"+item.run.ID, app.Token, "", nil, 200)
			decode(raw, &status)
			if !status.DeliveryCoverage.AllowPartial || len(status.DeliveryCoverage.Items) != 2 ||
				status.DeliveryCoverage.Items[1].State != "missing" || status.NextAction != "confirm_delivery" ||
				bytes.Contains(raw, []byte("乙方书面确认后继续")) {
				t.Fatalf("wrong signed status: %s", raw)
			}
			member("POST", projectPath+"/deliver", map[string]any{"expected": view().Revision, "partial": true}, 201)
			manifest := do("GET", "/api/v1/runs/"+item.run.ID+"/delivery", app.Token, "", nil, 200)
			if !bytes.Contains(manifest, []byte(first)) || !bytes.Contains(manifest, []byte(second)) {
				t.Fatalf("partial receipt lost requirements: %s", manifest)
			}
		} else {
			member("POST", projectPath+"/deliver", map[string]any{"expected": view().Revision, "partial": true}, 409)
		}
	}

	next := map[string]any{"id": service.ID, "expected": 1, "expectedRoutingRevision": 1,
		"name": "快速摘要", "instructions": "摘要", "agent": agent}
	member("POST", "/api/services", next, 422)
	next["confirmDeliveryContract"] = true
	member("POST", "/api/services", next, 201)
	var comparison struct{ Current, Target contract }
	decode(member("GET", servicePath+"/versions/1/preflight", nil, 200), &comparison)
	if len(comparison.Current.DeliveryContract.Items) != 0 || len(comparison.Target.DeliveryContract.Items) != 2 {
		t.Fatalf("version comparison lost established policy: %+v", comparison)
	}
	runInput["version"], runInput["authorizeCalls"] = 2, 1
	do("POST", v1+"/runs", app.Token, uuid.NewString(), runInput, 422)
	runInput["version"], runInput["authorizeCalls"] = 1, 2
	var historical receipt
	decode(do("POST", v1+"/runs", app.Token, uuid.NewString(), runInput, 202), &historical)
	var historicalStatus coverageView
	decode(do("GET", "/api/v1/runs/"+historical.ID, app.Token, "", nil, 200), &historicalStatus)
	if !historicalStatus.DeliveryCoverage.AllowPartial || len(historicalStatus.DeliveryCoverage.Items) != 2 {
		t.Fatalf("historical invocation lost its requirements: %+v", historicalStatus)
	}
	do("POST", servicePath+"/rollback", "", uuid.NewString(), map[string]any{
		"expected": 2, "expectedRoutingRevision": 2, "target": 1, "reason": "恢复原交付约定",
	}, 200)
	var restored contract
	decode(member("GET", servicePath+"/entry", nil, 200), &restored)
	if len(restored.DeliveryContract.Items) != 2 {
		t.Fatalf("rollback lost pinned items: %+v", restored)
	}
}

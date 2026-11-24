package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"archive/zip"
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
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeliveryCoveragePostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch PostgreSQL database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
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
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Messages []resources.Message }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) < 2 {
			http.Error(w, "missing fixture prompt", http.StatusBadRequest)
			return
		}
		prompts <- req.Messages[1].Content
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已生成候选。"},"finish_reason":"stop"}],"usage":{"total_tokens":18}}`)
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	t.Setenv("OBJECT_ROOT", t.TempDir())
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	routes := http.NewServeMux()
	auth := identity.Server{DB: db}
	projects.Server{DB: db}.Routes(routes, auth)
	knowledge.Server{DB: db, Objects: knowledge.ConfiguredObjects()}.Routes(routes, auth)
	step, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var owner, team, agent string
	if err := db.QueryRow(ctx, `select r.actor,r.charge_team,n.agent_id from runs r
       join nodes n on(n.project_id,n.plan_version)=(r.project_id,r.plan_version) and n.ordinal=1 where r.id=$1`,
		step.Run).Scan(&owner, &team, &agent); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", agent)
	final, assignee, outsider := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{final, assignee, outsider} {
		lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'项目成员','unused','member')", id, id+"@example.invalid")
	}
	session := func(id string) string {
		token := web.Token()
		lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), id)
		return token
	}
	ownerToken, finalToken, assigneeToken, outsiderToken := session(owner), session(final), session(assignee), session(outsider)

	for _, partial := range []bool{false, true} {
		name := "default waits until all required items are covered"
		if partial {
			name = "partial seals accepted versions and the continuation"
		}
		t.Run(name, func(t *testing.T) {
			project := uuid.NewString()
			path := "/api/projects/" + project
			lookupTestSQL(t, ctx, db, "insert into projects(id,owner,final_reviewer,team_id,title) values($1,$2,$3,$4,'报价比较')", project, owner, final, team)
			lookupTestSQL(t, ctx, db, `insert into project_memberships(project_id,member_id,can_review)
                  values($1,$2,true),($1,$3,true),($1,$4,false) on conflict do nothing`, project, owner, final, assignee)
			lookupTestSQL(t, ctx, db, "insert into project_budgets(project_id) values($1) on conflict do nothing", project)
			call := func(token, method, more string, input any, want int) []byte {
				t.Helper()
				return skillTestRequest(t, routes, token, method, path+more, input, want)
			}
			view := func(token string) (result struct {
				Revision int
				Stage    string
				Coverage struct {
					AllowPartial bool
					Items        []struct {
						Item, Version                                     int
						Label, Run, Artifact, State, Reason, ContinueWhen string
					}
				} `json:"deliveryCoverage"`
			}) {
				t.Helper()
				if raw := call(token, "GET", "", nil, 200); json.Unmarshal(raw, &result) != nil {
					t.Fatalf("invalid project snapshot: %s", raw)
				}
				return result
			}
			plan := projects.PlanInput{Expected: 1, Brief: "核对两家报价", Agents: []string{agent, agent}, AgentRevisions: []int{1, 1},
				DeliveryContract: &projects.DeliveryContract{AllowPartial: partial, Items: []projects.DeliveryRequirement{
					{Step: 1, Label: "甲报价总价"}, {Step: 2, Label: "乙报价有效期"},
				}}}
			call(ownerToken, "POST", "/plan", plan, 204)
			saved := view(ownerToken)
			if saved.Coverage.AllowPartial != partial || len(saved.Coverage.Items) != 2 {
				t.Fatalf("plan lost requirements: %+v", saved.Coverage)
			}
			plan.Expected, plan.DeliveryContract = saved.Revision, nil
			call(ownerToken, "POST", "/plan", plan, 409)

			raw, err := json.Marshal(map[string]int{"expected": saved.Revision, "authorizeCalls": 2})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", path+"/runs", bytes.NewReader(raw))
			req.AddCookie(&http.Cookie{Name: "aa_session", Value: ownerToken})
			req.Header.Set("Idempotency-Key", uuid.NewString())
			res := httptest.NewRecorder()
			routes.ServeHTTP(res, req)
			var started struct{ ID string }
			if err := json.Unmarshal(res.Body.Bytes(), &started); err != nil || res.Code != 202 || started.ID == "" {
				t.Fatalf("start: %d %s %v", res.Code, res.Body, err)
			}
			for i, requirement := range saved.Coverage.Items {
				outcome, err := worker.ExecuteStep(ctx, Step{Run: started.ID, Ordinal: i + 1})
				if err != nil || outcome.Wait {
					t.Fatalf("step %d: %+v, %v", i+1, outcome, err)
				}
				select {
				case prompt := <-prompts:
					if !strings.Contains(prompt, requirement.Label) {
						t.Fatalf("step %d did not receive its requirement: %s", i+1, prompt)
					}
				default:
					t.Fatal("no prompt received")
				}
			}
			projectView := view(ownerToken)
			if projectView.Stage != "review" || projectView.Coverage.Items[0].Artifact == "" || projectView.Coverage.Items[1].Artifact == "" {
				t.Fatalf("candidates not visible: %+v", projectView)
			}
			finalize := func(token string, allow bool, want int) []byte {
				return call(token, "POST", "/deliver", map[string]any{"expected": view(ownerToken).Revision, "partial": allow}, want)
			}
			assess := func(token string, index int, state, person string, want int) {
				current := view(ownerToken)
				item := current.Coverage.Items[index]
				body := map[string]any{"expected": current.Revision, "item": item.Item, "run": item.Run, "artifact": item.Artifact,
					"version": item.Version, "state": state, "reason": "已核对原件"}
				if state == "missing" {
					body["reason"] = "乙报价尚未提供有效期"
				}
				if person != "" {
					body["responsible"], body["continueWhen"] = person, "取得供应商确认的有效期"
				}
				call(token, "POST", "/delivery/assessments", body, want)
			}
			approve := func(index int) {
				item := view(ownerToken).Coverage.Items[index]
				call(ownerToken, "POST", "/artifacts/"+item.Artifact+"/review", map[string]int{"version": item.Version}, 204)
			}
			finalize(finalToken, partial, 409)
			assess(assigneeToken, 0, "covered", "", 403)
			assess(ownerToken, 0, "covered", "", 204)
			assess(ownerToken, 1, "missing", "", 204)
			approve(0)
			finalize(finalToken, false, 409)
			finalize(finalToken, true, 409)
			inbox := func() []byte {
				return skillTestRequest(t, routes, finalToken, "GET", "/api/projects/review-inbox?project="+project, nil, 200)
			}
			if pending := inbox(); !bytes.Contains(pending, []byte(`"kind":"coverage"`)) || bytes.Contains(pending, []byte(`"kind":"deliver"`)) {
				t.Fatalf("inbox skipped the coverage decision: %s", pending)
			}

			if partial {
				assess(ownerToken, 1, "missing", outsider, 422)
				assess(ownerToken, 1, "missing", assignee, 204)
				finalize(ownerToken, true, 403)
				lookupTestSQL(t, ctx, db, "delete from project_memberships where project_id=$1 and member_id=$2", project, assignee)
				finalize(finalToken, true, 409)
				assess(ownerToken, 1, "missing", owner, 204)
				if ready := inbox(); !bytes.Contains(ready, []byte(`"kind":"deliver"`)) || bytes.Contains(ready, []byte(`"kind":"coverage"`)) {
					t.Fatalf("approved partial delivery did not reach final review: %s", ready)
				}
			} else {
				item := view(ownerToken).Coverage.Items[1]
				call(ownerToken, "POST", "/artifacts/"+item.Artifact+"/revise", map[string]any{
					"version": item.Version, "body": "乙报价有效期至 12 月 1 日", "reason": "补齐报价有效期",
				}, 204)
				current := view(ownerToken)
				if current.Coverage.Items[1].Version != 2 || current.Coverage.Items[1].State != "unchecked" {
					t.Fatalf("prior decision survived revision: %+v", current.Coverage.Items[1])
				}
				call(ownerToken, "POST", "/delivery/assessments", map[string]any{"expected": current.Revision,
					"item": item.Item, "run": item.Run, "artifact": item.Artifact, "version": item.Version,
					"state": "covered", "reason": "旧页面核对"}, 409)
				assess(ownerToken, 1, "covered", "", 204)
				approve(1)
			}
			finalize(finalToken, partial, 201)
			finalize(finalToken, partial, 409)
			call(outsiderToken, "GET", "/delivery/receipt", nil, 404)
			var receipt struct {
				IsPartial bool
				Items     []json.RawMessage
				Coverage  json.RawMessage
			}
			sealed := call(finalToken, "GET", "/delivery", nil, 200)
			expectedCount := 2
			if partial {
				expectedCount = 1
			}
			if err := json.Unmarshal(sealed, &receipt); err != nil || receipt.IsPartial != partial || len(receipt.Items) != expectedCount ||
				partial && (!bytes.Contains(receipt.Coverage, []byte("取得供应商确认的有效期")) || bytes.Contains(receipt.Items[0], []byte(projectView.Coverage.Items[1].Artifact))) {
				t.Fatalf("wrong sealed coverage or artifacts: %s, %v", sealed, err)
			}
			archived := call(finalToken, "GET", "/delivery/package", nil, 200)
			pack, err := zip.NewReader(bytes.NewReader(archived), int64(len(archived)))
			if err != nil {
				t.Fatal(err)
			}
			artifactCount := 0
			for _, file := range pack.File {
				if strings.HasPrefix(file.Name, "artifacts/") {
					artifactCount++
				}
			}
			if artifactCount != expectedCount || partial && !bytes.Contains(sealed, []byte(owner)) {
				t.Fatalf("archive has %d items and receipt %s", artifactCount, sealed)
			}
			if partial {
				current := view(ownerToken)
				missing := current.Coverage.Items[1]
				var followup struct{ Feedback string }
				raw := call(ownerToken, "POST", "/delivery/revisions", map[string]any{
					"expected": current.Revision, "edition": 1, "artifact": missing.Artifact, "version": missing.Version, "reason": "已收到乙方有效期",
				}, 201)
				if err := json.Unmarshal(raw, &followup); err != nil || followup.Feedback == "" {
					t.Fatalf("could not reopen the missing item: %s, %v", raw, err)
				}
				artifact := "/artifacts/" + missing.Artifact
				call(ownerToken, "POST", artifact+"/revise", map[string]any{"version": missing.Version, "body": "乙报价有效期至 12 月 1 日", "reason": "补齐有效期"}, 204)
				feedback := artifact + "/feedback/" + followup.Feedback
				for i, action := range []string{"submit", "resolve"} {
					call(ownerToken, "POST", feedback, map[string]any{"expected": i + 1, "version": missing.Version + 1,
						"action": action, "reason": "核对供应商原件"}, 200)
				}
				assess(ownerToken, 1, "covered", "", 204)
				approve(1)
				finalize(finalToken, false, 201)
				old := call(finalToken, "GET", "/delivery?edition=1", nil, 200)
				newEdition := call(finalToken, "GET", "/delivery?edition=2", nil, 200)
				var updated struct {
					IsPartial bool
					Items     []struct {
						Artifact string
						Version  int
					}
				}
				if err := json.Unmarshal(newEdition, &updated); err != nil || !bytes.Equal(old, sealed) || updated.IsPartial ||
					len(updated.Items) != 2 || updated.Items[1].Artifact != missing.Artifact || updated.Items[1].Version != missing.Version+1 {
					t.Fatalf("reopened delivery changed the prior seal or failed to include the new item: %s", newEdition)
				}
			}
		})
	}
}

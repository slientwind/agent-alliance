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
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentScopedPreferencesPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("df", 32))
	if err != nil {
		t.Fatal(err)
	}
	const private = "我个人偏好用甲栏标记待复核项。"
	requests := make(chan []resources.Message, 12)
	var pickPrimary atomic.Bool
	var revokeOnCall atomic.Pointer[string]
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Messages) < 2 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		if id := revokeOnCall.Load(); id != nil && strings.Contains(in.Messages[0].Content, "执行备选") {
			if _, err := db.Exec(r.Context(), "update preferences set body='新修订',revision=revision+1 where id=$1", *id); err != nil {
				http.Error(w, "fixture update failed", 500)
				return
			}
		}
		requests <- in.Messages
		answer := "完成合同核对。"
		if strings.Contains(in.Messages[0].Content, harness.AgentChoiceInstructions) {
			answer = `{"choice":2}`
			if pickPrimary.Load() {
				answer = `{"choice":1}`
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": answer}}},
			"usage":   map[string]int{"total_tokens": 12},
		})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	a := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	seed, _ := seedLookupTurn(t, ctx, a, provider.URL)
	var actor, team, model string
	err = db.QueryRow(ctx, `select r.actor,r.charge_team,ar.resource_id from runs r
        join effective_run_nodes n on n.run_id=r.id and n.ordinal=1
        join agent_revisions ar on (ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision) where r.id=$1`, seed.Run).
		Scan(&actor, &team, &model)
	if err != nil {
		t.Fatal(err)
	}
	agent := func(name string) string {
		t.Helper()
		id := uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", id, actor, team)
		lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
            values($1,1,$2,$2,$3,1,$4)`, id, name, model, actor)
		lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", id, actor)
		lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", id)
		return id
	}
	selector, primary, alternative, independent := agent("选择执行者"), agent("执行主选"), agent("执行备选"), agent("归纳其他材料")
	routes := http.NewServeMux()
	auth := identity.Server{DB: db}
	projects.Server{DB: db, Vault: vault}.Routes(routes, auth)
	knowledge.Server{DB: db}.Routes(routes, auth)
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
	peer, peerToken := uuid.NewString(), web.Token()
	lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'Peer','unused','admin')", peer, peer+"@example.invalid")
	lookupTestSQL(t, ctx, db, "insert into team_members(team_id,member_id) values($1,$2)", team, peer)
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(peerToken), peer)
	tool := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", tool, actor, team)
	lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,tool,created_by)
        values($1,1,'文件工具','编排原文','{"operation":"docx"}'::jsonb,$2)`, tool, actor)
	lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", tool)
	skillTestRequest(t, routes, token, "POST", "/api/preferences", map[string]any{"agent": tool, "body": private, "enabled": true}, 409)

	type pinnedPreference struct {
		ID       string
		Revision int
	}
	for _, tc := range []struct {
		name                      string
		primary, withdraw, revoke bool
	}{
		{name: "selected alternative"},
		{name: "selected primary", primary: true},
		{name: "withdrawn before inference", withdraw: true},
		{name: "edited during inference", revoke: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pickPrimary.Store(tc.primary)
			revokeOnCall.Store(nil)
			project := uuid.NewString()
			lookupTestSQL(t, ctx, db, "insert into projects(id,owner,final_reviewer,team_id,title) values($1,$2,$2,$3,$4)", project, actor, team, tc.name)
			lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, actor)
			lookupTestSQL(t, ctx, db, "insert into project_budgets(project_id) values($1) on conflict do nothing", project)
			call := func(method, url string, body any, want int) []byte {
				t.Helper()
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(method, url, bytes.NewReader(raw))
				req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
				req.Header.Set("Idempotency-Key", web.Token())
				w := httptest.NewRecorder()
				routes.ServeHTTP(w, req)
				if w.Code != want {
					t.Fatalf("%s %s: status=%d want=%d %s", method, url, w.Code, want, w.Body)
				}
				return w.Body.Bytes()
			}
			created := call("POST", "/api/preferences", map[string]any{"agent": alternative, "body": private, "enabled": true}, 200)
			var pref struct{ ID string }
			if err := json.Unmarshal(created, &pref); err != nil || pref.ID == "" {
				t.Fatalf("preference creation: %s %v", created, err)
			}
			if options := call("GET", "/api/preferences", nil, 200); !bytes.Contains(options, []byte(alternative)) {
				t.Fatalf("missing preference scope: %s", options)
			}
			if view := skillTestRequest(t, routes, peerToken, "GET", "/api/preferences", nil, 200); bytes.Contains(view, []byte(private)) {
				t.Fatal("team administrator could read another member's preference")
			}
			root := "/api/projects/" + project
			var rev int
			if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&rev); err != nil {
				t.Fatal(err)
			}
			plan := projects.PlanInput{Expected: rev, Brief: "核对合同材料", Agents: []string{selector, primary, independent},
				AgentRevisions: []int{1, 1, 1}, Dependencies: [][]int{{}, {1}, {}}, Preferences: []struct {
					ID       string
					Revision int
				}{pinnedPreference{ID: pref.ID, Revision: 1}}}
			call("POST", root+"/plan", plan, 422)
			plan.ToolChoices = []projects.ToolChoicePin{{Step: 2, Selector: 1, Alternative: alternative, Revision: 1}}
			call("POST", root+"/plan", plan, 204)
			var admitted struct{ ID string }
			raw := call("POST", root+"/runs", map[string]int{"expected": rev + 1, "authorizeCalls": 3}, 202)
			if err := json.Unmarshal(raw, &admitted); err != nil || admitted.ID == "" {
				t.Fatalf("run acceptance: %s %v", raw, err)
			}
			run := admitted.ID
			if _, err := db.Exec(ctx, `insert into plan_preferences
                select project_id,plan_version,$2,1 from runs where id=$1`, run, pref.ID); err == nil ||
				!strings.Contains(err.Error(), "scope-aware plan writer") {
				t.Fatalf("old plan writer widened the preference: %v", err)
			}
			if _, err := db.Exec(ctx, "insert into invocations(run_id,ordinal,status) values($1,3,'submitted')", run); err == nil ||
				!strings.Contains(err.Error(), "scoped preferences require an updated Worker") {
				t.Fatalf("legacy writer entered a scoped run: %v", err)
			}
			if tc.withdraw {
				call("PATCH", "/api/preferences/"+pref.ID, map[string]any{"body": private, "enabled": false, "expected": 1}, 200)
			}
			if tc.revoke {
				revokeOnCall.Store(&pref.ID)
			}
			wantPersonal := !tc.primary && !tc.withdraw
			for _, ordinal := range []int{3, 1, 2} {
				got, err := a.AdvanceTextStep(ctx, Step{run, ordinal})
				if err == nil && got.TextReply != nil {
					got, err = a.CommitTextReply(ctx, *got.TextReply)
				}
				if err != nil || got.Stop || got.Wait != (ordinal == 2 && tc.revoke) {
					t.Fatalf("step %d: outcome=%+v err=%v", ordinal, got, err)
				}
				select {
				case modelInput := <-requests:
					data, _ := json.Marshal(modelInput)
					if bytes.Contains(data, []byte(private)) != (wantPersonal && ordinal == 2) {
						t.Fatalf("step %d used wrong preference scope: %q", ordinal, data)
					}
				default:
					t.Fatalf("no model request for step %d", ordinal)
				}
			}
			var actorRefs, otherRefs, actualCalls, candidates int
			err = db.QueryRow(ctx, `select count(*) filter(where ordinal=2),count(*) filter(where ordinal<>2),
                (select count(*) from spend where run_id=$1),
                (select count(*) from artifacts where (run_id,ordinal)=($1,2))
                from run_working_inputs where run_id=$1 and kind='preference'`, run).
				Scan(&actorRefs, &otherRefs, &actualCalls, &candidates)
			if err != nil || actualCalls != 3 || (actorRefs == 1) != wantPersonal || otherRefs != 0 || (candidates == 0) != tc.revoke {
				t.Fatalf("wrong frozen memory: used=%d unrelated=%d calls=%d candidates=%d err=%v", actorRefs, otherRefs, actualCalls, candidates, err)
			}
		})
	}
}

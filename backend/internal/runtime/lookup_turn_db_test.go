package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLookupTurnPostgresRecovery(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	var calls atomic.Int32
	var truncated atomic.Bool
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reason := "stop"
		if truncated.Load() {
			reason = "length"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": reason, "message": map[string]string{"content": `{"terms":["质保单"]}`}}},
			"usage":   map[string]int{"total_tokens": 17},
		})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	vault, err := resources.NewVault(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	for _, tc := range []struct {
		name, outcome, status string
		artifacts             int
	}{{"replay", "lookup_refined", "running", 2}, {"revoke", "invalidated", "blocked", 1}, {"cancel", "run_ended", "cancelled", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			step, source := seedLookupTurn(t, ctx, a, provider.URL)
			before := calls.Load()
			started, err := a.AdvanceTurn(ctx, step)
			if err != nil || started.LookupAttempt != 1 || calls.Load() != before+1 {
				t.Fatalf("did not checkpoint one model reply: %+v %v, provider calls: %d", started, err, calls.Load()-before)
			}
			var pending, notYetPublished bool
			if err := db.QueryRow(ctx, `select t.decision is not null,i.result is null from lookup_turns t join invocations i using(run_id,ordinal)
                where t.run_id=$1 and t.ordinal=$2`, step.Run, step.Ordinal).Scan(&pending, &notYetPublished); err != nil || !pending || !notYetPublished {
				t.Fatalf("expected a sealed pending decision with no result: %v", err)
			}
			projectPending := lookupTurnProjectReader(t, ctx, db, step.Run)
			if !projectPending() {
				t.Fatal("project did not report the pending lookup")
			}
			next, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			restarted := Activities{Catalog: resources.Catalog{DB: next, Vault: vault}}
			replayed, err := restarted.AdvanceTurn(ctx, step)
			if err != nil || replayed.LookupAttempt != 1 || calls.Load() != before+1 {
				t.Fatalf("retry did not reuse the checkpoint: %+v %v", replayed, err)
			}
			switch tc.name {
			case "revoke":
				lookupTestSQL(t, ctx, next, "update sources set revoked_at=now() where id=$1", source)
				if err := next.QueryRow(ctx, "select decision is not null from lookup_turns where run_id=$1", step.Run).Scan(&pending); err != nil || pending {
					t.Fatalf("source revocation left a saved decision: %v", err)
				}
			case "cancel":
				lookupTestSQL(t, ctx, next, "update runs set status='cancelled',control_revision=control_revision+1 where id=$1", step.Run)
			}
			wait := make(chan error, 2)
			for i := 0; i < cap(wait); i++ {
				go func() {
					_, err := restarted.ExecuteLookupAction(ctx, lookupAction{step, replayed.LookupAttempt})
					wait <- err
				}()
			}
			for i := 0; i < cap(wait); i++ {
				if err := <-wait; err != nil {
					t.Fatal(err)
				}
			}
			var outcome, status, body string
			var sealedGone bool
			var artifacts, charges, tokens int
			err = db.QueryRow(ctx, `select t.outcome,r.status,coalesce(i.result,''),t.decision is null,
                (select count(*) from artifacts where run_id=r.id),(select count(*) from spend where run_id=r.id),i.tokens
                from lookup_turns t join runs r on r.id=t.run_id join invocations i using(run_id,ordinal)
                where t.run_id=$1 and t.ordinal=$2`, step.Run, step.Ordinal).Scan(&outcome, &status, &body, &sealedGone, &artifacts, &charges, &tokens)
			if err != nil || outcome != tc.outcome || status != tc.status || !sealedGone || artifacts != tc.artifacts ||
				charges != 1 || tokens != 17 || calls.Load() != before+1 {
				t.Fatalf("outcome=%q status=%q sealed gone=%t artifacts=%d charges=%d tokens=%d calls=%d err=%v",
					outcome, status, sealedGone, artifacts, charges, tokens, calls.Load()-before, err)
			}
			if projectPending() {
				t.Fatal("project still reported a pending lookup")
			}
			if (tc.name == "replay" && !strings.Contains(body, "周五前签收")) ||
				(tc.name != "replay" && body != "") {
				t.Fatalf("unexpected published output %q", body)
			}
			if tc.name == "replay" {
				var observations int
				if err := db.QueryRow(ctx, "select count(*) from trace_events where run_id=$1 and code='lookup_refined'", step.Run).
					Scan(&observations); err != nil || observations != 1 {
					t.Fatalf("concurrent workers recorded %d observations: %v", observations, err)
				}
			}
		})
	}
	t.Run("provider marks syntactically complete text as truncated", func(t *testing.T) {
		truncated.Store(true)
		step, _ := seedLookupTurn(t, ctx, a, provider.URL)
		outcome, err := a.AdvanceTurn(ctx, step)
		if err != nil || outcome.LookupAttempt != 0 || outcome.Wait {
			t.Fatalf("truncated reply should only leave existing references to review: %+v %v", outcome, err)
		}
		var body string
		err = db.QueryRow(ctx, "select result from invocations where run_id=$1 and ordinal=$2", step.Run, step.Ordinal).Scan(&body)
		if err != nil || strings.Contains(body, "质保单") || !strings.Contains(body, "模型未给出可核对的回答") {
			t.Fatalf("unexpected truncated reply output: %q %v", body, err)
		}
	})
}

func lookupTurnProjectReader(t *testing.T, ctx context.Context, db *pgxpool.Pool, run string) func() bool {
	t.Helper()
	var project, actor string
	if err := db.QueryRow(ctx, "select project_id,actor from runs where id=$1", run).Scan(&project, &actor); err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(token), actor)
	routes := http.NewServeMux()
	projects.Server{DB: db}.Routes(routes, identity.Server{DB: db})
	return func() bool {
		req := httptest.NewRequest("GET", "/api/projects/"+project, nil).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		res := httptest.NewRecorder()
		routes.ServeHTTP(res, req)
		var snapshot struct {
			Run struct{ LookupPending bool }
		}
		if err := json.Unmarshal(res.Body.Bytes(), &snapshot); err != nil || res.Code != http.StatusOK {
			t.Fatalf("read project snapshot: %d %v %s", res.Code, err, res.Body)
		}
		return snapshot.Run.LookupPending
	}
}

func seedLookupTurn(t *testing.T, ctx context.Context, a Activities, endpoint string) (Step, string) {
	t.Helper()
	db := a.Catalog.DB
	member, team, model, agent := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	project, run, source := uuid.NewString(), uuid.NewString(), uuid.NewString()
	title := "报价核对"
	key, err := a.Catalog.Vault.Seal("fixture", "model/"+model+"/1")
	if err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'Review','unused','member')", member, member+"@example.invalid")
	lookupTestSQL(t, ctx, db, "insert into teams(id,name) values($1,$2)", team, team)
	lookupTestSQL(t, ctx, db, "insert into team_members values($1,$2)", team, member)
	lookupTestSQL(t, ctx, db, "insert into model_resources(id,name,team_id,created_by) values($1,'Fixture',$2,$3)", model, team, member)
	lookupTestSQL(t, ctx, db, "insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,estimate_cny) values($1,1,$2,'fixture',$3,0.2)", model, endpoint, key)
	lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", agent, member, team)
	lookupTestSQL(t, ctx, db, "insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by) values($1,1,'Fixture','核对原文',$2,1,$3)", agent, model, member)
	lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", agent, member)
	lookupTestSQL(t, ctx, db, "insert into projects(id,owner,final_reviewer,team_id,title,stage,plan_version) values($1,$2,$2,$3,$4,'running',1)", project, member, team, title)
	lookupTestSQL(t, ctx, db, "insert into project_members(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, member)
	lookupTestSQL(t, ctx, db, "insert into project_budgets(project_id) values($1) on conflict do nothing", project)
	lookupTestSQL(t, ctx, db, "insert into plans(project_id,version,brief,confirmed_by) values($1,1,$2,$3)", project, title, member)
	lookupTestSQL(t, ctx, db, "insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision,lookup_phase) values($1,1,1,$2,1,'search'),($1,1,2,$2,1,'answer')", project, agent)
	lookupTestSQL(t, ctx, db, "insert into node_edges(project_id,plan_version,ordinal,depends_on) values($1,1,2,1)", project)
	lookupTestSQL(t, ctx, db, "insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,'running',$4,$5,'a',3)", run, project, member, team, run)
	first := "报价表仅用于采购。"
	text := first + "\n质保单必须在周五前签收。"
	hash := receiptDigest([]byte(text))
	lookupTestSQL(t, ctx, db, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", hash, len(text))
	lookupTestSQL(t, ctx, db, "insert into sources(id,project_id,name,created_by) values($1,$2,'报价资料.txt',$3)", source, project, member)
	lookupTestSQL(t, ctx, db, "insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state) values($1,1,$2,'text/plain',$3,$4,'报价资料.txt','ready')", source, hash, text, member)
	lookupTestSQL(t, ctx, db, "insert into plan_sources(project_id,plan_version,source_id,source_version) values($1,1,$2,1)", project, source)
	initial := (lookupHit{version: 1, end: len([]rune(first)), text: first}).line()
	lookupTestSQL(t, ctx, db, "insert into invocations(run_id,ordinal,status,result) values($1,1,'completed',$2)", run, initial)
	lookupTestSQL(t, ctx, db, "insert into artifacts(project_id,run_id,ordinal,name) values($1,$2,1,'已有引文')", project, run)
	return Step{run, 2}, source
}

func lookupTestSQL(t *testing.T, ctx context.Context, db *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

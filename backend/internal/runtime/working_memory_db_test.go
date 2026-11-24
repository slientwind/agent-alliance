package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/memory"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
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

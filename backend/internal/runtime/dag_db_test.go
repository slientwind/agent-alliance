package runtime

import (
	"alliance/internal/database"
	"alliance/internal/resources"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/testsuite"
)

func TestIndependentDAGDispatchPostgres(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("cf", 32))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var joined, slowReleasedByDependent, dependentReadOnlyItsParent atomic.Bool
	dependentArrived := make(chan struct{})
	var once sync.Once
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct{ Messages []resources.Message }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		var prior strings.Builder
		for _, message := range request.Messages[2:] {
			prior.WriteString(message.Content)
		}
		text := ""
		switch request.Messages[0].Content {
		case "slow branch":
			select {
			case <-dependentArrived:
				slowReleasedByDependent.Store(true)
			case <-time.After(8 * time.Second):
			case <-r.Context().Done():
				return
			}
			text = "slow evidence"
		case "fast branch":
			text = "fast evidence"
		case "fast dependent":
			dependentReadOnlyItsParent.Store(strings.Contains(prior.String(), "fast evidence") && !strings.Contains(prior.String(), "slow evidence"))
			once.Do(func() { close(dependentArrived) })
			text = "dependent result"
		case "merge branches":
			joined.Store(strings.Contains(prior.String(), "slow evidence") && strings.Contains(prior.String(), "dependent result"))
			text = "final result"
		default:
			http.Error(w, "unexpected fixture step", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "stop", "message": map[string]string{"content": text},
		}}, "usage": map[string]int{"total_tokens": 7}})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	a := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	seed, _ := seedLookupTurn(t, ctx, a, provider.URL)
	var project, actor, team, model string
	if err := db.QueryRow(ctx, `select r.project_id,r.actor,r.charge_team,v.resource_id from runs r
  join nodes n on(n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,1)
  join agent_revisions v on(v.agent_id,v.revision)=(n.agent_id,n.agent_revision) where r.id=$1`, seed.Run).
		Scan(&project, &actor, &team, &model); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "insert into plans(project_id,version,brief,confirmed_by) values($1,2,'assemble evidence',$2)", project, actor)
	for i, instruction := range []string{"slow branch", "fast branch", "fast dependent", "merge branches"} {
		agent := uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", agent, actor, team)
		lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
    values($1,1,$2,$2,$3,1,$4)`, agent, instruction, model, actor)
		lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", agent, actor)
		lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", agent)
		lookupTestSQL(t, ctx, db, "insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,2,$2,$3,1)", project, i+1, agent)
	}
	lookupTestSQL(t, ctx, db, "insert into node_edges values($1,2,3,2),($1,2,4,1),($1,2,4,3)", project)
	lookupTestSQL(t, ctx, db, "update projects set plan_version=2 where id=$1", project)
	run := uuid.NewString()
	lookupTestSQL(t, ctx, db, `insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
    values($1,$2,2,$3,'queued',$4,$5,'fixture',4)`, run, project, actor, team, run)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(20 * time.Second)
	env.RegisterActivity(a.Topology)
	env.RegisterActivity(a.EnableStepRemoteInput)
	env.RegisterActivity(a.ExecuteStep)
	env.ExecuteWorkflow(Collaborate, run)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var status string
	var spend, artifacts int
	var enabled bool
	err = db.QueryRow(ctx, `select status,(select count(*) from spend where run_id=$1),
		(select count(*) from artifacts where run_id=$1),step_remote_input from runs where id=$1`, run).Scan(&status, &spend, &artifacts, &enabled)
	if err != nil || status != "completed" || spend != 4 || artifacts != 4 || calls.Load() != 4 || !enabled {
		t.Fatalf("parallel execution did not complete once per step: status=%s spend=%d artifacts=%d calls=%d err=%v", status, spend, artifacts, calls.Load(), err)
	}
	if !slowReleasedByDependent.Load() || !dependentReadOnlyItsParent.Load() || !joined.Load() {
		t.Fatalf("dependency or overlap failed: overlapped=%t scoped=%t joined=%t", slowReleasedByDependent.Load(), dependentReadOnlyItsParent.Load(), joined.Load())
	}
}

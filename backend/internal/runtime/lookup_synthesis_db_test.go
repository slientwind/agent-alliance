package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/services"
	"alliance/internal/web"
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEvidenceFollowUpPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	var mode atomic.Value
	mode.Store("")
	var calls, synthesized atomic.Int32
	var sentCheckedEvidence atomic.Bool
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.Messages) < 2 {
			http.Error(w, "expected prompt and input", http.StatusBadRequest)
			return
		}
		prompt := request.Messages[0].Content
		body := `{"terms":["质保单"]}`
		scenario := mode.Load().(string)
		switch {
		case strings.Contains(prompt, synthesisInstructions):
			synthesized.Add(1)
			for _, message := range request.Messages {
				if strings.Contains(message.Content, "[1] 收据.txt v1 【S") &&
					strings.Contains(message.Content, "质保单必须在周五前签收。") {
					sentCheckedEvidence.Store(true)
				}
			}
			switch scenario {
			case "again":
				body = `{"terms":["还想查"]}`
			case "unknown":
				http.Error(w, "fixture has no receipt", http.StatusServiceUnavailable)
				return
			default:
				body = `{"answer":"质保单必须在周五前签收。","references":[1]}`
			}
		case strings.Contains(prompt, searchInstructions) || scenario == "repeat":
			body = `{"terms":["报价表"]}`
		case scenario == "answered":
			body = `{"answer":"报价表仅用于采购。","references":[1]}`
		case strings.Contains(prompt, batchedLookupInstructions):
			body = `{"searchBatches":[["报价表"],["备用单"]]}`
			if scenario == "batch fresh" {
				body = `{"searchBatches":[["报价表"],["质保单"]]}`
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": body}}},
			"usage":   map[string]int{"total_tokens": 11},
		})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
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
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	routes := http.NewServeMux()
	auth := identity.Server{DB: db}
	projects.Server{DB: db, Vault: vault}.Routes(routes, auth)
	services.Server{DB: db}.Routes(routes, auth)
	for _, scenario := range []string{"fresh", "repeat", "answered", "again", "unknown", "revoked", "changed", "batch fresh", "batch repeat"} {
		t.Run(scenario, func(t *testing.T) {
			mode.Store(scenario)
			sentCheckedEvidence.Store(false)
			before, beforeExtra := calls.Load(), synthesized.Load()
			seed, firstSource := seedLookupTurn(t, ctx, worker, provider.URL)
			var project, actor, agent string
			if err := db.QueryRow(ctx, `select r.project_id,r.actor,n.agent_id from runs r join nodes n
					on(n.project_id,n.plan_version)=(r.project_id,r.plan_version) where r.id=$1 and n.ordinal=2`,
				seed.Run).Scan(&project, &actor, &agent); err != nil {
				t.Fatal(err)
			}
			lookupTestSQL(t, ctx, db, "update runs set status='cancelled' where id=$1", seed.Run)
			lookupTestSQL(t, ctx, db, "update projects set stage='brief' where id=$1", project)
			lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", agent)
			lookupTestSQL(t, ctx, db, "update source_versions set extracted='报价表仅用于采购。' where source_id=$1", firstSource)
			secondSource := uuid.NewString()
			const receipt = "质保单必须在周五前签收。"
			hash := receiptDigest([]byte(receipt))
			lookupTestSQL(t, ctx, db, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", hash, len(receipt))
			lookupTestSQL(t, ctx, db, "insert into sources(id,project_id,name,created_by) values($1,$2,'收据.txt',$3)", secondSource, project, actor)
			lookupTestSQL(t, ctx, db, `insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state)
				values($1,1,$2,'text/plain',$3,$4,'收据.txt','ready')`, secondSource, hash, receipt, actor)
			token := uuid.NewString()
			lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(token), actor)
			root := "/api/projects/" + project
			currentRevision := func() int {
				t.Helper()
				var rev int
				if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&rev); err != nil {
					t.Fatal(err)
				}
				return rev
			}
			plan := projects.PlanInput{
				Expected: currentRevision(), Brief: "查明报价资料中的核对要求",
				Agents: []string{agent}, AgentRevisions: []int{1}, LookupSteps: []int{1}, FollowUpSteps: []int{1},
				Sources:          []projects.SourcePin{{ID: firstSource, Version: 1}, {ID: secondSource, Version: 1}},
				DeliveryContract: &projects.DeliveryContract{Items: []projects.DeliveryRequirement{{Step: 1, Label: "提供带出处的核对结论"}}},
			}
			skillTestRequest(t, routes, token, "POST", root+"/plan", plan, http.StatusNoContent)
			var finalOrdinal int
			if err := db.QueryRow(ctx, "select ordinal from plan_delivery_requirements where project_id=$1", project).Scan(&finalOrdinal); err != nil || finalOrdinal != 3 {
				t.Fatalf("required outcome mapped to %d, expected final answer: %v", finalOrdinal, err)
			}
			start := func(calls, want int) string {
				t.Helper()
				body, _ := json.Marshal(map[string]int{"expected": currentRevision(), "authorizeCalls": calls})
				req := httptest.NewRequest("POST", root+"/runs", bytes.NewReader(body)).WithContext(ctx)
				req.Header.Set("Idempotency-Key", web.Token())
				req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
				w := httptest.NewRecorder()
				routes.ServeHTTP(w, req)
				if w.Code != want {
					t.Fatalf("authorize %d calls: %d want %d: %s", calls, w.Code, want, w.Body)
				}
				var accepted map[string]string
				if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
					t.Fatal(err)
				}
				return accepted["id"]
			}
			start(2, http.StatusUnprocessableEntity)
			run := start(3, http.StatusAccepted)
			if result, err := worker.ExecuteStep(ctx, Step{run, 1}); err != nil || result.Wait {
				t.Fatalf("search failed: %+v %v", result, err)
			}
			advance := worker.AdvanceExecutionTurn
			batched := strings.HasPrefix(scenario, "batch ")
			if batched {
				advance = worker.AdvanceBatchedLookup
			}
			answer, err := advance(ctx, Step{run, 2})
			if err != nil || answer.Wait || ((answer.Action == nil) != (scenario == "answered")) {
				t.Fatalf("answer step did not record the expected decision: %+v %v", answer, err)
			}
			if batched {
				for round := 1; round <= 2; round++ {
					if answer.Action == nil || answer.Action.Round != round {
						t.Fatalf("expected batch %d, got %+v", round, answer)
					}
					observation, err := worker.ExecuteAction(ctx, *answer.Action)
					if err != nil || observation.Wait || observation.Observation == nil {
						t.Fatalf("observe batch %d: %+v %v", round, observation, err)
					}
					answer, err = worker.CommitLocalObservation(ctx, *observation.Observation)
					if err != nil || answer.Wait {
						t.Fatalf("commit batch %d: %+v %v", round, answer, err)
					}
				}
			} else if answer.Action != nil {
				if result, err := worker.ExecuteAction(ctx, *answer.Action); err != nil || result.Wait {
					t.Fatalf("read-only refinement failed: %+v %v", result, err)
				}
			}
			if scenario == "revoked" {
				lookupTestSQL(t, ctx, db, "update sources set revoked_at=now() where id=$1", secondSource)
			}
			if scenario == "changed" {
				lookupTestSQL(t, ctx, db, "update source_versions set extracted='质保单必须在周四前签收。' where source_id=$1", secondSource)
			}
			waiting := scenario == "unknown" || scenario == "revoked" || scenario == "changed"
			last := Step{run, 3}
			for range 2 {
				result, err := worker.ExecuteStep(ctx, last)
				if err != nil || result.Wait != waiting && !result.Stop {
					t.Fatalf("unexpected follow-up disposition: %+v %v", result, err)
				}
			}
			extra := scenario == "fresh" || scenario == "again" || scenario == "unknown" || scenario == "batch fresh"
			wantCalls := int32(2)
			wantExtra := int32(0)
			if extra {
				wantCalls++
				wantExtra++
			}
			if calls.Load()-before != wantCalls || synthesized.Load()-beforeExtra != wantExtra || extra && !sentCheckedEvidence.Load() {
				t.Fatalf("wrong provider effects: total %d, follow-up %d, verified evidence %t",
					calls.Load()-before, synthesized.Load()-beforeExtra, sentCheckedEvidence.Load())
			}
			var artifacts, charges int
			var candidate *string
			var state string
			if err := db.QueryRow(ctx, `select r.status,(select count(*) from artifacts a where a.run_id=r.id),
				(select count(*) from spend s where s.run_id=r.id),
				(select v.body from artifacts a join artifact_versions v on(v.artifact_id,v.version)=(a.id,a.current_version)
				 where a.run_id=r.id and a.ordinal=3) from runs r where r.id=$1`, run).
				Scan(&state, &artifacts, &charges, &candidate); err != nil || int32(charges) != wantCalls {
				t.Fatalf("incorrect persisted billing/impact: %d %v", charges, err)
			}
			if waiting {
				if candidate != nil || artifacts != 1 || (state != "reconcile" && state != "blocked") {
					t.Fatalf("unavailable follow-up incorrectly published a candidate: %q %d %v", state, artifacts, candidate)
				}
				return
			}
			wanted := map[string]string{"fresh": "质保单必须在周五前签收。", "repeat": "追加检索未取得新依据",
				"answered": "报价表仅用于采购。", "again": "本次补查已结束", "batch fresh": "质保单必须在周五前签收。",
				"batch repeat": "分组补查未取得新依据"}[scenario]
			if state != "completed" || artifacts != 2 || candidate == nil || !strings.Contains(*candidate, wanted) {
				t.Fatalf("did not produce exactly the expected final candidate: %q %d %v", state, artifacts, candidate)
			}
			if scenario == "fresh" {
				lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", actor)
				created := skillTestRequest(t, routes, token, "POST", "/api/services", map[string]string{
					"name": "原文续答", "project": project, "instructions": "得到新证据时续答",
				}, http.StatusCreated)
				var service struct{ ID string }
				if err := json.Unmarshal(created, &service); err != nil || service.ID == "" {
					t.Fatalf("published service missing: %v", err)
				}
				entry := skillTestRequest(t, routes, token, "GET", "/api/services/"+service.ID+"/entry", nil, http.StatusOK)
				if !bytes.Contains(entry, []byte(`"lookupPhase": "synthesize"`)) && !bytes.Contains(entry, []byte(`"lookupPhase":"synthesize"`)) {
					t.Fatalf("published service dropped the planned follow-up")
				}

				// Template storage opens after delivery. Approval itself is exercised by the delivery suite.
				lookupTestSQL(t, ctx, db, "update projects set stage='delivered' where id=$1", project)
				body, _ := json.Marshal(map[string]any{"expected": currentRevision(), "name": "原文续答模板", "inputs": "原文资料",
					"outcomes": "带出处的回答", "instructions": "补查获得新证据时续答",
					"deliveryContract": projects.DeliveryContract{Items: []projects.DeliveryRequirement{{Step: 1, Label: "提供带出处的核对结论"}}}})
				req := httptest.NewRequest("POST", root+"/workflow-templates", bytes.NewReader(body)).WithContext(ctx)
				req.Header.Set("Idempotency-Key", web.Token())
				req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
				w := httptest.NewRecorder()
				routes.ServeHTTP(w, req)
				if w.Code != http.StatusCreated {
					t.Fatalf("save follow-up template: %d %s", w.Code, w.Body)
				}
				var phase string
				var businessStep int
				if err := db.QueryRow(ctx, "select nodes->2->>'lookupPhase',(delivery_contract->'items'->0->>'step')::int from workflow_template_versions where origin_project=$1", project).
					Scan(&phase, &businessStep); err != nil || phase != "synthesize" || businessStep != 1 {
					t.Fatalf("template lost the conditional step or copied an execution ordinal: %s %d %v", phase, businessStep, err)
				}
			}
		})
	}
}

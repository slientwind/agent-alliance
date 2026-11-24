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
	var calls, synthesized, reviewed atomic.Int32
	var sentCheckedEvidence, sentFurtherEvidence, sentScopedReceipt atomic.Bool
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
				if strings.Contains(message.Content, "[1] 收据.txt v1 【S") && strings.Contains(message.Content, "延期申请须经采购经理确认。") {
					sentFurtherEvidence.Store(true)
				}
				if strings.Contains(message.Content, "质保失效须抄送资料管理员。") && !strings.Contains(message.Content, "内部备注不应随补件发送。") {
					sentScopedReceipt.Store(true)
				}
			}
			switch scenario {
			case "again":
				body = `{"terms":["还想查"]}`
			case "unknown":
				http.Error(w, "fixture has no receipt", http.StatusServiceUnavailable)
				return
			case "review fresh":
				body = `{"answer":"延期申请须经采购经理确认。","references":[1]}`
			default:
				body = `{"answer":"质保单必须在周五前签收。","references":[1]}`
			}
		case strings.Contains(prompt, evidenceReviewInstructions):
			reviewed.Add(1)
			switch scenario {
			case "review fresh", "review revoked":
				body = `{"searchBatches":[["报价表"],["采购经理"]]}`
			case "review repeats":
				body = `{"terms":["报价表"]}`
			case "review asks":
				body = `{"question":"请补充供应商的书面延期条款。"}`
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
			if strings.HasPrefix(scenario, "review ") && scenario != "review no progress" {
				body = `{"terms":["质保单"]}`
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
	advanceEvidence := func(ctx context.Context, step Step) (Result, error) {
		advanced, err := worker.AdvanceEvidenceStep(ctx, step)
		if err != nil || advanced.TextReply == nil {
			return advanced, err
		}
		return worker.CommitEvidenceReply(ctx, *advanced.TextReply)
	}
	routes := http.NewServeMux()
	auth := identity.Server{DB: db}
	projects.Server{DB: db, Vault: vault}.Routes(routes, auth)
	services.Server{DB: db}.Routes(routes, auth)
	for _, scenario := range []string{"fresh", "repeat", "answered", "again", "unknown", "revoked", "changed", "batch fresh", "batch repeat",
		"review fresh", "review answers", "review asks", "review repeats", "review no progress", "review revoked"} {
		t.Run(scenario, func(t *testing.T) {
			mode.Store(scenario)
			sentCheckedEvidence.Store(false)
			sentFurtherEvidence.Store(false)
			sentScopedReceipt.Store(false)
			before, beforeExtra, beforeReview := calls.Load(), synthesized.Load(), reviewed.Load()
			withReview := strings.HasPrefix(scenario, "review ")
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
			receipt := "质保单必须在周五前签收。"
			if scenario == "review fresh" || scenario == "review revoked" {
				receipt += "\n延期申请须经采购经理确认。"
			} else if scenario == "review repeats" {
				// Six fresh references leave the initial quotation off the
				// next prompt. Looking it up again still isn't new progress.
				receipt += "\n质保单须有甲方盖章。\n质保单须列明序列号。\n质保单采用原厂格式。\n质保单须注明检验结果。\n质保单应存入采购档案。"
			}
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
			wantFinal := 3
			if withReview {
				plan.EvidenceReviewSteps = []int{1}
				wantFinal = 4
			}
			skillTestRequest(t, routes, token, "POST", root+"/plan", plan, http.StatusNoContent)
			var finalOrdinal int
			if err := db.QueryRow(ctx, "select ordinal from plan_delivery_requirements where project_id=$1", project).Scan(&finalOrdinal); err != nil || finalOrdinal != wantFinal {
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
			start(wantFinal-1, http.StatusUnprocessableEntity)
			run := start(wantFinal, http.StatusAccepted)
			if withReview {
				topology, err := worker.Topology(ctx, run)
				if err != nil || len(topology) != 4 || !topology[2].LookupAnswer || topology[3].LookupAnswer ||
					len(topology[3].Dependencies) != 1 || topology[3].Dependencies[0] != 3 {
					t.Fatalf("worker topology cannot advance the review before final synthesis: %+v %v", topology, err)
				}
			}
			if scenario == "review fresh" {
				const allowed = "质保失效须抄送资料管理员。"
				const whole = allowed + "\n内部备注不应随补件发送。"
				doc := uuid.NewString()
				digest := receiptDigest([]byte(whole))
				lookupTestSQL(t, ctx, db, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", digest, len(whole))
				lookupTestSQL(t, ctx, db, "insert into sources(id,project_id,name,created_by) values($1,$2,'补件.txt',$3)", doc, project, actor)
				lookupTestSQL(t, ctx, db, `insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state)
                    values($1,1,$2,'text/plain',$3,$4,'补件.txt','ready')`, doc, digest, whole, actor)
				lookupTestSQL(t, ctx, db, `insert into run_source_receipts(run_id,ordinal,request_revision,source_id,source_version,kind,excerpt_start,excerpt_end)
                    values($1,2,nextval('source_receipt_request_no'),$2,1,'lookup',0,$3)`, run, doc, len([]rune(allowed)))
			}
			if result, err := worker.ExecuteStep(ctx, Step{run, 1}); err != nil || result.Wait {
				t.Fatalf("search failed: %+v %v", result, err)
			}
			advance := worker.AdvanceExecutionTurn
			batched := strings.HasPrefix(scenario, "batch ")
			if batched || withReview {
				advance = advanceEvidence
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
			} else if withReview {
				for answer.Action != nil {
					observation, err := worker.ExecuteAction(ctx, *answer.Action)
					if err != nil || observation.Observation == nil || observation.Wait {
						t.Fatalf("observe before review: %+v %v", observation, err)
					}
					answer, err = worker.CommitLocalObservation(ctx, *observation.Observation)
					if err != nil || answer.Wait {
						t.Fatalf("commit before review: %+v %v", answer, err)
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
			if withReview {
				review, err := advanceEvidence(ctx, Step{run, 3})
				expectsAction := scenario == "review fresh" || scenario == "review repeats" || scenario == "review revoked"
				if err != nil || review.Wait || (review.Action != nil) != expectsAction {
					t.Fatalf("unexpected evidence review: %+v %v", review, err)
				}
				for review.Action != nil {
					observation, err := worker.ExecuteAction(ctx, *review.Action)
					if err != nil || observation.Observation == nil || observation.Wait {
						t.Fatalf("observe review: %+v %v", observation, err)
					}
					review, err = worker.CommitLocalObservation(ctx, *observation.Observation)
					if err != nil || review.Wait {
						t.Fatalf("publish review: %+v %v", review, err)
					}
				}
			}
			if scenario == "review revoked" {
				lookupTestSQL(t, ctx, db, "update sources set revoked_at=now() where id=$1", secondSource)
			}
			waiting := scenario == "unknown" || scenario == "revoked" || scenario == "changed" || scenario == "review revoked"
			last := Step{run, wantFinal}
			for range 2 {
				result, err := advanceEvidence(ctx, last)
				if err != nil || result.Wait != waiting && !result.Stop {
					t.Fatalf("unexpected follow-up disposition: %+v %v", result, err)
				}
			}
			extra := scenario == "fresh" || scenario == "again" || scenario == "unknown" || scenario == "batch fresh" || scenario == "review fresh"
			wantCalls := int32(2)
			wantExtra := int32(0)
			if extra {
				wantCalls++
				wantExtra++
			}
			wantReview := int32(0)
			if withReview && scenario != "review no progress" {
				wantCalls++
				wantReview++
			}
			if calls.Load()-before != wantCalls || synthesized.Load()-beforeExtra != wantExtra || reviewed.Load()-beforeReview != wantReview ||
				extra && !sentCheckedEvidence.Load() || scenario == "review fresh" && (!sentFurtherEvidence.Load() || !sentScopedReceipt.Load()) {
				t.Fatalf("wrong provider effects: total %d, follow-up %d, verified evidence %t",
					calls.Load()-before, synthesized.Load()-beforeExtra, sentCheckedEvidence.Load())
			}
			var artifacts, charges int
			var candidate *string
			var state string
			if err := db.QueryRow(ctx, `select r.status,(select count(*) from artifacts a where a.run_id=r.id),
				(select count(*) from spend s where s.run_id=r.id),
				(select v.body from artifacts a join artifact_versions v on(v.artifact_id,v.version)=(a.id,a.current_version)
				 where a.run_id=r.id and a.ordinal=$2) from runs r where r.id=$1`, run, wantFinal).
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
				"batch repeat": "分组补查未取得新依据", "review fresh": "延期申请须经采购经理确认。",
				"review answers": "质保单必须在周五前签收。", "review asks": "请补充供应商的书面延期条款。",
				"review repeats": "追加检索未取得新依据", "review no progress": "分组补查未取得新依据"}[scenario]
			if state != "completed" || artifacts != 2 || candidate == nil || !strings.Contains(*candidate, wanted) {
				t.Fatalf("did not produce exactly the expected final candidate: %q %d %v", state, artifacts, candidate)
			}
			if scenario == "fresh" || scenario == "review fresh" {
				lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", actor)
				var publication struct{ PlanVersion int }
				preview := skillTestRequest(t, routes, token, "GET", "/api/service-publications/projects/"+project, nil, 200)
				if err := json.Unmarshal(preview, &publication); err != nil || publication.PlanVersion < 1 {
					t.Fatalf("publication source plan missing: %s %v", preview, err)
				}
				created := skillTestRequest(t, routes, token, "POST", "/api/services", map[string]any{
					"name": "原文续答", "project": project, "instructions": "得到新证据时续答",
					"expectedPlanVersion": publication.PlanVersion, "confirmDeliveryContract": true,
				}, http.StatusCreated)
				var service struct{ ID string }
				if err := json.Unmarshal(created, &service); err != nil || service.ID == "" {
					t.Fatalf("published service missing: %v", err)
				}
				entry := skillTestRequest(t, routes, token, "GET", "/api/services/"+service.ID+"/entry", nil, http.StatusOK)
				if !bytes.Contains(entry, []byte(`"lookupPhase": "synthesize"`)) && !bytes.Contains(entry, []byte(`"lookupPhase":"synthesize"`)) {
					t.Fatalf("published service dropped the planned follow-up")
				}
				if withReview && !bytes.Contains(entry, []byte(`review_evidence`)) {
					t.Fatal("published service dropped the authorized evidence review")
				}
				var published struct {
					DeliveryContract services.ServiceDeliveryContract
				}
				if err := json.Unmarshal(entry, &published); err != nil || len(published.DeliveryContract.Items) != 1 ||
					published.DeliveryContract.Items[0].Step != wantFinal {
					t.Fatalf("published service lost the final delivery step: %s %v", entry, err)
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
				if err := db.QueryRow(ctx, "select nodes->$2::integer->>'lookupPhase',(delivery_contract->'items'->0->>'step')::int from workflow_template_versions where origin_project=$1", project, wantFinal-1).
					Scan(&phase, &businessStep); err != nil || phase != "synthesize" || businessStep != 1 {
					t.Fatalf("template lost the conditional step or copied an execution ordinal: %s %d %v", phase, businessStep, err)
				}
			}
		})
	}
}

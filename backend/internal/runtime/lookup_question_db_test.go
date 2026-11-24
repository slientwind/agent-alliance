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
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEvidenceQuestionPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
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
	vault, err := resources.NewVault(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	const question = "请补充供应商书面延期时限。"
	const supplement = "供应商同意延长至周五。"
	const unrelated = "不可发送的内部备注。"
	var calls atomic.Int32
	var sawApprovedExcerpt atomic.Bool
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) < 2 {
			http.Error(w, "invalid fixture input", 400)
			return
		}
		prompt := req.Messages[0].Content
		body := "独立成果"
		switch {
		case strings.Contains(prompt, searchInstructions):
			body = `{"terms":["报价表"]}`
		case strings.Contains(prompt, synthesisInstructions):
			content, _ := json.Marshal(req.Messages)
			sawApprovedExcerpt.Store(bytes.Contains(content, []byte(supplement)) && !bytes.Contains(content, []byte(unrelated)))
			body = `{"answer":"` + supplement + `","references":[1]}`
		case strings.Contains(prompt, answerInstructions):
			body = `{"question":"` + question + `"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": body}}},
			"usage":   map[string]int{"total_tokens": 6},
		})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	a := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	routes := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(routes, identity.Server{DB: db})
	for _, scenario := range []string{"resume once", "revoke prior evidence", "legacy reply"} {
		t.Run(scenario, func(t *testing.T) {
			before := calls.Load()
			seed, input := seedLookupTurn(t, ctx, a, provider.URL)
			var project, actor, agent string
			if err := db.QueryRow(ctx, `select r.project_id,r.actor,n.agent_id from runs r
                join effective_run_nodes n on n.run_id=r.id where (r.id,n.ordinal)=($1,2)`, seed.Run).Scan(&project, &actor, &agent); err != nil {
				t.Fatal(err)
			}
			lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", agent)
			lookupTestSQL(t, ctx, db, "update runs set status='cancelled' where id=$1", seed.Run)
			lookupTestSQL(t, ctx, db, "update projects set stage='brief' where id=$1", project)
			token := web.Token()
			lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
			path := "/api/projects/" + project
			current := func() int {
				t.Helper()
				var rev int
				if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&rev); err != nil {
					t.Fatal(err)
				}
				return rev
			}
			post := func(url string, data any, key string, want int) []byte {
				t.Helper()
				raw, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
				req.Header.Set("Idempotency-Key", key)
				req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
				res := httptest.NewRecorder()
				routes.ServeHTTP(res, req)
				if res.Code != want {
					t.Fatalf("POST %s: got %d, want %d: %s", url, res.Code, want, res.Body)
				}
				return res.Body.Bytes()
			}
			post(path+"/plan", projects.PlanInput{Expected: current(), Brief: "核对报价并确认书面延期时限",
				Agents: []string{agent, agent}, AgentRevisions: []int{1, 1}, Dependencies: [][]int{{}, {}},
				LookupSteps: []int{1}, FollowUpSteps: []int{1}, Sources: []projects.SourcePin{{ID: input, Version: 1}},
				DeliveryContract: &projects.DeliveryContract{Items: []projects.DeliveryRequirement{{Step: 1, Label: "延期时限"}}}},
				web.Token(), 204)
			var accepted struct{ ID string }
			if err := json.Unmarshal(post(path+"/runs", map[string]any{
				"expected": current(), "authorizeCalls": 4, "awaitLookupReceipt": true,
			}, web.Token(), 202), &accepted); err != nil || accepted.ID == "" {
				t.Fatal("missing run", err)
			}
			run := accepted.ID
			if step, err := a.ExecuteStep(ctx, Step{run, 1}); err != nil || step.Wait {
				t.Fatalf("search: %+v %v", step, err)
			}
			advance, commit := a.AdvanceEvidenceQuestion, a.CommitEvidenceQuestion
			if scenario == "legacy reply" {
				advance, commit = a.AdvanceEvidenceStep, a.CommitEvidenceReply
			}
			ref, err := advance(ctx, Step{run, 2})
			if err != nil || ref.TextReply == nil {
				t.Fatalf("question checkpoint: %+v %v", ref, err)
			}
			for range 2 {
				got, err := commit(ctx, *ref.TextReply)
				if err != nil || got.Wait || got.Stop {
					t.Fatalf("adopt question: %+v %v", got, err)
				}
			}
			if scenario == "legacy reply" {
				got, err := advance(ctx, Step{run, 3})
				if err != nil || got.Wait || got.TextReply != nil || calls.Load()-before != 2 {
					t.Fatalf("legacy did not skip follow-up: %+v %v", got, err)
				}
				return
			}
			waiting, err := advance(ctx, Step{run, 3})
			if err != nil || !waiting.Wait {
				t.Fatalf("receipt wait: %+v %v", waiting, err)
			}
			if result, err := a.ExecuteStep(ctx, Step{run, 4}); err != nil || result.Wait {
				t.Fatalf("independent branch: %+v %v", result, err)
			}
			var revision, paidCalls, parentCandidates int
			err = db.QueryRow(ctx, `select cr.request_revision,
                (select count(*) from spend where run_id=$1),
                (select count(*) from artifacts where (run_id,ordinal)=($1,2))
                from calculation_requests cr where (cr.run_id,cr.ordinal)=($1,3)`, run).
				Scan(&revision, &paidCalls, &parentCandidates)
			if err != nil || paidCalls != 3 || parentCandidates != 0 || calls.Load()-before != 3 {
				t.Fatalf("wait wrote side effects: rev=%d paid=%d parent=%d err=%v", revision, paidCalls, parentCandidates, err)
			}
			read := func() []byte {
				return skillTestRequest(t, routes, token, "GET", path, nil, 200)
			}
			if !bytes.Contains(read(), []byte(question)) {
				t.Fatal("originator cannot read question")
			}
			if scenario == "resume once" {
				peer, peerToken := uuid.NewString(), web.Token()
				lookupTestSQL(t, ctx, db, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'Peer','unused','member')", peer, peer+"@example.invalid")
				lookupTestSQL(t, ctx, db, "insert into project_members(project_id,member_id) values($1,$2)", project, peer)
				lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(peerToken), peer)
				if got := skillTestRequest(t, routes, peerToken, "GET", path, nil, 200); bytes.Contains(got, []byte(question)) {
					t.Fatal("model's question was exposed to a different viewer")
				}
			}
			source := uuid.NewString()
			body := supplement + "\n" + unrelated
			hash := receiptDigest([]byte(body))
			lookupTestSQL(t, ctx, db, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", hash, len(body))
			lookupTestSQL(t, ctx, db, "insert into sources(id,project_id,name,created_by) values($1,$2,'延期函',$3)", source, project, actor)
			lookupTestSQL(t, ctx, db, `insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state)
                values($1,1,$2,'text/plain',$3,$4,'延期函','ready')`, source, hash, body, actor)
			submit := "/api/runs/" + run + "/lookup-receipts"
			in := projects.ReceiptSelection{Expected: 1, Step: 3, Source: source, Version: 1, Excerpt: supplement, ReceiptRevision: revision}
			key := web.Token()
			if scenario == "revoke prior evidence" {
				lookupTestSQL(t, ctx, db, "update sources set revoked_at=now() where id=$1", input)
				if bytes.Contains(read(), []byte(question)) {
					t.Fatal("unusable parent question remained visible")
				}
				post(submit, in, key, 409)
				if calls.Load()-before != 3 {
					t.Fatal("stale receipt triggered an inference")
				}
				return
			}
			post(submit, in, key, 202)
			post(submit, in, key, 202)
			post(submit, in, web.Token(), 409)
			final, err := advance(ctx, Step{run, 3})
			if err != nil || final.TextReply == nil {
				t.Fatalf("continued inference: %+v %v", final, err)
			}
			if got, err := commit(ctx, *final.TextReply); err != nil || got.Wait {
				t.Fatalf("publish continuation: %+v %v", got, err)
			}
			if !sawApprovedExcerpt.Load() || calls.Load()-before != 4 || !bytes.Contains(read(), []byte(supplement)) {
				t.Fatal("missing grounded result or unexpected model input")
			}
		})
	}
}

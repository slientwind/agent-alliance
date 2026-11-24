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

type testIndexWait struct {
	revision, question       int
	active, pending, expired bool
	observation              *string
}

type failingIndexSignal struct{ receiptSignalCapture }

func (*failingIndexSignal) SignalWorkflow(context.Context, string, string, string, any) error {
	return errors.New("temporary signal failure")
}

func TestStepLookupIndexPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("use a scratch database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const encoderRevision = "bge-small-zh-v1.5-int8/1"
	var calls, probes atomic.Int32
	var noMatch, offline atomic.Bool
	encoder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		if offline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		vector := make([]float64, 512)
		if noMatch.Load() {
			vector[1] = 1
		} else {
			vector[0] = 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"encoder": encoderRevision, "vectors": [][]float64{vector}})
	}))
	defer encoder.Close()
	t.Setenv("RETRIEVAL_URL", encoder.URL)
	model := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"finish_reason": "stop", "message": map[string]string{"content": `{"answer":"核对完成","references":[1]}`}},
		}, "usage": map[string]int{"total_tokens": 12}})
	}))
	defer model.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: model.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	vault, err := resources.NewVault(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	answer, source := seedLookupTurn(t, ctx, worker, model.URL)
	run := answer.Run
	var project, actor string
	var control int
	if err := db.QueryRow(ctx, "select project_id,actor,control_revision from runs where id=$1", run).Scan(&project, &actor, &control); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, `insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision,lookup_phase)
        select n.project_id,n.plan_version,v.ordinal,n.agent_id,n.agent_revision,v.phase from nodes n
        cross join (values(3,'answer'),(4,''),(5,'answer')) v(ordinal,phase) where n.project_id=$1 and n.ordinal=2`, project)
	lookupTestSQL(t, ctx, db, "insert into node_edges values($1,1,3,1),($1,1,5,1)", project)
	lookupTestSQL(t, ctx, db, "update runs set await_lookup_receipt=true,max_calls=5 where id=$1", run)
	marker := "\n" + semanticIndexPending + "\n" + noEvidence
	lookupTestSQL(t, ctx, db, "update invocations set result=$2 where run_id=$1 and ordinal=1", run, marker)
	lookupTestSQL(t, ctx, db, "insert into outbox(run_id,sent_at) values($1,now())", run)
	if err := worker.EnableStepSourceReceipts(ctx, run); err != nil {
		t.Fatal(err)
	}
	const quote = "报价表仅用于采购。"
	lookupTestSQL(t, ctx, db, "delete from source_passages where source_id=$1", source)
	lookupTestSQL(t, ctx, db, "insert into source_passages(source_id,version,start_char,passage) values($1,1,1,$2)", source, quote)
	const supplied = "替代依据要求周一交接。"
	file := uuid.NewString()
	digest := receiptDigest([]byte(supplied))
	lookupTestSQL(t, ctx, db, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", digest, len(supplied))
	lookupTestSQL(t, ctx, db, "insert into sources(id,project_id,name,created_by) values($1,$2,'替代依据.txt',$3)", file, project, actor)
	lookupTestSQL(t, ctx, db, `insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state)
        values($1,1,$2,'text/plain',$3,$4,'替代依据.txt','ready')`, file, digest, supplied, actor)

	token := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
	routes := http.NewServeMux()
	projects.Server{DB: db}.Routes(routes, identity.Server{DB: db})
	request := func(path, key string, body any, want int) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/runs/"+run+"/"+path, bytes.NewReader(raw)).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", key)
		res := httptest.NewRecorder()
		routes.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: status %d, want %d, response %s", path, key, res.Code, want, res.Body.String())
		}
	}
	wait := func(step int) testIndexWait {
		t.Helper()
		var w testIndexWait
		err := db.QueryRow(ctx, `select coalesce(w.step_wait_revision,0),coalesce(q.request_revision,0),
            w.active,w.wake_pending,w.expiry_announced,w.observation from lookup_index_waits w
            left join calculation_requests q using(run_id,ordinal) where (w.run_id,w.ordinal)=($1,$2)`, run, step).
			Scan(&w.revision, &w.question, &w.active, &w.pending, &w.expired, &w.observation)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	advance := func(step int, parked bool) {
		t.Helper()
		out, err := worker.AdvanceExecutionTurn(ctx, Step{run, step})
		if err != nil || out.Wait != parked || out.Stop {
			t.Fatalf("step %d: %+v %v", step, out, err)
		}
	}
	for _, step := range []int{2, 3, 5} {
		advance(step, true)
		if w := wait(step); !w.active || w.pending || w.revision == 0 || w.question == 0 {
			t.Fatalf("step %d did not park independently: %+v", step, w)
		}
	}
	advance(4, false)
	if calls.Load() != 1 || probes.Load() != 0 {
		t.Fatalf("independent execution spent additional calls: model=%d encoder=%d", calls.Load(), probes.Load())
	}

	// The optional file wins this wait, without touching the other two.
	request("lookup-receipts", "early-file", projects.ReceiptSelection{
		Step: 5, Expected: control, ReceiptRevision: wait(5).question, Source: file, Version: 1, Excerpt: supplied,
	}, 202)
	var fileSignal receiptSignalCapture
	if err := worker.signalControl(ctx, &fileSignal); err != nil || fileSignal.signal != sourceReceiptPrefix+"5" {
		t.Fatalf("manual receipt did not replace the index wake: %+v %v", fileSignal, err)
	}
	advance(5, false)
	var oldWait bool
	if err := db.QueryRow(ctx, "select exists(select from lookup_index_waits where run_id=$1 and ordinal=5)", run).Scan(&oldWait); err != nil || oldWait {
		t.Fatalf("accepted file left a second wake behind: %v %v", oldWait, err)
	}
	first, second := wait(2), wait(3)
	lookupTestSQL(t, ctx, db, "update lookup_index_waits set next_check_at=now()-interval '10 seconds' where run_id=$1 and ordinal=2", run)
	lookupTestSQL(t, ctx, db, "update lookup_index_waits set deadline=now()-interval '1 second' where run_id=$1 and ordinal=3", run)
	vector := "[" + "1," + strings.Repeat("0,", 510) + "0]"
	lookupTestSQL(t, ctx, db, "update source_passages set embedding=$2::text::vector,encoder=$3 where source_id=$1", source, vector, encoderRevision)

	request("control", "index-pause", map[string]any{"expected": control, "action": "pause", "reason": "fixture"}, 202)
	if err := worker.wakeIndexedLookup(ctx); err != nil || !wait(2).active {
		t.Fatalf("pause allowed or removed indexing work: %v", err)
	}
	request("control", "index-resume", map[string]any{"expected": control + 1, "action": "resume", "reason": "fixture"}, 202)
	control += 2
	if err := worker.wakeIndexedLookup(ctx); err != nil {
		t.Fatal(err)
	}
	if w := wait(2); w.active || !w.pending || w.revision != first.revision || w.question != 0 {
		t.Fatalf("index readiness did not queue exactly the current wait: %+v", w)
	}
	if err := worker.wakeIndexedLookup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := worker.signalIndexedLookup(ctx, &failingIndexSignal{}); err == nil || !wait(2).pending {
		t.Fatalf("failed signal lost its durable wake: %v", err)
	}
	lookupTestSQL(t, ctx, db, "update lookup_index_waits set next_check_at=now() where run_id=$1 and ordinal=2", run)
	replacement := Activities{Catalog: worker.Catalog}
	var signal receiptSignalCapture
	if err := replacement.signalIndexedLookup(ctx, &signal); err != nil ||
		signal.workflow != "run/"+run || signal.channel != "control" || signal.signal != sourceReceiptPrefix+"2" {
		t.Fatalf("index event did not wake just step 2: %+v %v", signal, err)
	}
	if w := wait(3); !w.active || w.pending || !w.expired || w.question != second.question {
		t.Fatalf("second wait did not keep its own deadline and optional receipt: %+v", w)
	}
	request("lookup-receipts", "losing-file", projects.ReceiptSelection{
		Step: 2, Expected: control, ReceiptRevision: first.question, Source: file, Version: 1, Excerpt: supplied,
	}, 409)
	advance(2, false)
	if w := wait(2); w.observation == nil || !strings.Contains(*w.observation, source) || probes.Load() != 1 || calls.Load() != 3 {
		t.Fatalf("index recovery did not reuse search and produce one grounded answer: %+v, probes=%d calls=%d", w, probes.Load(), calls.Load())
	}

	manual := projects.IndexRecheckInput{Step: 3, Expected: control, WaitRevision: second.revision}
	request("lookup-index/recheck", "retry-expired", manual, 202)
	request("lookup-index/recheck", "retry-expired", manual, 202)
	request("lookup-index/recheck", "stale-retry", manual, 409)
	offline.Store(true)
	advance(3, true)
	if w := wait(3); !w.active || w.pending || !w.expired || w.revision == second.revision || w.question <= second.question {
		t.Fatalf("encoder failure did not preserve the deadline and offer a new check: %+v", w)
	}
	request("lookup-index/recheck", "old-generation", manual, 409)
	manual.WaitRevision = wait(3).revision
	request("lookup-index/recheck", "retry-encoder", manual, 202)
	offline.Store(false)
	noMatch.Store(true)
	advance(3, true)
	if w := wait(3); w.active || w.pending || w.question <= second.question || w.observation == nil || *w.observation != "[]" ||
		calls.Load() != 3 || probes.Load() != 3 {
		t.Fatalf("no evidence did not become a new source request: %+v, probes=%d calls=%d", w, probes.Load(), calls.Load())
	}
	request("lookup-receipts", "same-file-new-question", projects.ReceiptSelection{
		Step: 3, Expected: control, ReceiptRevision: wait(3).question, Source: file, Version: 1, Excerpt: supplied,
	}, 202)
	advance(3, false)
	var status string
	var revision, plan, spend int
	err = db.QueryRow(ctx, `select status,control_revision,plan_version,(select count(*) from spend where run_id=r.id)
        from runs r where r.id=$1`, run).Scan(&status, &revision, &plan, &spend)
	if err != nil || status != "completed" || revision != control || plan != 1 || spend != 4 || calls.Load() != 4 {
		t.Fatalf("index paths changed global state or charged a replay: %s revision=%d plan=%d spend=%d calls=%d %v", status, revision, plan, spend, calls.Load(), err)
	}
	lookupTestSQL(t, ctx, db, "update controls set sent_at=now() where run_id=$1 and wake_ordinal is null", run)
	if err := worker.signalControl(ctx, &fileSignal); err != nil || fileSignal.signal != sourceReceiptPrefix+"3" {
		t.Fatalf("manual receipt did not wake the last step: %+v %v", fileSignal, err)
	}

	// A persisted legacy wait keeps the run-wide gate even if its run
	// already supports scoped source supplements.
	legacy, _ := seedLookupTurn(t, ctx, worker, model.URL)
	lookupTestSQL(t, ctx, db, "update invocations set result=$2 where run_id=$1 and ordinal=1", legacy.Run, marker)
	lookupTestSQL(t, ctx, db, "update runs set step_source_receipt=true,status='blocked' where id=$1", legacy.Run)
	lookupTestSQL(t, ctx, db, `insert into lookup_index_waits(run_id,ordinal,plan_version,active,deadline,next_check_at)
        values($1,2,1,true,now()+interval '2 minutes',now())`, legacy.Run)
	if err := worker.wakeIndexedLookup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "select status from runs where id=$1", legacy.Run).Scan(&status); err != nil || status != "blocked" {
		t.Fatalf("legacy wait resumed before its index was ready: %s %v", status, err)
	}
	lookupTestSQL(t, ctx, db, `update source_passages p set embedding=$2::text::vector,encoder=$3
        from runs r join plan_sources s on (s.project_id,s.plan_version)=(r.project_id,r.plan_version)
        where r.id=$1 and p.source_id=s.source_id`, legacy.Run, vector, encoderRevision)
	lookupTestSQL(t, ctx, db, "update lookup_index_waits set next_check_at=now() where run_id=$1", legacy.Run)
	if err := worker.wakeIndexedLookup(ctx); err != nil {
		t.Fatal(err)
	}
	var globalWake bool
	if err := db.QueryRow(ctx, `select status,exists(select from receipt_wakes where run_id=r.id) from runs r where r.id=$1`, legacy.Run).
		Scan(&status, &globalWake); err != nil || status != "running" || !globalWake {
		t.Fatalf("legacy history lost its global receipt wake: %s %v %v", status, globalWake, err)
	}
	lookupTestSQL(t, ctx, db, "insert into outbox(run_id,sent_at) values($1,now())", legacy.Run)
	if err := worker.wakeReturnedSubmission(ctx, &signal); err != nil ||
		signal.workflow != "run/"+legacy.Run || signal.signal != "original-receipt" {
		t.Fatalf("legacy receipt did not use its original signal: %+v %v", signal, err)
	}
}

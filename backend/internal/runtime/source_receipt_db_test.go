package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/channels"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/client"
)

type receiptSignalCapture struct {
	client.Client
	workflow, channel, signal string
}

func (c *receiptSignalCapture) SignalWorkflow(_ context.Context, workflow, _, channel string, signal any) error {
	c.workflow, c.channel, c.signal = workflow, channel, signal.(string)
	return nil
}

func TestStepSourceReceiptsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
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

	const quote = "甲组原文规定在周五签收。"
	const firstPrice = "乙组第一笔为 500.00 元。"
	const secondPrice = "乙组第二笔为 540.50 元。"
	const private = "同一份原件中没有选用的正文。"
	var calls, leaks atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []struct{ Content string } }
		_ = json.NewDecoder(r.Body).Decode(&input)
		var prompt strings.Builder
		for _, msg := range input.Messages {
			prompt.WriteString(msg.Content)
		}
		content := prompt.String()
		reply := "已完成独立步骤"
		switch {
		case strings.Contains(content, "可用金额编号"):
			if strings.Contains(content, quote) || !strings.Contains(content, firstPrice) || !strings.Contains(content, secondPrice) {
				leaks.Add(1)
			}
			reply = `{"calculation":{"operation":"difference","operands":[1,2]}}`
		case strings.Contains(content, quote):
			if strings.Contains(content, firstPrice) {
				leaks.Add(1)
			}
			reply = `{"answer":"按原文签收","references":[1]}`
		case strings.Contains(content, firstPrice), strings.Contains(content, secondPrice):
			leaks.Add(1)
		}
		if strings.Contains(content, private) {
			leaks.Add(1)
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{
			{"finish_reason": "stop", "message": map[string]string{"content": reply}},
		}, "usage": map[string]int{"total_tokens": 11}})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	vault, err := resources.NewVault(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	a := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	answer, _ := seedLookupTurn(t, ctx, a, provider.URL)
	run := answer.Run
	calculation, other := Step{run, 3}, Step{run, 4}
	var project, actor string
	if err := db.QueryRow(ctx, "select project_id,actor from runs where id=$1", run).Scan(&project, &actor); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, `insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision,lookup_phase)
 select n.project_id,n.plan_version,v.ordinal,n.agent_id,n.agent_revision,v.phase from nodes n
 cross join (values(3,'answer_calc'),(4,'')) v(ordinal,phase) where n.project_id=$1 and n.ordinal=2`, project)
	lookupTestSQL(t, ctx, db, "insert into node_edges values($1,1,3,1)", project)
	lookupTestSQL(t, ctx, db, "update runs set await_lookup_receipt=true,await_calculation_receipt=true,max_calls=4 where id=$1", run)
	lookupTestSQL(t, ctx, db, "update invocations set result=$2 where run_id=$1 and ordinal=1", run, noEvidence)
	lookupTestSQL(t, ctx, db, "insert into artifact_versions(artifact_id,version,body,reason) select id,1,$2,'fixture' from artifacts where run_id=$1", run, noEvidence)
	if err := a.EnableStepSourceReceipts(ctx, run); err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
	routes := http.NewServeMux()
	auth := identity.Server{DB: db}
	projects.Server{DB: db}.Routes(routes, auth)
	feishu := channels.Feishu{DB: db, Vault: vault, App: "fixture-app", Secret: "fixture-secret", Encrypt: "fixture-encrypt",
		Verify: "fixture-verify", Tenant: uuid.NewString(), Public: "https://workspace.example.invalid"}
	feishu.Routes(routes, auth)
	const chat, openID = "fixture-chat", "fixture-actor"
	lookupTestSQL(t, ctx, db, "insert into channel_links(tenant,open_id,member_id) values($1,$2,$3)", feishu.Tenant, openID, actor)
	sendFeishu := func(text string) (note, kind, argument string) {
		t.Helper()
		event := uuid.NewString()
		req := signedRemoteInputFeishuEvent(t, feishu, openID, chat, text, event).WithContext(ctx)
		res := httptest.NewRecorder()
		routes.ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatalf("signed Feishu event: %d %s", res.Code, res.Body)
		}
		if err := db.QueryRow(ctx, "select note,reply_kind,argument from channel_replies where event_id=$1", event).
			Scan(&note, &kind, &argument); err != nil {
			t.Fatal(err)
		}
		return note, kind, argument
	}
	attachFeishu := func(quote string) string {
		t.Helper()
		note, kind, argument := sendFeishu("补件 #3 " + quote)
		if kind != "receipt_proposal" || argument == "" {
			t.Fatalf("Feishu did not offer the current step: %s %s %s", note, kind, argument)
		}
		lookupTestSQL(t, ctx, db, `update channel_proposals set file_key='fixture',file_name='原件.txt',file_message_id='fixture-file' where id=$1`, argument)
		return argument
	}
	request := func(method, path, key string, data any, status int) string {
		t.Helper()
		body, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", key)
		result := httptest.NewRecorder()
		routes.ServeHTTP(result, req)
		if result.Code != status {
			t.Fatalf("%s %s: status=%d wanted %d: %s", method, path, result.Code, status, result.Body.String())
		}
		return result.Body.String()
	}
	store := func(name, text string) string {
		t.Helper()
		id, hash := uuid.NewString(), receiptDigest([]byte(text))
		lookupTestSQL(t, ctx, db, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", hash, len(text))
		lookupTestSQL(t, ctx, db, "insert into sources(id,project_id,name,created_by) values($1,$2,$3,$4)", id, project, name, actor)
		lookupTestSQL(t, ctx, db, `insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state)
            values($1,1,$2,'text/plain',$3,$4,$5,'ready')`, id, hash, text, actor, name)
		return id
	}
	shared := store("共同原件.txt", strings.Join([]string{quote, firstPrice, private}, "\n"))
	second := store("乙方补件.txt", secondPrice)
	advance := func(step Step, wait bool) Result {
		t.Helper()
		out, err := a.AdvanceExecutionTurn(ctx, step)
		if err != nil || out.Wait != wait || out.Stop {
			t.Fatalf("step %d: %+v %v", step.Ordinal, out, err)
		}
		return out
	}
	question := func(step Step) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, "select request_revision from current_source_receipt_requests where (run_id,ordinal)=($1,$2)", run, step.Ordinal).Scan(&n); err != nil || n <= 0 {
			t.Fatalf("step %d has no current receipt revision: %d %v", step.Ordinal, n, err)
		}
		return n
	}
	advance(answer, true)
	advance(calculation, true)
	advance(other, false)
	first, money := question(answer), question(calculation)
	if first == money || calls.Load() != 1 {
		t.Fatalf("both steps must await evidence while their sibling completes, got %d calls", calls.Load())
	}
	var snapshot struct {
		Run struct {
			ControlRevision int
			ReceiptRequest  json.RawMessage
			ReceiptRequests []projects.ReceiptSelection
		}
	}
	if body := request("GET", "/api/projects/"+project, "", nil, 200); json.Unmarshal([]byte(body), &snapshot) != nil ||
		string(snapshot.Run.ReceiptRequest) != "null" || len(snapshot.Run.ReceiptRequests) != 2 ||
		snapshot.Run.ReceiptRequests[0].ReceiptRevision != first || snapshot.Run.ReceiptRequests[1].ReceiptRevision != money {
		t.Fatalf("snapshot did not project both step waits: %s", body)
	}
	initialControl := snapshot.Run.ControlRevision
	path := "/api/runs/" + run
	selected := projects.ReceiptSelection{Step: answer.Ordinal, ReceiptRevision: first, Source: shared, Version: 1, Excerpt: quote}
	selection := request("GET", fmt.Sprintf("%s/receipt-passages?source=%s&step=2&version=1&receiptRevision=%d", path, shared, first), "", nil, 200)
	if !strings.Contains(selection, quote) {
		t.Fatal("authorized receipt could not choose its passage")
	}
	request("POST", path+"/control", "pause", map[string]any{"action": "pause", "expected": initialControl, "reason": "fixture"}, 202)
	request("POST", path+"/lookup-receipts", "paused", selected, 409)
	request("POST", path+"/control", "resume", map[string]any{"action": "resume", "expected": initialControl + 1, "reason": "fixture"}, 202)
	selected.Expected = initialControl + 2
	request("POST", path+"/lookup-receipts", "approved-quote", selected, 202)
	request("POST", path+"/lookup-receipts", "approved-quote", selected, 202)
	lookupTestSQL(t, ctx, db, "update controls set sent_at=now() where run_id=$1 and wake_ordinal is null", run)
	lookupTestSQL(t, ctx, db, "insert into outbox(run_id,sent_at) values($1,now())", run)
	var signal receiptSignalCapture
	if err := a.signalControl(ctx, &signal); err != nil || signal.workflow != "run/"+run ||
		signal.channel != "control" || signal.signal != sourceReceiptPrefix+"2" {
		t.Fatalf("outbox did not signal the exact receipt step: %+v %v", signal, err)
	}
	if question(calculation) != money {
		t.Fatal("supplementing A changed B's wait")
	}
	advance(answer, false)
	advance(calculation, true)
	sendFeishu("切换 " + project)
	staleFeishu := attachFeishu(firstPrice)
	amount := projects.ReceiptSelection{Expected: selected.Expected, Step: calculation.Ordinal, ReceiptRevision: money, Source: shared, Version: 1, Excerpt: firstPrice}
	request("POST", path+"/calculation-receipts", "approved-first-price", amount, 202)
	advance(calculation, true)
	next := question(calculation)
	if next == money {
		t.Fatal("a new lack of evidence reused the old receipt revision")
	}
	if note, _, _ := sendFeishu("确认 " + staleFeishu); !strings.Contains(note, "待补件步骤已变化") {
		t.Fatalf("Feishu treated the old preview as consent to the new question: %q", note)
	}
	freshFeishu := attachFeishu(secondPrice)
	if note, kind, _ := sendFeishu("确认 " + freshFeishu); kind != "attachment_pending" {
		t.Fatalf("fresh Feishu confirmation did not queue its original question: %q %q", kind, note)
	}
	var savedQuestion int
	if err := db.QueryRow(ctx, "select receipt_question_revision from channel_attachment_jobs where id=$1", freshFeishu).Scan(&savedQuestion); err != nil || savedQuestion != next {
		t.Fatalf("attachment job lost its question version: %d %v", savedQuestion, err)
	}
	amount.Source, amount.Excerpt = second, secondPrice
	request("POST", path+"/calculation-receipts", "outdated-question", amount, 409)
	amount.ReceiptRevision = next
	request("POST", path+"/calculation-receipts", "approved-second-price", amount, 202)
	action := advance(calculation, false).Action
	if action == nil {
		t.Fatal("grounded calculation did not produce its durable action")
	}
	if out, err := a.ExecuteAction(ctx, *action); err != nil || out.Wait {
		t.Fatalf("calculation did not commit: %+v %v", out, err)
	}
	var plan, control, charges, wakes, usedByOther int
	var state, result string
	err = db.QueryRow(ctx, `select plan_version,control_revision,status,
   (select result from invocations where run_id=r.id and ordinal=3),
   (select count(*) from spend where run_id=r.id),
   (select count(*) from controls where run_id=r.id and wake_ordinal is not null),
   (select count(*) from run_working_inputs where run_id=r.id and ordinal=4 and ref in ($2,$3))
   from runs r where r.id=$1`, run, shared, second).Scan(&plan, &control, &state, &result, &charges, &wakes, &usedByOther)
	if err != nil || plan != 1 || control != selected.Expected || state != "completed" || !strings.Contains(result, "差额为 40.50") ||
		charges != 3 || wakes != 3 || usedByOther != 0 || leaks.Load() != 0 || calls.Load() != 3 {
		t.Fatalf("incorrect scoped recovery: plan=%d control=%d state=%s result=%q charges=%d wakes=%d other inputs=%d leaked=%d calls=%d: %v",
			plan, control, state, result, charges, wakes, usedByOther, leaks.Load(), calls.Load(), err)
	}
	lookupTestSQL(t, ctx, db, "insert into reviews(artifact_id,version,reviewer) select id,1,$2 from artifacts where run_id=$1", run, actor)
	var projectRevision int
	if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&projectRevision); err != nil {
		t.Fatal(err)
	}
	request("POST", "/api/projects/"+project+"/deliver", "", map[string]int{"expected": projectRevision}, 201)
	var receiptCount int
	if err := db.QueryRow(ctx, `select sum(jsonb_array_length(e.step_receipts))::int from delivery_source_evidence e
        join deliveries d on d.id=e.delivery_id where d.project_id=$1`, project).Scan(&receiptCount); err != nil || receiptCount != 3 {
		t.Fatalf("delivery lost scoped excerpts: %d %v", receiptCount, err)
	}
	lookupTestSQL(t, ctx, db, "update sources set revoked_at=now() where id=$1", shared)
	var aUsable, bUsable, otherUsable bool
	var impacted int
	err = db.QueryRow(ctx, `select run_working_set_usable($1,2,1),run_working_set_usable($1,3,1),run_working_set_usable($1,4,1),
        (select count(*) from distribution_cases where project_id=$2 and source_id=$3)`, run, project, shared).
		Scan(&aUsable, &bUsable, &otherUsable, &impacted)
	if err != nil || aUsable || bUsable || !otherUsable || impacted != 2 {
		t.Fatalf("withdrawal crossed receipt lineage: %v %v %v impacted=%d %v", aUsable, bUsable, otherUsable, impacted, err)
	}
}

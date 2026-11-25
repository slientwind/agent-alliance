package studio

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sampleOffice(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for name, body := range files {
		part, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestSampleKeepsOnlyLayout(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		want Structure
	}{
		{"hidden-customer.docx", sampleOffice(t, map[string]string{
			"word/document.xml": `<document><p><pStyle val="Heading2"/><jc val="right"/><t>SECRET NAME</t></p>
			 <tbl><tblGrid><gridCol/><gridCol/></tblGrid><tblHeader/><numPr/><gridSpan val="2"/></tbl></document>`,
		}), Structure{Kind: "document", Outline: []int{2}, Columns: []int{2}, Alignments: []string{"right"}, Lists: true, HasMerged: true, HasHeaderRows: true}},
		{"hidden-customer.xlsx", sampleOffice(t, map[string]string{
			"xl/worksheets/sheet1.xml": `<worksheet><row><c r="A1"><v>SECRET NAME</v></c><c r="D1"/></row>
			 <row><c r="ZZ2"><v>SECRET VALUE</v></c></row><mergeCell ref="A1:C1"/></worksheet>`,
			"xl/styles.xml": `<styleSheet><alignment horizontal="center"/><alignment horizontal="SECRET VALUE"/></styleSheet>`,
		}), Structure{Kind: "spreadsheet", Columns: []int{4}, Alignments: []string{"center"}, HasMerged: true}},
		{"hidden-customer.pptx", sampleOffice(t, map[string]string{
			"ppt/slides/slide1.xml": `<sld><ph type="title"/><t>SECRET NAME</t><ph type="chart"/><ph type="SECRET VALUE"/></sld>`,
		}), Structure{Kind: "slides", SlideLayouts: []string{"title,chart"}}},
		{"hidden-customer.md", []byte("# SECRET NAME\n- SECRET VALUE\n| SECRET1 | SECRET2 |"), Structure{Kind: "text", Outline: []int{1}, Columns: []int{2}, Lists: true, HasHeaderRows: true}},
		{"hidden-customer.csv", []byte("SECRET1,SECRET2\nPERSON,12345678"), Structure{Kind: "table", Columns: []int{2}, HasHeaderRows: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := sampleStructure(test.name, test.raw)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("%+v want %+v (%v)", got, test.want, err)
			}
			encoded, err := json.Marshal(got)
			if err != nil || bytes.Contains(encoded, []byte("SECRET")) || bytes.Contains(encoded, []byte(test.name)) {
				t.Fatalf("sample exposed raw data: %s (%v)", encoded, err)
			}
		})
	}
}

func TestStudioPatchRejectsUnsupportedChanges(t *testing.T) {
	current := emptySpec()
	current.Steps = []string{"existing step"}
	for _, bad := range []string{
		`{"reply":"test","patch":[{"op":"replace","path":"/scope","value":"enterprise"}]}`,
		`{"reply":"test","patch":[{"op":"remove","path":"/steps","value":[]}]}`,
		`{"reply":"test","patch":[{"op":"replace","path":"/tier","value":"embedding"}]}`,
		`{"reply":"test","patch":[{"op":"replace","path":"/capabilities","value":["external","execute_shell"]}]}`,
		`{"reply":"test","patch":[{"op":"replace","path":"/steps","value":["change"]},{"op":"replace","path":"/steps","value":["repeat"]}]}`,
		`{"reply":"test","questions":["one","two","three"],"patch":[]}`,
	} {
		if _, _, err := modelSuggestion(bad, current); err == nil {
			t.Errorf("accepted invalid patch: %s", bad)
		}
	}
	out, updated, err := modelSuggestion(`{"reply":"已改规则","questions":[],"patch":[{"op":"replace","path":"/steps","value":["new"]}]}`, current)
	if err != nil || len(out.Patch) != 1 || updated.Steps[0] != "new" || current.Steps[0] != "existing step" {
		t.Fatalf("patch changed the wrong state: %+v %+v %v", current, updated, err)
	}
}

func TestRefinementKeepsUnselectedRulesAndUTF16Offsets(t *testing.T) {
	got, err := selectedText("前😀价量尾", 3, 5)
	if err != nil || got != "价量" {
		t.Fatalf("selection = %q %v", got, err)
	}
	if _, err := selectedText("前😀价", 2, 4); err == nil {
		t.Fatal("accepted a selection starting halfway through an emoji")
	}
	diff, err := makeInstructionDiff("✓😀\n金额左对齐\n保留币种", "金额左对齐", "金额右对齐")
	if err != nil || diff.Offset != 4 || diff.After != "✓😀\n金额右对齐\n保留币种" ||
		diff.Removed != "金额左对齐" || diff.Added != "金额右对齐" {
		t.Fatalf("bad diff %+v (%v)", diff, err)
	}
	if _, err := makeInstructionDiff("金额列\n金额列", "金额列", "合计列"); err == nil {
		t.Fatal("accepted an ambiguous patch location")
	}
}

func TestDistillationKeepsReusableOperationsAndDiscardsUnknowns(t *testing.T) {
	spec := distillSpec("报价 核对 ACME PRIVATE 915527", llmgateway.Fast,
		[]string{"aggregate_csv", "aggregate", "read_sources", "create_xlsx", "vendor_private_action"})
	raw, err := json.Marshal(spec)
	if err != nil || spec.Name != "业务核对助手" || spec.Tier != llmgateway.Fast ||
		!reflect.DeepEqual(spec.Capabilities, []string{"office", "knowledge"}) || len(spec.Steps) != 5 {
		t.Fatalf("unusable distilled specification: %+v %v", spec, err)
	}
	if bytes.Contains(raw, []byte("PRIVATE")) || bytes.Contains(raw, []byte("915527")) || bytes.Contains(raw, []byte("vendor_")) {
		t.Fatalf("private task data was retained: %s", raw)
	}
}

type gatewayFixture func(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)

func (g gatewayFixture) Call(ctx context.Context, actor llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
	return g(ctx, actor, request)
}

func TestStudioBuilderPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	team := uuid.NewString()
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into enterprise(name,default_team) values('Studio fixture',$1) on conflict do nothing", team)
	member := func() (string, string) {
		t.Helper()
		id, token := uuid.NewString(), web.Token()
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Studio user','','member')", id, id+"@example.test")
		exec("insert into team_members(team_id,member_id) values($1,$2)", team, id)
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), id)
		return id, token
	}
	owner, token := member()
	_, teammate := member()
	const message = "帮我做一个报价核对助手"
	const modelAnswer = `{"reply":"已生成草稿","questions":["需要按周汇总吗？"],"patch":[
	  {"op":"replace","path":"/name","value":"报价核对"},
	  {"op":"replace","path":"/instructions","value":"保留币种。金额左对齐。将小计放在末尾。"},
	  {"op":"replace","path":"/steps","value":["汇总并核对列"]},
	  {"op":"replace","path":"/capabilities","value":["office"]}]}`
	calls := 0
	s := Server{DB: db, Vault: vault, Gateway: gatewayFixture(func(_ context.Context, actor llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
		calls++
		in := request.Messages[1].Content
		if actor.MemberID != owner || actor.TeamID != team || strings.Contains(in, "SECRET") {
			t.Fatalf("model request had the wrong actor or contained private sample data")
		}
		output := modelAnswer
		if calls == 1 && (!strings.Contains(in, message) || !strings.Contains(in, `"columns":[3]`) || !strings.Contains(in, `"outline":[2]`)) {
			t.Fatalf("model did not receive the request and anonymous layout: %s", in)
		}
		switch calls {
		case 2:
			if !strings.Contains(in, message) || !strings.Contains(in, "需要按周汇总吗？") {
				t.Fatal("clarification lost the original request or question")
			}
			output = `{"reply":"invalid","patch":[{"op":"replace","path":"/owner","value":"outside"}]}`
		case 3:
			if !strings.Contains(request.Messages[0].Content, "金额左对齐") || in != "示例报价" {
				t.Fatal("playground did not use the current draft and explicit trial input")
			}
			output = "金额列；私有试聊内容"
		case 4, 5:
			if strings.Contains(in, "私有试聊内容") || !strings.Contains(in, `"selectedOutput":"金额列"`) {
				t.Fatalf("refinement must receive only the selected output: %s", in)
			}
			output = `{"match":"金额左对齐","replacement":"金额右对齐"}`
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: resources.Reply{Text: output}}}, nil
	})}
	mux := http.NewServeMux()
	s.Routes(mux, identity.Server{DB: db})
	call := func(token, method, route, body, media, key string, status int) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, route, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		r.Header.Set("Content-Type", media)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: status=%d want=%d %s", method, route, w.Code, status, w.Body)
		}
		return w.Body.String()
	}
	var draft draft
	err = json.Unmarshal([]byte(call(token, "POST", "/api/studio", `{}`, "application/json", "", 201)), &draft)
	if err != nil || draft.Team != team || draft.Revision != 1 || draft.ID == "" {
		t.Fatalf("fixture draft not created in its team: %+v %v", draft, err)
	}
	if got := call(token, "GET", "/api/studio", "", "", "", 200); !strings.Contains(got, draft.ID) {
		t.Fatal(got)
	}
	path := "/api/studio/" + draft.ID
	call(teammate, "GET", path, "", "", "", 404)
	call(teammate, "POST", path+"/chat", "irrelevant", "", "wrong-owner", 404)
	var multi bytes.Buffer
	m := multipart.NewWriter(&multi)
	if err := m.WriteField("message", message); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteField("expected", "1"); err != nil {
		t.Fatal(err)
	}
	for name, sample := range map[string]string{
		"SECRET customer.csv": "SECRET PRODUCT,SECRET AMOUNT,SECRET DATE\nA,200,2020",
		"SECRET meeting.md":   "## SECRET HEADING\n- Confidential body",
	} {
		part, err := m.CreateFormFile("samples", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, sample); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	first := call(token, "POST", path+"/chat", multi.String(), m.FormDataContentType(), "build-first", 200)
	var result Turn
	err = json.Unmarshal([]byte(first), &result)
	if err != nil || result.Revision != 2 || result.Spec.Name != "报价核对" || len(result.Patch) != 4 || len(result.Questions) != 1 {
		t.Fatalf("invalid model patch result: %s (%v)", first, err)
	}
	call(token, "POST", path+"/chat", multi.String(), m.FormDataContentType(), "build-first", 200)
	call(token, "POST", path+"/chat", `{"message":"different","expected":1}`, "application/json", "build-first", 409)
	call(token, "POST", path+"/chat", `{"message":"stale","expected":1}`, "application/json", "stale", 409)
	call(token, "POST", path+"/chat", `{"message":"invalid patch","expected":2}`, "application/json", "bad-model", 502)
	if calls != 2 {
		t.Fatalf("model called %d times", calls)
	}
	saved := call(token, "GET", path, "", "", "", 200)
	if strings.Contains(saved, "SECRET") || !strings.Contains(saved, `"revision":2`) || !strings.Contains(saved, message) || strings.Contains(saved, "invalid patch") {
		t.Fatalf("wrong saved conversation: %s", saved)
	}
	var trial trialResult
	err = json.Unmarshal([]byte(call(token, "POST", path+"/try", `{"expected":2,"message":"示例报价"}`, "application/json", "try-once", 200)), &trial)
	if err != nil || trial.ID == "" || trial.Output != "金额列；私有试聊内容" {
		t.Fatalf("trial was not retained: %+v %v", trial, err)
	}
	call(token, "POST", path+"/try", `{"expected":2,"message":"示例报价"}`, "application/json", "try-once", 200)
	request, err := json.Marshal(map[string]any{"expected": 2, "trial": trial.ID, "start": 0, "end": 3, "feedback": "把这一列改成右对齐"})
	if err != nil {
		t.Fatal(err)
	}
	var one, two refinement
	firstRefinement := call(token, "POST", path+"/refine", string(request), "application/json", "refine-one", 200)
	err = json.Unmarshal([]byte(firstRefinement), &one)
	if err != nil || one.Diff.Removed != "金额左对齐" || one.Diff.Added != "金额右对齐" || one.Selected != "金额列" {
		t.Fatalf("no localized proposal: %s (%v)", firstRefinement, err)
	}
	replayRefinement := call(token, "POST", path+"/refine", string(request), "application/json", "refine-one", 200)
	if !strings.Contains(replayRefinement, one.ID) {
		t.Fatal(replayRefinement)
	}
	err = json.Unmarshal([]byte(call(token, "POST", path+"/refine", string(request), "application/json", "refine-two", 200)), &two)
	if err != nil {
		t.Fatal(err)
	}
	current := call(token, "GET", path, "", "", "", 200)
	if !strings.Contains(current, "金额左对齐") || strings.Contains(current, "金额右对齐") {
		t.Fatal("proposal changed the draft before it was applied")
	}
	apply := path + "/refinements/" + one.ID + "/apply"
	call(teammate, "POST", apply, `{"expected":2}`, "application/json", "", 404)
	call(token, "POST", apply, `{"expected":2}`, "application/json", "", 200)
	call(token, "POST", apply, `{"expected":2}`, "application/json", "", 200)
	call(token, "POST", path+"/refinements/"+two.ID+"/apply", `{"expected":2}`, "application/json", "", 409)
	current = call(token, "GET", path, "", "", "", 200)
	if !strings.Contains(current, "保留币种。金额右对齐。将小计放在末尾。") || strings.Contains(current, "私有试聊内容") || calls != 5 {
		t.Fatalf("unexpected refined draft or call count: %s calls=%d", current, calls)
	}
	project, run := uuid.NewString(), uuid.NewString()
	exec(`insert into projects(id,owner,team_id,final_reviewer,title,plan_version)
	 values($1,$2,$3,$2,'PRIVATE-PROJECT',1)`, project, owner, team)
	exec(`insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)`, project, owner)
	exec(`insert into plans(project_id,version,brief,confirmed_by) values($1,1,'核对报价 SECRET-ACCOUNT',$2)`, project, owner)
	exec(`insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls)
	 values($1,$2,1,$3,$4,$1::uuid::text,'x'::bytea,2)`, run, project, owner, team)
	var messageID int64
	if err := db.QueryRow(ctx, `insert into messages(project_id,author,kind,body) values($1,$2,'user','SECRET-ACCOUNT') returning seq`,
		project, owner).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	exec(`insert into general_agent_runs(run_id,tier,goal_message_seq) values($1,'fast',$2)`, run, messageID)
	exec(`insert into general_agent_turns(run_id,turn,state,response) values($1,1,'tools','unused'::bytea)`, run)
	exec(`insert into general_agent_actions(run_id,turn,position,tool_name,call_id,args_cipher,fingerprint,state,observation_cipher)
	 values($1,1,1,'aggregate_csv','one','SECRET-COLUMN'::bytea,'fp','completed','SECRET-RESULT'::bytea),
	 ($1,1,2,'web_search','two','unused'::bytea,'fp2','rejected','unused'::bytea)`, run)
	fromRun := `{"run":"` + run + `"}`
	call(token, "POST", "/api/studio/from-run", fromRun, "application/json", "", 404)
	exec("update runs set status='completed' where id=$1", run)
	call(teammate, "POST", "/api/studio/from-run", fromRun, "application/json", "", 404)
	raw := call(token, "POST", "/api/studio/from-run", fromRun, "application/json", "", 201)
	var reusable struct {
		ID   string
		Spec Spec
	}
	if err := json.Unmarshal([]byte(raw), &reusable); err != nil || reusable.ID == "" || strings.Contains(raw, "SECRET") ||
		reusable.Spec.Tier != llmgateway.Fast || !reflect.DeepEqual(reusable.Spec.Capabilities, []string{"office"}) {
		t.Fatalf("invalid reusable draft: %s (%v)", raw, err)
	}
	replayed := call(token, "POST", "/api/studio/from-run", fromRun, "application/json", "", 200)
	if !strings.Contains(replayed, reusable.ID) || calls != 5 {
		t.Fatal("saving the same run constructed a separate draft or made a model call")
	}
	capabilityPath := path + "/capabilities"
	capabilities := call(token, "GET", capabilityPath, "", "", "", 200)
	if !strings.Contains(capabilities, "企业知识库") || !strings.Contains(capabilities, "已授权外部 Agent") ||
		!strings.Contains(capabilities, `"id":"office"`) {
		t.Fatalf("the user got invalid capability descriptions: %s", capabilities)
	}
	call(teammate, "PUT", capabilityPath, `{"expected":3,"capabilities":[]}`, "application/json", "", 404)
	call(token, "PUT", capabilityPath, `{"expected":3,"capabilities":["office","execute_shell"]}`, "application/json", "", 422)
	raw = call(token, "PUT", capabilityPath, `{"expected":3,"capabilities":["knowledge","images"]}`, "application/json", "", 200)
	if !strings.Contains(raw, `"revision":4`) || !strings.Contains(raw, `"capabilities":["knowledge","images"]`) {
		t.Fatalf("capabilities were not set: %s", raw)
	}
	call(token, "PUT", capabilityPath, `{"expected":3,"capabilities":["office"]}`, "application/json", "", 409)
	exec("delete from team_members where (team_id,member_id)=($1,$2)", team, owner)
	call(token, "GET", path, "", "", "", 404)
}

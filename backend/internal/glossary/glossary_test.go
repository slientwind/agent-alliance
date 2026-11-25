package glossary

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMetricFormulaAllowsOnlyBusinessArithmetic(t *testing.T) {
	if formatted, ok := metricFormula("SUM(gross_sales)-avg(refunds)*0.25"); !ok ||
		formatted != "SUM(gross_sales) - avg(refunds)*0.25" {
		t.Fatalf("approved expression failed to normalize: %q %v", formatted, ok)
	}
	for _, input := range []string{
		"SUM(a); SELECT password", "os.ReadFile(thing)", "net_sales[0]", "case when true then 1 end",
		"sum(a,b)", "sum(a+1)", "avg(x...)", "0xFF+sales", "1e5*sales", "a<<b", "a==b", "sum(a)// sensitive",
		strings.Repeat("(", 15) + "sales" + strings.Repeat(")", 15),
	} {
		if _, ok := metricFormula(input); ok {
			t.Errorf("invalid formula accepted: %s", input)
		}
	}
}

func TestGlossaryIsolationAndApprovedChatCorrectionsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database")
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	admin, lead, employee, other, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	tokens := make(map[string]string)
	for id, role := range map[string]string{admin: "admin", lead: "member", employee: "member", other: "member"} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','unused',$3)", id, id+"@example.test", role)
		tokens[id] = web.Token()
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(tokens[id]), id)
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, lead, employee)
	project := uuid.NewString()
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'Team fixture')", project, employee, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,true)", project, employee, lead)
	var correction int64
	if err := db.QueryRow(ctx, `insert into messages(project_id,author,kind,body)
		values($1,$2,'user','纠正 QCC 术语，另外这段聊天有 PRIVATE-TEXT') returning seq`, project, employee).Scan(&correction); err != nil {
		t.Fatal(err)
	}
	var teamSpace, commons, private string
	if err := db.QueryRow(ctx, `select id from spaces where kind='team' and team_id=$1`, team).Scan(&teamSpace); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select id from spaces where kind='commons'`).Scan(&commons); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `select id from spaces where kind='personal' and owner_member_id=$1`, employee).Scan(&private); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Server{DB: db}.Routes(mux, identity.Server{DB: db})
	do := func(actor, method, path string, body any, status int) string {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(string(raw)))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[actor]})
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: got %d instead of %d: %s", method, path, res.Code, status, res.Body.String())
		}
		return res.Body.String()
	}
	commonID, teamID, candidateID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	commonPath := "/api/spaces/" + commons + "/glossary"
	teamPath := "/api/spaces/" + teamSpace + "/glossary"
	commonEntry := map[string]any{"term": "净销售额", "aliases": []string{"Net Sales"}, "definition": "成交额扣减退款", "metricKey": "net_sales",
		"metricFormula": "sum(gross_sales)-sum(refunds)", "enabled": true, "expectedVersion": 0}
	do(admin, "PUT", commonPath+"/"+commonID, commonEntry, 200)
	defer db.Exec(ctx, "delete from enterprise_glossary where id=$1", commonID)
	shared := map[string]any{"term": "最高优先级审批", "aliases": []string{"ＫＡ"}, "definition": "采购线的审批口径", "enabled": true, "expectedVersion": 0}
	do(employee, "PUT", teamPath+"/"+teamID, shared, 403)
	do(lead, "PUT", teamPath+"/"+teamID, shared, 200)
	shared["term"], shared["aliases"] = "冲突编码", []string{"ka"}
	do(lead, "PUT", teamPath+"/"+uuid.NewString(), shared, 409)
	before := do(employee, "GET", teamPath, nil, 200)
	if !strings.Contains(before, "净销售额") || !strings.Contains(before, "最高优先级审批") || strings.Contains(before, "冲突编码") {
		t.Fatalf("effective glossary has the wrong scope: %s", before)
	}
	if otherView := do(other, "GET", teamPath, nil, 200); otherView != "[]\n" {
		t.Fatalf("ungranted actor can see a team glossary: %s", otherView)
	}
	if adminView := do(admin, "GET", teamPath, nil, 200); adminView != "[]\n" {
		t.Fatalf("enterprise admin bypassed a team workspace: %s", adminView)
	}
	privateView := do(employee, "GET", "/api/spaces/"+private+"/glossary", nil, 200)
	if !strings.Contains(privateView, "净销售额") || strings.Contains(privateView, "采购线") {
		t.Fatal("private workspace inherited another team's definitions")
	}
	fromChat := map[string]any{"id": candidateID, "messageSeq": correction, "term": "质量复核口径", "aliases": []string{"QCC"}, "definition": "核对有效供应批次"}
	correctionPath := "/api/projects/" + project + "/glossary"
	do(lead, "POST", correctionPath, fromChat, 404)
	do(employee, "POST", correctionPath, fromChat, 200)
	repeat := do(employee, "POST", correctionPath, fromChat, 200)
	if !strings.Contains(repeat, `"revision":1`) {
		t.Fatal("retry created a new revision")
	}
	after := do(employee, "GET", teamPath, nil, 200)
	if !strings.Contains(after, "质量复核口径") || strings.Contains(after, "PRIVATE-TEXT") || strings.Contains(after, project) {
		t.Fatalf("published glossary copied unselected conversation data: %s", after)
	}
	fromChat["id"], fromChat["term"], fromChat["aliases"] = uuid.NewString(), "未核定率", []string{"UNCHECKED"}
	fromChat["metricKey"], fromChat["metricFormula"] = "unsafe_metric", "sum(refunds)"
	do(employee, "POST", correctionPath, fromChat, 403)

	shared["term"], shared["aliases"], shared["enabled"], shared["expectedVersion"] = "最高优先级审批", []string{"ＫＡ"}, false, 1
	do(lead, "PUT", teamPath+"/"+teamID, shared, 200)
	if view := do(employee, "GET", teamPath, nil, 200); strings.Contains(view, "最高优先级审批") {
		t.Fatal("disabled term remained visible to members")
	}
	shared["term"], shared["aliases"], shared["enabled"], shared["expectedVersion"] = "采购优先级", []string{"ka"}, true, 0
	do(lead, "PUT", teamPath+"/"+uuid.NewString(), shared, 200)
}

package bizconnectors

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
)

func TestEmployeeOAuthExchangeAndRowScope(t *testing.T) {
	db := connectorTestDB(t)
	space, actors := connectorSpace(t, db)
	ctx := t.Context()
	vault, err := resources.NewVault(strings.Repeat("b4", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	department := uuid.NewString()
	exec("insert into departments(id,name) values($1,$1::uuid::text)", department)
	exec("update members set department_id=$1 where id=any($2::uuid[])", department, []string{actors[0].ID, actors[1].ID})
	cID, viewID := uuid.NewString(), uuid.NewString()
	const issuer = "https://identity.example.test/tenant"
	const audience = "alliance-api"
	const resource = "https://data.example.test"
	const token1 = "test-alice-subject"
	const token2 = "test-bob-subject"
	var tokenRequests, readRequests atomic.Int32
	expiry := time.Now().Add(15 * time.Minute).Unix()
	var revokeDuringRequest atomic.Bool
	s := Server{DB: db, Vault: vault, HTTP: businessFixture(func(r *http.Request) (*http.Response, error) {
		var response string
		switch r.URL.Path {
		case "/introspect":
			user, passwd, _ := r.BasicAuth()
			if user != "app-fixture" || passwd != "secret-fixture" || r.ParseForm() != nil {
				t.Fatal("missing configured confidential client authentication")
			}
			subject, employee := "Alice", "EMP100"
			if r.Form.Get("token") == token2 {
				subject, employee = "Bob", "EMP101"
			}
			response = fmt.Sprintf(`{"active":true,"iss":%q,"sub":%q,"aud":[%q],"exp":%d,"employee_no":%q,"dept_id":"D1"}`,
				issuer, subject, audience, expiry, employee)
		case "/token":
			tokenRequests.Add(1)
			if r.ParseForm() != nil || r.Form.Get("audience") != resource || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" {
				t.Fatal("token exchange ignored configured resource or grant type")
			}
			response = fmt.Sprintf(`{"access_token":%q,"token_type":"Bearer","expires_in":600}`, "delegated-"+r.Form.Get("subject_token"))
		case "/orders":
			readRequests.Add(1)
			employee := r.URL.Query().Get("employee")
			required := map[string]string{"EMP100": "Bearer delegated-" + token1, "EMP101": "Bearer delegated-" + token2}
			if required[employee] == "" || r.URL.Query().Get("department") != "D1" || r.Header.Get("Authorization") != required[employee] {
				t.Fatal("employee credential or mandatory upstream predicates were mixed")
			}
			if revokeDuringRequest.Load() {
				exec("delete from business_obo_grants where (connector_id,member_id)=($1,$2)", cID, actors[0].ID)
			}
			// A provider can return stale rows. The worker must filter both keys before projecting or aggregating.
			response = `[{"employee":"EMP100","department":"D1","amount":42},{"employee":"EMP101","department":"D1","amount":81},
			 {"employee":"EMP100","department":"D2","amount":200},{"department":"D1","amount":400}]`
		default:
			t.Fatalf("unexpected OAuth or data endpoint: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	config := Settings{Resources: map[string]string{"obo_employee_param": "employee", "obo_department_param": "department"},
		OBO: &OBOSettings{Protocol: "rfc8693", Issuer: issuer, SubjectAud: audience, Resource: resource,
			IntrospectURL: "https://identity.example.test/introspect", TokenURL: "https://identity.example.test/token"}}
	operations := []Operation{{Name: "orders", Description: "Orders", Method: "GET", Path: "/orders", Risk: "read", Parameters: map[string]Parameter{
		"employee": {Type: "string", Location: "query", Required: true}, "department": {Type: "string", Location: "query", Required: true},
	}}}
	cipher, err := vault.Seal(`{"appKey":"app-fixture","appSecret":"secret-fixture","token":"must-not-use-service"}`, credentialScope(cID))
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,settings,operations,credential_cipher,maintainer)
	 values($1,$2,'internal','openapi_rest','openapi','Orders',$3,'obo',$4,$5,$6,$7)`, cID, space, resource, config, operations, cipher, actors[0].ID)
	request := func(method, member string, input any) (*httptest.ResponseRecorder, *http.Request) {
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest(method, "/", strings.NewReader(string(raw))).WithContext(ctx)
		r.SetPathValue("space", space)
		r.SetPathValue("connector", cID)
		r.SetPathValue("member", member)
		r.SetPathValue("view", viewID)
		return httptest.NewRecorder(), r
	}
	for i, subject := range []string{"Alice", "Bob"} {
		member := actors[i].ID
		w, r := request("PUT", member, map[string]any{"issuerSubject": subject, "employeeNo": fmt.Sprintf("EMP%d", 100+i), "departmentCode": "D1"})
		if err := s.bindOBOIdentity(w, r, actors[0]); err != nil {
			t.Fatal(err)
		}
	}
	authorize := func(member identity.Member, token string) error {
		w, r := request("POST", member.ID, map[string]string{"subjectToken": token})
		err := s.authorizeOBO(w, r, member)
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("authorization response contained a token")
		}
		return err
	}
	if err := authorize(actors[0], token2); err != web.Fail(403, "授权凭据与当前员工或发行方不匹配") || tokenRequests.Load() != 0 {
		t.Fatalf("cross-employee assertion was exchanged: %v", err)
	}
	for i, token := range []string{token1, token2} {
		if err := authorize(actors[i], token); err != nil {
			t.Fatal(err)
		}
	}
	var sealed []byte
	if err := db.QueryRow(ctx, "select token_cipher from business_obo_grants where (connector_id,member_id)=($1,$2)", cID, actors[0].ID).Scan(&sealed); err != nil ||
		strings.Contains(string(sealed), token1) || len(sealed) < 32 {
		t.Fatal("delegated token was not durably encrypted")
	}
	w, r := request("PUT", "", SemanticView{Connector: cID, Name: "Orders", Description: "Current employee only", Operation: "orders", Enabled: true, Sensitivity: "internal",
		Dimensions: map[string]ViewField{"amount": {Source: "amount", Type: "integer", Label: "金额"}, "owner": {Source: "employee", Type: "string", Label: "负责人"}},
		Metrics:    map[string]Metric{"total": {Label: "本人金额", Formula: "sum(amount)"}}, RLS: map[string]string{"employee_no": "employee", "dept_id": "department"},
	})
	if err := s.configureView(w, r, actors[0]); err != nil {
		t.Fatal(err)
	}
	query := ViewQuery{View: viewID, Metrics: []string{"total"}}
	for i, wanted := range []json.Number{"42", "81"} {
		result, err := s.QueryView(ctx, space, actors[i].ID, query)
		if err != nil || len(result.Rows) != 1 || result.Rows[0]["total"] != wanted || !result.Provenance.ActorOnly || result.Provenance.OBOGrant == "" {
			t.Fatalf("incorrect per-employee aggregation: %d %+v %v", i, result, err)
		}
	}
	query.Filters = map[string]any{"owner": "EMP101"}
	if _, err := s.QueryView(ctx, space, actors[0].ID, query); err != ErrBadQuery || readRequests.Load() != 2 {
		t.Fatalf("model could override employee filter: %v", err)
	}
	query.Filters = nil
	revokeDuringRequest.Store(true)
	if _, err := s.QueryView(ctx, space, actors[0].ID, query); err != ErrUnavailable {
		t.Fatalf("revoked request returned rows: %v", err)
	}
	revokeDuringRequest.Store(false)
	exec("update members set department_id=null where id=$1", actors[1].ID)
	if _, err := s.QueryView(ctx, space, actors[1].ID, query); err != ErrUnavailable || readRequests.Load() != 3 {
		t.Fatalf("department transfer retained the former grant: %v", err)
	}
}

func TestWarehouseRowAuthorizationBindsBothFields(t *testing.T) {
	c := Connector{Driver: "readonly_sql", Provider: "clickhouse", Endpoint: "https://data.example.test", Settings: Settings{Database: "warehouse", AllowedRelations: []string{"orders"}}}
	v := SemanticView{Relation: "orders", Dimensions: map[string]ViewField{"amount": {Source: "order_amount", Type: "integer"}},
		RLS: map[string]string{"employee_no": "staff_code", "dept_id": "department_code"}}
	s := Server{HTTP: businessFixture(func(r *http.Request) (*http.Response, error) {
		person, password, _ := r.BasicAuth()
		params := r.URL.Query()
		sql, _ := io.ReadAll(r.Body)
		if person != "E05" || password != "obo-fixture" || params.Get("param_arg1") != "E05" || params.Get("param_arg2") != "D01" ||
			!strings.Contains(string(sql), "`staff_code` = {arg1:String} and `department_code` = {arg2:String}") {
			t.Fatalf("warehouse request did not bind both verified identity fields: %s", sql)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"amount":81}]}`))}, nil
	})}
	rows, err := s.queryWarehouseView(t.Context(), c, Credentials{Username: "E05", Password: "obo-fixture"}, v,
		ViewQuery{Dimensions: []string{"amount"}}, viewPlan{identity: rowIdentity{Employee: "E05", Department: "D01", Grant: "fixture"}, limit: 4})
	if err != nil || len(rows) != 1 || rows[0]["amount"] != json.Number("81") {
		t.Fatalf("warehouse result: %+v %v", rows, err)
	}
}

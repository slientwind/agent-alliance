package bizconnectors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func connectorTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	db, err := database.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func connectorSpace(t *testing.T, db *pgxpool.Pool) (space string, actors [3]identity.Member) {
	t.Helper()
	exec := func(sql string, params ...any) {
		t.Helper()
		if _, err := db.Exec(t.Context(), sql, params...); err != nil {
			t.Fatal(err)
		}
	}
	for i := range actors {
		actors[i].ID = uuid.NewString()
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,$3,'','member')",
			actors[i].ID, actors[i].ID+"@example.test", "Business Test")
	}
	team := uuid.NewString()
	exec("insert into teams(id,name) values($1,$2)", team, "Business Test "+team)
	for i := range 2 {
		exec("insert into team_members(team_id,member_id) values($1,$2)", team, actors[i].ID)
	}
	if err := db.QueryRow(t.Context(), "select id from spaces where team_id=$1", team).Scan(&space); err != nil {
		t.Fatal(err)
	}
	exec("update space_members set role='space_admin' where (space_id,member_id)=($1,$2)", space, actors[0].ID)
	return space, actors
}

func TestManageConnectorsKeepsCredentialsAndRowsPrivate(t *testing.T) {
	db := connectorTestDB(t)
	space, actors := connectorSpace(t, db)
	vault, err := resources.NewVault(strings.Repeat("f4", 32))
	if err != nil {
		t.Fatal(err)
	}
	s := Server{DB: db, Vault: vault, HTTP: businessFixture(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer fixture-private-token" || req.URL.Path != "/books" {
			t.Fatal("approved test failed to use credential or fixed endpoint")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"name":"private-row"}]`))}, nil
	})}
	id := uuid.NewString()
	request := func(method, payload string) (*httptest.ResponseRecorder, *http.Request) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/", strings.NewReader(payload)).WithContext(t.Context())
		r.SetPathValue("space", space)
		r.SetPathValue("connector", id)
		return w, r
	}
	payload, err := json.Marshal(map[string]any{"name": "Orders", "driver": "openapi_rest", "provider": "openapi", "enabled": true,
		"category": "internal", "endpoint": "https://internal.example.test", "authMode": "service",
		"operations": []Operation{{Name: "books", Description: "Approved order book", Method: "GET", Path: "/books", Risk: "read"}},
		"credential": map[string]string{"token": "fixture-private-token"}})
	if err != nil {
		t.Fatal(err)
	}
	w, r := request("PUT", string(payload))
	if err := s.configure(w, r, actors[0]); err != nil {
		t.Fatal(err)
	}
	w, r = request("POST", `{"operation":"books"}`)
	if err := s.test(w, r, actors[1]); err == nil {
		t.Fatal("ordinary member could run direct connector test")
	}
	w, r = request("POST", `{"operation":"books"}`)
	if err := s.test(w, r, actors[0]); err != nil || strings.Contains(w.Body.String(), "private-row") ||
		!strings.Contains(w.Body.String(), `"rowCount":1`) {
		t.Fatalf("test leaked rows or failed: %s %v", w.Body, err)
	}
	for _, i := range []int{0, 1, 2} {
		w, r = request("GET", "")
		if err := s.list(w, r, actors[i]); err != nil || strings.Contains(w.Body.String(), "fixture-private-token") {
			t.Fatalf("list leaked a credential or failed: %d %v", i, err)
		}
		if strings.Contains(w.Body.String(), id) != (i != 2) || strings.Contains(w.Body.String(), "internal.example.test") != (i == 0) {
			t.Fatalf("invalid space visibility: %d %s", i, w.Body)
		}
	}
	w, r = request("PUT", strings.Replace(string(payload), "internal.example.test", "elsewhere.example.test", 1))
	if err := s.configure(w, r, actors[0]); err != web.Fail(409, "业务连接器已变更，请刷新。切换数据源需新建连接器") {
		t.Fatalf("duplicate connector allowed rebinding: %v", err)
	}
}

func TestSpaceSemanticViewsGuardDefaultFiltersAndRelations(t *testing.T) {
	db := connectorTestDB(t)
	space, actors := connectorSpace(t, db)
	id := uuid.NewString()
	op := []Operation{{Name: "daily", Description: "Daily records", Method: "GET", Path: "/daily", Risk: "read"}}
	if _, err := db.Exec(t.Context(), `insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,operations,maintainer)
	 values($1,$2,'internal','openapi_rest','openapi','Fixture','https://operations.example.test','service',$3,$4)`, id, space, op, actors[0].ID); err != nil {
		t.Fatal(err)
	}
	s := Server{DB: db, HTTP: businessFixture(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[
		 {"group_id":"Store A","returned":3,"sold":9,"scope":"final","private":"route credential"},
		 {"group_id":"Store A","returned":3,"sold":3,"scope":"draft"}
		]`))}, nil
	})}
	related, view := uuid.NewString(), uuid.NewString()
	fields := map[string]ViewField{"shop": {Source: "group_id", Label: "门店", Type: "string"}, "returns": {Source: "returned", Label: "退货量", Type: "integer"},
		"sales": {Source: "sold", Label: "销售量", Type: "integer"}, "status": {Source: "scope", Label: "统计状态", Type: "string"}}
	configuration := SemanticView{Connector: id, Name: "Sales", Description: "Approved data", Operation: "daily",
		Dimensions: fields, Sensitivity: "internal", Enabled: true}
	configure := func(view string, in SemanticView) {
		t.Helper()
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(raw))).WithContext(t.Context())
		r.SetPathValue("space", space)
		r.SetPathValue("view", view)
		if err := s.configureView(httptest.NewRecorder(), r, actors[0]); err != nil {
			t.Fatal(err)
		}
	}
	configure(related, configuration)
	configuration.Name = "Return rate"
	configuration.Relationships = []Relationship{{Dimension: "shop", RelatedView: related, RelatedDimension: "shop"}}
	configuration.Defaults = map[string]any{"status": "final"}
	configuration.Metrics = map[string]Metric{"return_rate": {Label: "退货率", Formula: "sum(returns) / sum(sales)"}}
	configure(view, configuration)
	list, err := s.Views(t.Context(), space, actors[1].ID, false)
	if err != nil || len(list) != 2 || list[0].Dimensions["shop"].Source != "" || len(list[0].Relationships) != 1 {
		t.Fatalf("member view visibility: %+v %v", list, err)
	}
	result, err := s.QueryView(t.Context(), space, actors[1].ID, ViewQuery{View: view, Dimensions: []string{"shop"}, Metrics: []string{"return_rate"}})
	if err != nil || len(result.Rows) != 1 || result.Rows[0]["return_rate"] != json.Number("0.33333333") || len(result.Rows[0]) != 2 {
		t.Fatalf("incorrect verified metric or extra fields: %+v %v", result, err)
	}
	standard := uuid.NewString()
	if _, err := db.Exec(t.Context(), `insert into enterprise_glossary(id,space_id,term,definition,metric_key,metric_formula,verified_by)
	 values($1,$2,'退货率','按百分比显示','return_rate','sum(returns) / sum(sales) * 100',$3)`, standard, space, actors[0].ID); err != nil {
		t.Fatal(err)
	}
	result, err = s.QueryView(t.Context(), space, actors[1].ID, ViewQuery{View: view, Dimensions: []string{"shop"}, Metrics: []string{"return_rate"}})
	if err != nil || len(result.Rows) != 1 || result.Rows[0]["return_rate"] != json.Number("33.33333333") ||
		result.Provenance.FormulaSources["return_rate"].ID != standard {
		t.Fatalf("approved glossary formula did not override the view default: %+v %v", result, err)
	}
	if _, err := s.QueryView(t.Context(), space, actors[1].ID, ViewQuery{View: view, Dimensions: []string{"shop"}, Filters: map[string]any{"status": "draft"}}); err != ErrBadQuery {
		t.Fatalf("mandatory metric filter was replaceable: %v", err)
	}
	if _, err := s.QueryView(t.Context(), space, actors[2].ID, ViewQuery{View: view, Dimensions: []string{"shop"}}); err == nil {
		t.Fatal("actor outside the space could read the view")
	}
}

package bizconnectors

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadSQLBindsValuesAndCapsRows(t *testing.T) {
	const query = "SELECT store_id, SUM(amount) AS total FROM warehouse.orders WHERE region = 'east' GROUP BY store_id ORDER BY total DESC LIMIT 999"
	for _, provider := range []string{"postgresql", "mysql", "starrocks", "clickhouse"} {
		t.Run(provider, func(t *testing.T) {
			statement, err := compileReadSQL(query, provider, []string{"warehouse.orders"}, 900)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(statement.Text, "east") || statement.Limit != 200 || len(statement.Args) != 2 ||
				statement.Args[0] != "east" || statement.Args[1] != int64(200) {
				t.Fatalf("values escaped binding or limit: %+v", statement)
			}
			if provider == "postgresql" && (!strings.Contains(statement.Text, `"warehouse"."orders"`) || !strings.Contains(statement.Text, "limit $2")) {
				t.Fatalf("invalid PostgreSQL query: %s", statement.Text)
			}
			if provider == "clickhouse" && (statement.HTTP["param_arg1"] != "east" || statement.HTTP["param_arg2"] != "200") {
				t.Fatalf("invalid ClickHouse bindings: %+v", statement)
			}
		})
	}
	for _, query := range []string{
		"SELECT * FROM orders; SELECT * FROM orders",
		"SELECT * FROM information_schema.tables",
		"SELECT * FROM orders CROSS JOIN items",
		"SELECT * FROM (SELECT * FROM orders) q",
		"UPDATE orders SET amount=0",
		"SELECT * FROM orders FOR UPDATE",
		"SELECT * FROM items",
	} {
		if _, err := compileReadSQL(query, "postgresql", []string{"orders", "information_schema.tables"}, 1); !errors.Is(err, ErrBadQuery) {
			t.Errorf("unexpected acceptance of %q: %v", query, err)
		}
	}
}

func TestOpenAPIImportsTypedOperationsWithExplicitWriteRisk(t *testing.T) {
	raw := `openapi: 3.0.3
paths:
  /orders/{order_id}:
    parameters:
      - name: order_id
        in: path
        required: true
        schema: {type: string}
    get:
      operationId: order_get
      x-alliance-result-path: [data]
    patch:
      operationId: order_update
      requestBody:
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Status'
components:
  schemas:
    Status:
      type: object
      properties:
        closed: {type: boolean}
      required: [closed]
`
	operations, err := importOperations([]byte(raw))
	if err != nil || len(operations) != 2 {
		t.Fatalf("operations=%+v, err=%v", operations, err)
	}
	if operations[0].Risk != "read" || operations[1].Risk != "write" || operations[1].Parameters["closed"].Type != "boolean" {
		t.Fatalf("invalid imported operations: %+v", operations)
	}
	connector := Connector{Operations: operations, Endpoint: "https://erp.example.test"}
	read, err := readOperation(connector, "order_get")
	if err != nil {
		t.Fatal(err)
	}
	request, err := restRequest(t.Context(), connector, Credentials{Token: "test-only"}, read, map[string]any{"order_id": "order-5"})
	if err != nil || request.URL.Path != "/orders/order-5" || request.Header.Get("Authorization") != "Bearer test-only" {
		t.Fatalf("failed typed request: %v", err)
	}
	if _, err := readOperation(connector, "order_update"); !errors.Is(err, ErrBadQuery) {
		t.Fatal("write operation was eligible for the read interface")
	}
	for _, input := range []string{strings.Replace(raw, "#/components/schemas/Status", "https://schemas.example.test/Status", 1),
		strings.Replace(raw, "type: boolean", "type: object", 1)} {
		if _, err := importOperations([]byte(input)); err == nil {
			t.Fatal("unsupported input accepted")
		}
	}
}

type businessRoundTrip func(*http.Request) (*http.Response, error)

func (f businessRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func businessFixture(roundTrip businessRoundTrip) func(context.Context, string) (*http.Client, error) {
	return func(context.Context, string) (*http.Client, error) {
		return &http.Client{Transport: roundTrip}, nil
	}
}

func TestCommerceReadProfiles(t *testing.T) {
	tests := []struct {
		provider, path, method, payload string
	}{
		{"douyin", "/order/searchList", "order.searchList", `{"code":10000,"data":{"shop_order_list":[{"order_id":"one"}]}}`},
		{"qianniu", "/router/rest", "taobao.trades.sold.get", `{"trades_sold_get_response":{"trades":{"trade":[{"order_id":"one"}]}}}`},
		{"jushuitan", "/api/open/query.aspx", "jushuitan.order.list.query", `{"code":0,"orders":[{"order_id":"one"}]}`},
	}
	for _, fixture := range tests {
		t.Run(fixture.provider, func(t *testing.T) {
			connector := Connector{Driver: "saas_pack", Category: "commerce", Provider: fixture.provider, Endpoint: "https://merchant.example.test", AuthMode: "service"}
			connector.Operations = commerceOperations(fixture.provider)
			s := Server{HTTP: businessFixture(func(req *http.Request) (*http.Response, error) {
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.URL.Path != fixture.path || req.Form.Get("method") != fixture.method || len(req.Form.Get("sign")) < 32 {
					t.Fatalf("unexpected provider route or absent signature: %s %s", req.URL.Path, req.Form.Get("method"))
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fixture.payload))}, nil
			})}
			rows, err := s.read(t.Context(), connector, Credentials{AppKey: "fixture-app", AppSecret: "fixture-key", Token: "fixture-token"}, ReadRequest{
				Operation: "orders", Limit: 1,
			})
			if err != nil || len(rows.Rows) != 1 || rows.Rows[0]["order_id"] != "one" {
				t.Fatalf("failed reading fixture: %+v %v", rows, err)
			}
		})
	}
}

func TestClickHouseReadOnlyProtocol(t *testing.T) {
	c := Connector{Driver: "readonly_sql", Provider: "clickhouse", Endpoint: "https://warehouse.example.test", Settings: Settings{
		Database: "warehouse", AllowedRelations: []string{"orders"},
	}}
	s := Server{HTTP: businessFixture(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query()
		raw, _ := io.ReadAll(r.Body)
		deadline, ok := r.Context().Deadline()
		if query.Get("readonly") != "1" || query.Get("max_execution_time") != "5" || query.Get("max_result_rows") != "3" ||
			query.Get("param_arg1") != "fixture" || !strings.Contains(string(raw), "FORMAT JSON") || strings.Contains(string(raw), "fixture") ||
			!ok || deadline.IsZero() {
			t.Fatalf("incorrect read-only request: %s %v", raw, query)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"total":3}]}`))}, nil
	})}
	result, err := s.read(t.Context(), c, Credentials{Username: "fixture"}, ReadRequest{Limit: 3, SQL: "SELECT COUNT(*) AS total FROM orders WHERE status='fixture'"})
	if err != nil || len(result.Rows) != 1 || result.Rows[0]["total"] != json.Number("3") {
		t.Fatalf("unexpected warehouse result: %+v %v", result, err)
	}
}

func TestERPCustomerProfiles(t *testing.T) {
	tests := []struct {
		provider, operation, path, payload, field string
		settings                                  map[string]string
	}{
		{"kingdee", "suppliers", kingdeeQuery, `[[1,"0001","Fixture","C"]]`, "FName", nil},
		{"yonyou", "inventory_batches", "/openapi/batches", `{"code":200,"data":{"records":[{"name":"Fixture"}]}}`, "name",
			map[string]string{"inventory_batches_path": "/openapi/batches"}},
		{"fxiaoke", "opportunities", fxiaokeQuery, `{"errorCode":0,"data":{"dataList":[{"name":"Fixture"}]}}`, "name", nil},
		{"salesforce", "customer_receipts", salesforceQuery, `{"records":[{"Name":"Fixture"}]}`, "Name",
			map[string]string{"customer_receipts_object": "Receipt__c", "customer_receipts_fields": "Id,Name"}},
		{"feishu_crm", "opportunities", "/open-apis/bitable/v1/apps/app123/tables/tbl123/records",
			`{"code":0,"data":{"items":[{"record_id":"rec123","fields":{"name":"Fixture"}}]}}`, "name",
			map[string]string{"app": "app123", "opportunities_table": "tbl123"}},
	}
	for _, fixture := range tests {
		t.Run(fixture.provider, func(t *testing.T) {
			family, operations := saasProfile(fixture.provider)
			connector := Connector{Driver: "saas_pack", Category: family, Provider: fixture.provider, Operations: operations,
				Endpoint: "https://business.example.test", Settings: Settings{Resources: fixture.settings}}
			s := Server{HTTP: businessFixture(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != fixture.path {
					t.Fatalf("unexpected provider path: %s", req.URL.Path)
				}
				if fixture.provider == "salesforce" && req.URL.Query().Get("q") != "SELECT Id,Name FROM Receipt__c LIMIT 1" {
					t.Fatal("Salesforce query ignored approved fields")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fixture.payload))}, nil
			})}
			result, err := s.read(t.Context(), connector, Credentials{Token: "synthetic", Account: "fixture"}, ReadRequest{Operation: fixture.operation, Limit: 1})
			if err != nil || len(result.Rows) != 1 || result.Rows[0][fixture.field] != "Fixture" {
				t.Fatalf("invalid provider response: %+v %v", result, err)
			}
		})
	}
}

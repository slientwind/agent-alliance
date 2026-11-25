package bizconnectors

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var warehouseDialects = []string{"postgresql", "mysql", "starrocks", "clickhouse"}

var readOnlyCases = []struct {
	name string
	sql  string
}{
	{"multiple_selects", "SELECT order_id FROM orders; SELECT item_id FROM items"},
	{"multiple_aggregates", "SELECT count(*) FROM orders; SELECT count(*) FROM items"},
	{"information_schema", "SELECT count(*) FROM information_schema.tables"},
	{"pg_catalog", "SELECT count(*) FROM pg_catalog.pg_class"},
	{"system_relation", "SELECT count(*) FROM system.tables"},
	{"cartesian_join", "SELECT count(*) FROM orders CROSS JOIN items"},
	{"comma_join", "SELECT count(*) FROM orders, items"},
	{"ordinary_join", "SELECT count(*) FROM orders JOIN items ON orders.order_id=items.order_id"},
}

var readOnlyRelations = []string{"orders", "items", "information_schema.tables", "pg_catalog.pg_class", "system.tables"}

const approvedRead = "SELECT count(*) AS count FROM orders WHERE category = 'fiction;review'"

func TestWarehouseReadOnlyRejectionMatrix(t *testing.T) {
	for _, provider := range warehouseDialects {
		t.Run(provider, func(t *testing.T) {
			for _, fixture := range readOnlyCases {
				t.Run(fixture.name, func(t *testing.T) {
					if _, err := compileReadSQL(fixture.sql, provider, readOnlyRelations, 20); !errors.Is(err, ErrBadQuery) {
						t.Fatalf("read-only guard returned %v, expected rejection", err)
					}
				})
			}
			query, err := compileReadSQL(approvedRead, provider, readOnlyRelations, 20)
			if err != nil || len(query.Args) != 2 || query.Args[0] != "fiction;review" || query.Limit != 20 {
				t.Fatalf("allowed query did not preserve bound values and limit: %+v %v", query, err)
			}
		})
	}
}

func BenchmarkWarehouseReadOnlyGuard(b *testing.B) {
	for _, provider := range warehouseDialects {
		b.Run(provider+"/rejected", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(work *testing.PB) {
				index := 0
				for work.Next() {
					if _, err := compileReadSQL(readOnlyCases[index%len(readOnlyCases)].sql, provider, readOnlyRelations, 20); !errors.Is(err, ErrBadQuery) {
						b.Error("read-only guard accepted a blocked fixture")
					}
					index++
				}
			})
		})
		b.Run(provider+"/approved", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(work *testing.PB) {
				for work.Next() {
					if _, err := compileReadSQL(approvedRead, provider, readOnlyRelations, 20); err != nil {
						b.Errorf("warehouse guard rejected an approved fixture: %v", err)
					}
				}
			})
		})
	}
}

type isolationEmployee struct {
	id, grant, number, token string
	total                    int
}

type isolationTenant struct {
	space, view, host string
	employees         [2]isolationEmployee
}

func TestConcurrentTenantOBOIsolation(t *testing.T) {
	db := connectorTestDB(t)
	ctx := t.Context()
	vault, err := resources.NewVault(strings.Repeat("c8", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}

	tenants := make([]isolationTenant, 2)
	hosts := make(map[string]isolationTenant)
	for index := range tenants {
		space, members := connectorSpace(t, db)
		connector, department := uuid.NewString(), uuid.NewString()
		tenant := isolationTenant{space: space, view: uuid.NewString(), host: fmt.Sprintf("warehouse-%d.example.test", index)}
		exec("insert into departments(id,name) values($1,$1::uuid::text)", department)
		exec(`insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,settings,maintainer)
		 values($1,$2,'warehouse','readonly_sql','clickhouse','Orders Warehouse',$3,'obo',$4,$5)`,
			connector, space, "https://"+tenant.host, Settings{Database: "warehouse", AllowedRelations: []string{"orders"}}, members[0].ID)
		exec(`insert into semantic_data_views(id,connector_id,space_id,view_name,business_desc,relation_name,
		 allowed_dimensions,verified_metrics_json,rls_column_mapping,verified_by)
		 values($1,$2,$3,'Order counts','Current employee within their department','orders',$4,$5,$6,$7)`,
			tenant.view, connector, space, map[string]ViewField{"amount": {Source: "amount", Type: "integer", Label: "Amount"}},
			map[string]Metric{"total": {Label: "Total", Formula: "sum(amount)"}}, map[string]string{"employee_no": "employee_code", "dept_id": "department_code"}, members[0].ID)
		for position := range tenant.employees {
			employee := isolationEmployee{id: members[position].ID, grant: uuid.NewString(),
				number: fmt.Sprintf("E%d", position+1), token: fmt.Sprintf("delegated-fixture-%d-%d", index, position), total: 100*(index+1) + position}
			exec("update members set department_id=$1 where id=$2", department, employee.id)
			exec(`insert into business_obo_identities(connector_id,member_id,department_id,issuer_subject,employee_no,dept_code,verified_by)
			 values($1,$2,$3,$4,$4,'D1',$5)`, connector, employee.id, department, employee.number, members[0].ID)
			cipher, err := vault.Seal(employee.token, oboCipherScope(connector, employee.id)+"/"+employee.grant)
			if err != nil {
				t.Fatal(err)
			}
			exec(`insert into business_obo_grants(connector_id,member_id,grant_id,connector_revision,identity_revision,token_cipher,expires_at)
			 values($1,$2,$3,1,1,$4,clock_timestamp()+interval '15 minutes')`, connector, employee.id, employee.grant, cipher)
			tenant.employees[position] = employee
		}
		tenants[index] = tenant
		hosts[tenant.host] = tenant
	}

	var reads atomic.Int32
	s := Server{DB: db, Vault: vault, HTTP: businessFixture(func(r *http.Request) (*http.Response, error) {
		reads.Add(1)
		tenant, ok := hosts[r.URL.Hostname()]
		user, token, authenticated := r.BasicAuth()
		params := r.URL.Query()
		body, err := io.ReadAll(r.Body)
		if !ok || !authenticated || err != nil || params.Get("param_arg1") != user || params.Get("param_arg2") != "D1" ||
			!strings.Contains(string(body), "`employee_code` = {arg1:String} and `department_code` = {arg2:String}") {
			return nil, errors.New("warehouse request lacked its bounded department and employee parameters")
		}
		for _, employee := range tenant.employees {
			if user == employee.number && token == employee.token {
				// The fixture returns only the aggregate for the authenticated warehouse and row identity.
				result := fmt.Sprintf(`{"data":[{"_measure_0":%d}]}`, employee.total)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(result))}, nil
			}
		}
		return nil, errors.New("delegated warehouse credentials crossed employees or tenants")
	})}

	const workers, repeats = 32, 4
	begin := make(chan struct{})
	var sessions sync.WaitGroup
	for worker := range workers {
		sessions.Go(func() {
			tenant, foreign := tenants[worker%2], tenants[(worker+1)%2]
			employee := tenant.employees[(worker/2)%2]
			query := ViewQuery{View: tenant.view, Metrics: []string{"total"}}
			foreignQuery := ViewQuery{View: foreign.view, Metrics: []string{"total"}}
			<-begin
			for range repeats {
				result, err := s.QueryView(ctx, tenant.space, employee.id, query)
				if err != nil || len(result.Rows) != 1 || result.Rows[0]["total"] != json.Number(fmt.Sprint(employee.total)) ||
					!result.Provenance.ActorOnly || result.Provenance.OBOGrant != employee.grant || result.Provenance.View != tenant.view {
					t.Errorf("worker %d did not receive its warehouse total and pinned authorization: %+v %v", worker, result, err)
				}
				for _, scope := range []string{tenant.space, foreign.space} {
					result, err := s.QueryView(ctx, scope, employee.id, foreignQuery)
					if !errors.Is(err, pgx.ErrNoRows) || len(result.Rows) != 0 {
						t.Errorf("worker %d read a foreign warehouse view: %+v %v", worker, result, err)
					}
				}
			}
		})
	}
	started := time.Now()
	close(begin)
	sessions.Wait()
	if reads.Load() != workers*repeats {
		t.Fatalf("got %d warehouse calls, expected only the %d authorized calls", reads.Load(), workers*repeats)
	}
	t.Logf("%d concurrent sessions: %d authorized reads, %d foreign-view rejections in %s",
		workers, workers*repeats, 2*workers*repeats, time.Since(started).Round(time.Millisecond))
}

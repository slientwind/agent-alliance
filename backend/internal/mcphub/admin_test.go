package mcphub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMCPAdminSecretsAndRevisionPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
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
	issueSession := func(role string) string {
		t.Helper()
		id, token := uuid.NewString(), web.Token()
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'测试成员','unused',$3)", id, id+"@example.invalid", role)
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(token), id)
		return token
	}
	admin, employee := issueSession("admin"), issueSession("member")
	vault, err := resources.NewVault(strings.Repeat("bd", 32))
	if err != nil {
		t.Fatal(err)
	}
	routes := http.NewServeMux()
	Admin{DB: db, Client: Client{Vault: vault, Egress: egress.Policy{DB: db}}}.Routes(routes, identity.Server{DB: db})
	base, service := "/api/admin/mcp/servers", uuid.NewString()
	request := func(method, suffix, token string, payload any, want int) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, base+suffix, bytes.NewReader(raw)).WithContext(ctx)
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status = %d, want %d, response = %s", w.Code, want, w.Body)
		}
		return w
	}
	for _, host := range []string{"mcp-one.example", "mcp-two.example"} {
		exec("insert into outbound_host_allowlist(host,port) values($1,443) on conflict do nothing", host)
	}
	input := map[string]any{"name": "企业知识", "endpoint": "https://mcp-one.example", "transport": StreamableHTTP,
		"enabled": true, "expected": 0, "bearer": "fixture-key", "tools": map[string]string{"lookup": "read_only", "publish": "write"}}
	request(http.MethodPut, "/"+service, employee, input, 403)
	request(http.MethodPut, "/"+service, admin, input, 200)
	request(http.MethodPut, "/"+service, admin, input, 409)
	response := request(http.MethodGet, "", admin, nil, 200)
	var public []struct {
		ID            string
		HasCredential bool
	}
	if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil || strings.Contains(response.Body.String(), "fixture-key") {
		t.Fatalf("unexpected public credential field: %s", response.Body)
	}
	visible := false
	for _, item := range public {
		if item.ID == service {
			visible = item.HasCredential
		}
	}
	if !visible {
		t.Fatal("operator could not see whether the server had a credential")
	}
	var cipher []byte
	err = db.QueryRow(ctx, "select encrypted_bearer from mcp_servers where id=$1", service).Scan(&cipher)
	if err != nil || bytes.Contains(cipher, []byte("fixture-key")) {
		t.Fatalf("credential is missing or unencrypted: %v", err)
	}
	if got, err := vault.Open(cipher, bearerScope(service)); err != nil || got != "fixture-key" {
		t.Fatal("Vault key was stored under the wrong scope")
	}
	if _, err := vault.Open(cipher, bearerScope(uuid.NewString())); err == nil {
		t.Fatal("MCP credential was reusable by another resource")
	}
	delete(input, "bearer")
	input["expected"], input["endpoint"] = 1, "https://mcp-blocked.example"
	request(http.MethodPut, "/"+service, admin, input, 422)
	input["endpoint"] = "https://mcp-two.example"
	request(http.MethodPut, "/"+service, admin, input, 200)
	var missing bool
	if err = db.QueryRow(ctx, "select encrypted_bearer is null from mcp_servers where id=$1", service).Scan(&missing); err != nil || !missing {
		t.Fatal("changing the endpoint retained a prior destination credential")
	}
}

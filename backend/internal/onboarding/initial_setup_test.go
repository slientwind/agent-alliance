package onboarding

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInitialSetupPersistsSixTiersAndReplaysSingleUseInvites(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("b7", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, admin, colleague := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into enterprise(name,default_team) values('Fixture',$1)", team)
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','admin'),($3,$4,'Member','','member')",
		admin, admin+"@example.test", colleague, colleague+"@example.test")
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, admin, colleague)
	actor := identity.Member{ID: admin}
	server := Server{Catalog: resources.Catalog{DB: db, Vault: vault}}
	provider := llmgateway.InitialProvider{Name: "Fixture provider", Kind: "openai", Endpoint: "https://setup-fixture.example.test/v1", Key: "private-fixture-provider"}
	for i, candidate := range []struct {
		name string
		caps []string
	}{
		{"chat-fixture", []string{"text", "tool_call", "reasoning", "vision"}},
		{"image-fixture", []string{"image"}},
		{"vector-fixture", []string{"embedding"}},
	} {
		provider.Models = append(provider.Models, llmgateway.InitialModel{
			Name: candidate.name, Capabilities: candidate.caps, ContextTokens: 8192, MaxOutputTokens: 1000,
			InputPerMillion: "1000029", OutputPerMillion: "2000043", CachedPerMillion: "0", PerImage: []string{"0", "320731", "0"}[i],
		})
	}
	setup := func(member identity.Member, key string, sources []string, invites, expected int) string {
		t.Helper()
		requestBody, err := json.Marshal(map[string]any{"provider": provider, "sources": sources, "inviteCount": invites})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/admin/onboarding", strings.NewReader(string(requestBody)))
		req.Header.Set("Idempotency-Key", key)
		out := httptest.NewRecorder()
		err = server.initialSetup(out, req, member)
		var issue web.Problem
		if errors.As(err, &issue) {
			if issue.Status != expected {
				t.Fatalf("unexpected setup error: %v", err)
			}
			return issue.Message
		}
		if err != nil || out.Code != expected {
			t.Fatalf("setup status %d, response %s, error %v", out.Code, out.Body, err)
		}
		return out.Body.String()
	}
	setup(identity.Member{ID: colleague}, "not-authorized", []string{"mcp"}, 2, 403)
	setup(actor, "bad-source", []string{"unapproved"}, 2, 422)
	var providers int
	if err := db.QueryRow(ctx, "select count(*) from model_providers").Scan(&providers); err != nil || providers != 0 {
		t.Fatalf("invalid setup partially persisted a provider: %d, %v", providers, err)
	}
	output := setup(actor, "setup", []string{"mcp", "anthropic"}, 2, 201)
	if duplicate := setup(actor, "setup", []string{"mcp", "anthropic"}, 2, 200); duplicate != output {
		t.Fatalf("idempotent retry lost invitation receipt: %s", duplicate)
	}
	setup(actor, "setup", []string{"mcp", "anthropic"}, 1, 409)
	var receipt initialSetupReceipt
	if err := json.Unmarshal([]byte(output), &receipt); err != nil || !slices.Equal(receipt.Tiers,
		[]llmgateway.Tier{llmgateway.Default, llmgateway.Fast, llmgateway.Reasoning, llmgateway.Vision, llmgateway.Image, llmgateway.Embedding}) ||
		len(receipt.Invitations) != 2 || receipt.Invitations[0] == receipt.Invitations[1] || strings.Contains(output, provider.Key) {
		t.Fatalf("incorrect tier or invite receipt %q, %v", output, err)
	}
	for _, tier := range receipt.Tiers {
		candidates, err := (llmgateway.Catalog{DB: db}).ModelsForTier(ctx, tier, receipt.Expires)
		if err != nil || len(candidates) != 1 || candidates[0].Price.InputPerMillion != 1000029 || candidates[0].Price.OutputPerMillion != 2000043 {
			t.Fatalf("tier %s did not resolve its exact price and model: %v", tier, err)
		}
	}
	var active, selected, entries, unsafe int
	err = db.QueryRow(ctx, `select (select count(*) from invites where team_id=$1 and used_by is null and expires_at>now()),
	 (select count(*) from ecosystem_sources where enabled and schedule_sync='pending'),
	 (select count(*) from outbound_host_allowlist where host in ('registry.modelcontextprotocol.io','api.github.com','setup-fixture.example.test')),
	 (select count(*) from initial_workspace_setup where position(convert_to($2,'UTF8') in receipt_cipher)>0)`, team, receipt.Invitations[0]).
		Scan(&active, &selected, &entries, &unsafe)
	if err != nil || active != 2 || selected != 2 || entries != 3 || unsafe != 0 {
		t.Fatalf("expected selected sources and encrypted invitations: %d/%d/%d/%d %v", active, selected, entries, unsafe, err)
	}
}

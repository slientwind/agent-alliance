package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDatabaseBotConfigurationOverridesDeploySettings(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	t.Setenv("PUBLIC_BASE_URL", "https://workspace.example.test")
	t.Setenv("SLACK_TEAM_ID", "TLEGACY")
	t.Setenv("SLACK_APP_ID", "ALEGACY")
	t.Setenv("SLACK_SIGNING_SECRET", "deploy-signing-fixture")
	t.Setenv("SLACK_BOT_TOKEN", "deploy-token-fixture")
	ctx := t.Context()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("db", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, "delete from im_bot_settings where provider='slack'"); err != nil {
		t.Fatal(err)
	}
	admin, member := uuid.NewString(), uuid.NewString()
	for id, role := range map[string]string{admin: "admin", member: "member"} {
		if _, err := db.Exec(ctx, "insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','',$3)", id, id+"@example.test", role); err != nil {
			t.Fatal(err)
		}
	}
	manager := Bots{DB: db, Vault: vault}
	if state, err := manager.load(ctx); err != nil || !state.slack.configured() || state.slack.key != "slack/TLEGACY/ALEGACY" {
		t.Fatalf("existing deploy bot unavailable: %v", err)
	}
	invoke := func(action identity.Action, status int, actor, body string) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/", strings.NewReader(body))
		r.SetPathValue("provider", "slack")
		w := httptest.NewRecorder()
		err := action(w, r, identity.Member{ID: actor})
		var problem web.Problem
		if errors.As(err, &problem) && problem.Status == status {
			return problem.Message
		}
		if err != nil || w.Code != status {
			t.Fatalf("got %d; expected %d: %v %s", w.Code, status, err, w.Body)
		}
		return w.Body.String()
	}
	initial := `{"expected":0,"enabled":true,"fields":{"tenant":"TDB","app":"ADB"},"credentials":{"secret":"first-signature","verify":"only-database-token"}}`
	invoke(manager.configure, 403, member, initial)
	invoke(manager.configure, 200, admin, initial)
	invoke(manager.configure, 409, admin, initial)
	if body := invoke(manager.list, 200, admin, ""); strings.Contains(body, "first-signature") || strings.Contains(body, "only-database-token") ||
		strings.Contains(body, "deploy-signing-fixture") || !strings.Contains(body, `"hasCredentials":true`) {
		t.Fatal("admin response exposed a credential or omitted its state")
	}
	var cipher []byte
	if err := db.QueryRow(ctx, "select encrypted_credentials from im_bot_settings where provider='slack'").Scan(&cipher); err != nil ||
		strings.Contains(string(cipher), "first-signature") || strings.Contains(string(cipher), "only-database-token") {
		t.Fatal("bot credentials were not encrypted")
	}
	if _, err := vault.Open(cipher, botScope("feishu")); err == nil {
		t.Fatal("bot ciphertext unexpectedly opened in a different channel")
	}
	check := func(secret string, want bool) {
		t.Helper()
		current, err := manager.load(ctx)
		if err != nil || current.slack.key != "slack/TDB/ADB" {
			t.Fatalf("saved channel identity was not used: %v", err)
		}
		body := `{"type":"url_verification","challenge":"fixture"}`
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte("v0:" + stamp + ":" + body))
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
		request.Header.Set("X-Slack-Request-Timestamp", stamp)
		request.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		err = current.slack.event(w, request)
		if (err == nil && w.Code == http.StatusOK && strings.Contains(w.Body.String(), "fixture")) != want {
			t.Fatalf("unexpected signed callback outcome, expected %v, got %d %v %s", want, w.Code, err, w.Body)
		}
	}
	check("first-signature", true)
	check("deploy-signing-fixture", false)
	invoke(manager.configure, 422, admin, `{"expected":1,"enabled":true,"fields":{"tenant":"TOTHER","app":"ADB"}}`)
	invoke(manager.configure, 200, admin, `{"expected":1,"enabled":true,"fields":{"tenant":"TDB","app":"ADB"},"credentials":{"secret":"rotated-signature","verify":"new-token"}}`)
	check("first-signature", false)
	check("rotated-signature", true)
	invoke(manager.configure, 200, admin, `{"expected":2,"enabled":false,"fields":{"tenant":"TDB","app":"ADB"}}`)
	check("rotated-signature", false)
	check("deploy-signing-fixture", false)
	var leaks int
	if err := db.QueryRow(ctx, `select count(*) from audit_events where actor=$1 and
		(detail::text like '%rotated-signature%' or detail::text like '%only-database-token%')`, admin).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatal("credential recorded in audit")
	}
}

package eventtriggers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEventHMACFiltersDeduplicatesAndCoalescesConcurrentHits(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
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
	vault, err := resources.NewVault(strings.Repeat("c7", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	admin, team, trigger := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','Event owner','','member')", admin)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, admin)
	var space string
	if err := db.QueryRow(ctx, "select id from spaces where team_id=$1", team).Scan(&space); err != nil {
		t.Fatal(err)
	}
	exec("update space_members set role='space_admin' where (space_id,member_id)=($1,$2)", space, admin)
	s := Server{DB: db, Vault: vault}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"name":"Low stock","filter":"event.quantity < 4",
	 "dedupKey":"event.id","cooldownSeconds":300,"eventFields":["quantity"],"enabled":true}`)).WithContext(ctx)
	r.SetPathValue("space", space)
	r.SetPathValue("trigger", trigger)
	if err := s.configure(w, r, identity.Member{ID: admin}); err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		WebhookPath string
		Secret      string
	}
	if json.Unmarshal(w.Body.Bytes(), &configuration) != nil || configuration.Secret == "" ||
		!strings.HasPrefix(configuration.WebhookPath, "/api/webhooks/events/") {
		t.Fatalf("missing provisioning details: %s", w.Body)
	}
	token := strings.TrimPrefix(configuration.WebhookPath, "/api/webhooks/events/")
	post := func(body, signBody string, ts time.Time) (string, error) {
		stamp := strconv.FormatInt(ts.Unix(), 10)
		mac := hmac.New(sha256.New, []byte(configuration.Secret))
		mac.Write([]byte(stamp + "." + signBody))
		r := httptest.NewRequest(http.MethodPost, configuration.WebhookPath, strings.NewReader(body)).WithContext(ctx)
		r.SetPathValue("token", token)
		r.Header.Set("X-Alliance-Timestamp", stamp)
		r.Header.Set("X-Alliance-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		err := s.ingress(w, r)
		return w.Body.String(), err
	}
	normal := `{"id":"state","quantity":10}`
	if status, err := post(normal, normal, time.Now()); err != nil || !strings.Contains(status, "ignored") {
		t.Fatalf("CEL filter did not suppress non-matching event: %s %v", status, err)
	}
	if _, err := post(normal, normal, time.Now().Add(-15*time.Minute)); err != web.Fail(403, "业务事件签名无效") {
		t.Fatalf("stale signature accepted: %v", err)
	}
	if _, err := post(normal, `{"id":"state","quantity":1}`, time.Now()); err != web.Fail(403, "业务事件签名无效") {
		t.Fatalf("modified body accepted: %v", err)
	}
	var group sync.WaitGroup
	for i := range 24 {
		group.Go(func() {
			body := fmt.Sprintf(`{"id":"evt%d","quantity":1,"employeeEmail":"fixture.private@example.test"}`, i%12)
			if result, err := post(body, body, time.Now()); err != nil || (!strings.Contains(result, "accepted") && !strings.Contains(result, "duplicate")) {
				t.Errorf("signed concurrent event failed: %s %v", result, err)
			}
		})
	}
	group.Wait()
	var batches, hits, eventCount int
	if err := db.QueryRow(ctx, `select count(*),sum(hits),(select count(*) from event_trigger_deliveries where trigger_id=$1)
	 from event_trigger_batches where trigger_id=$1`, trigger).Scan(&batches, &hits, &eventCount); err != nil || batches != 1 || hits != 12 || eventCount != 12 {
		t.Fatalf("storm coalescing or deduplication failed: %d %d %d %v", batches, hits, eventCount, err)
	}
	var batchID string
	var cipher []byte
	if err := db.QueryRow(ctx, "select id,sample_cipher from event_trigger_batches where trigger_id=$1", trigger).Scan(&batchID, &cipher); err != nil {
		t.Fatal(err)
	}
	if sample, err := vault.Open(cipher, sampleScope(batchID)); err != nil || sample != `{"quantity":1}` {
		t.Fatalf("unapproved event details entered the task payload: %s %v", sample, err)
	}
	exec("update event_trigger_batches set ready_at=clock_timestamp()-interval '1 second' where id=$1", batchID)
	next := `{"id":"next","quantity":2}`
	if result, err := post(next, next, time.Now()); err != nil || !strings.Contains(result, "accepted") {
		t.Fatalf("event after cooldown not accepted: %s %v", result, err)
	}
	if err := db.QueryRow(ctx, "select count(*) from event_trigger_batches where trigger_id=$1", trigger).Scan(&batches); err != nil || batches != 2 {
		t.Fatalf("new cooldown started no batch: count %d %v", batches, err)
	}
}

package sandbox

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSandboxPoolQuotaAndExpiredLeasesPostgres(t *testing.T) {
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
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("delete from sandbox_leases")
	exec("update sandbox_pool set revision=1,max_jobs=2,timeout_seconds=60")
	defer exec("update sandbox_pool set revision=1,max_jobs=2,timeout_seconds=60")
	admin := uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','admin')", admin, admin+"@example.test")
	pool := Pool{DB: db}
	save := func(raw string, status int) {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/", strings.NewReader(raw))
		w := httptest.NewRecorder()
		err := pool.configure(w, r, identity.Member{ID: admin})
		var problem web.Problem
		if errors.As(err, &problem) && problem.Status == status {
			return
		}
		if err != nil || w.Code != status {
			t.Fatalf("quota returned %d instead of %d: %v", w.Code, status, err)
		}
	}
	save(`{"expected":1,"maxJobs":1,"timeoutSeconds":12}`, 200)
	save(`{"expected":1,"maxJobs":2,"timeoutSeconds":12}`, 409)
	save(`{"expected":2,"maxJobs":1,"timeoutSeconds":65}`, 422)
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			id, timeout, err := pool.acquire(ctx)
			if err == nil {
				if timeout != 12*time.Second {
					t.Errorf("wrong execution timeout %s", timeout)
				}
				ids <- id
			} else if !errors.Is(err, ErrPoolFull) {
				t.Errorf("unexpected lease failure %v", err)
			}
		})
	}
	wg.Wait()
	close(ids)
	active := []string{}
	for id := range ids {
		active = append(active, id)
	}
	if len(active) != 1 {
		t.Fatalf("expected one permit, got %d", len(active))
	}
	exec("update sandbox_leases set expires_at=clock_timestamp()-interval '1 second' where id=$1", active[0])
	missing := filepath.Join(t.TempDir(), "missing")
	if err := pool.Run(ctx, missing, filepath.Join(t.TempDir(), "results")); err == nil || errors.Is(err, ErrPoolFull) {
		t.Fatalf("stale permit was not released or runner did not execute: %v", err)
	}
	var count int
	if err := db.QueryRow(ctx, "select count(*) from sandbox_leases").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed sandbox run leaked a permit: %d %v", count, err)
	}
	save(`{"expected":2,"maxJobs":0,"timeoutSeconds":12}`, 200)
	if _, _, err := pool.acquire(ctx); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("paused pool accepted a new task: %v", err)
	}
}

package knowledge

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAttachmentAtUploadLimitCanBePreviewedAndExtractedPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
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
	member, team, project, session := uuid.NewString(), uuid.NewString(), uuid.NewString(), web.Token()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','attachment fixture','','member')", member)
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'attachment fixture')", project, member, team)
	exec("insert into project_memberships(project_id,member_id) values($1,$2)", project, member)
	exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(session), member)
	var attempts atomic.Int32
	parser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size, err := io.Copy(io.Discard, r.Body)
		if r.URL.Path != "/extract/pdf" || r.Header.Get("Authorization") != "Bearer attachment-fixture" || err != nil || size != AttachmentLimit {
			t.Errorf("invalid original sent to extractor: path=%s size=%d err=%v", r.URL.Path, size, err)
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{"state":"ready","segments":[{"location":"Page 1","text":"Extracted report"}]}`)
	}))
	defer parser.Close()
	t.Setenv("PREVIEW_URL", parser.URL)
	t.Setenv("CONNECTOR_TOKEN", "attachment-fixture")
	mux := http.NewServeMux()
	Server{DB: db, Objects: Objects{Root: t.TempDir()}}.Routes(mux, identity.Server{DB: db})
	base := "/api/projects/" + project + "/sources"
	call := func(method, path string, body io.Reader, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, path, body)
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: session})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got HTTP %d, want %d", method, path, w.Code, status)
		}
		return w
	}
	raw := make([]byte, AttachmentLimit)
	copy(raw, "%PDF-1.7\n"+project)
	w := call(http.MethodPost, base+"?name=report.pdf", bytes.NewReader(raw), 201)
	var source struct {
		ID, Extraction string
		Version        int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil || source.ID == "" || source.Extraction != "unavailable" || source.Version != 1 {
		t.Fatalf("invalid upload receipt: %s (%v)", w.Body, err)
	}
	path := base + "/" + source.ID
	preview := call(http.MethodGet, path+"/content?preview=1", nil, 200)
	if preview.Header().Get("Content-Type") != "application/pdf" || !bytes.Equal(preview.Body.Bytes(), raw) {
		t.Fatal("preview did not return the stored attachment")
	}
	retry := call(http.MethodPost, path+"/extract", strings.NewReader(`{"version":1}`), 200)
	if !strings.Contains(retry.Body.String(), `"readable":true`) || attempts.Load() != 2 {
		t.Fatalf("reattempt did not extract the large attachment: %s", retry.Body)
	}
	call(http.MethodPost, base+"?name=too-large.bin", io.MultiReader(bytes.NewReader(raw), strings.NewReader("x")), 413)
}

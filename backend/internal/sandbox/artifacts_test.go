package sandbox

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSandboxArtifactVersionsAndPreviewsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
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
	project, owner, team, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'任务发起人','unused','member')", owner, owner+"@example.invalid")
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,stage,plan_version) values($1,$2,$3,$2,'测试产物','review',1)", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, owner)
	exec("insert into plans(project_id,version,brief) values($1,1,'测试任务')", project)
	exec(`insert into runs(id,project_id,actor,charge_team,plan_version,status,finished_at,request_key,request_hash,max_calls)
		values($1,$2,$3,$4,1,'completed',now(),$5,digest('test','sha256'),1)`, run, project, owner, team, run)
	token := web.Token()
	exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(token), owner)
	outputs := t.TempDir()
	objects := knowledge.Objects{Root: t.TempDir()}
	csvName, wordName := "采购.csv", "说明.docx"
	csv := "商品,金额\n测试,2\n"
	if err := os.WriteFile(filepath.Join(outputs, csvName), []byte(csv), 0600); err != nil {
		t.Fatal(err)
	}
	var word bytes.Buffer
	archive := zip.NewWriter(&word)
	for _, entry := range []string{"[Content_Types].xml", "word/document.xml"} {
		part, err := archive.Create(entry)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, "<fixture/>"); err != nil {
			t.Fatal(err)
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outputs, wordName), word.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	collect := func() []PublishedArtifact {
		t.Helper()
		result, err := ImportArtifacts(ctx, db, objects, run, outputs)
		if err != nil || len(result) != 2 {
			t.Fatalf("expected two artifacts: %v %v", result, err)
		}
		return result
	}
	first := collect()
	if repeated := collect(); !reflect.DeepEqual(first, repeated) {
		t.Fatalf("identical receipt changed artifact versions: %v / %v", first, repeated)
	}
	if err := os.WriteFile(filepath.Join(outputs, csvName), []byte(csv+"续行,3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next := collect()
	var doc, table PublishedArtifact
	for _, item := range next {
		if item.Name == wordName {
			doc = item
		}
		if item.Name == csvName {
			table = item
		}
	}
	digest := sha256.Sum256([]byte(csv + "续行,3\n"))
	if doc.Version != 1 || table.Version != 2 || table.Hash != hex.EncodeToString(digest[:]) {
		t.Fatalf("wrong imported file versions or digests: %v", next)
	}
	const rendered = "%PDF-1.4\n% mock preview\n"
	var conversions atomic.Int32
	preview := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/convert/docx" || !bytes.Equal(got, word.Bytes()) || r.Header.Get("Authorization") != "Bearer fixture" {
			w.WriteHeader(422)
			return
		}
		conversions.Add(1)
		_, _ = io.WriteString(w, rendered)
	}))
	defer preview.Close()
	t.Setenv("PREVIEW_URL", preview.URL)
	t.Setenv("CONNECTOR_TOKEN", "fixture")
	work, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		objects.ServeArtifactPreviews(work, db)
	}()
	defer func() { stop(); <-done }()
	for {
		var ready int
		err = db.QueryRow(ctx, `select count(*) from artifact_preview_jobs where artifact_id in ($1,$2) and state='ready'`, doc.ID, table.ID).Scan(&ready)
		if err != nil {
			t.Fatal(err)
		}
		if ready == 3 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	routes := http.NewServeMux()
	knowledge.Server{DB: db, Objects: objects}.Routes(routes, identity.Server{DB: db})
	getPreview := func(want int) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/projects/"+project+"/artifacts/"+doc.ID+"/file?version=1&preview=1", nil).WithContext(ctx)
		request.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		delivery := httptest.NewRecorder()
		routes.ServeHTTP(delivery, request)
		if delivery.Code != want {
			t.Fatalf("preview HTTP %d, want %d: %s", delivery.Code, want, delivery.Body)
		}
		return delivery
	}
	response := getPreview(200)
	if response.Body.String() != rendered || response.Header().Get("Content-Type") != "application/pdf" || conversions.Load() != 1 {
		t.Fatal("artifact preview was not served from its committed version cache")
	}
	exec("delete from project_memberships where project_id=$1 and member_id=$2", project, owner)
	getPreview(404)
	if _, err = ImportArtifacts(ctx, db, objects, run, outputs); err == nil {
		t.Fatal("revoked project membership could still publish artifacts")
	}
}

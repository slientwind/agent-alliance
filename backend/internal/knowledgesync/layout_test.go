package knowledgesync

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestKnowledgeParserPublishesStructuredChunksAtomically(t *testing.T) {
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
	objects := knowledge.Objects{Root: t.TempDir()}
	s := Server{DB: db, Objects: objects}
	actor, collection, document, invalid := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	defer db.Exec(context.Background(), "delete from kb_collections where id=$1", collection)
	const input = "<h1>采购</h1><p>合规采购至少三家比价</p>"
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into members(id,email,display_name,password_hash,role)
		 values($1,$1::uuid::text||'@example.test','Parser','','member')`, actor); err != nil {
			return err
		}
		var space string
		if err := tx.QueryRow(ctx, "select id from spaces where owner_member_id=$1", actor).Scan(&space); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into kb_collections(id,space_id,name,sync_provider,acl_mode,maintainer)
		 values($1,$2,'Fixture','manual','space',$3)`, collection, space, actor); err != nil {
			return err
		}
		hash, _, err := objects.Put(ctx, tx, strings.NewReader(input), 8192)
		if err != nil {
			return err
		}
		for index, id := range []string{document, invalid} {
			if _, err := tx.Exec(ctx, `insert into kb_documents(id,collection_id,space_id,remote_id,title,media_type,source_revision,maintainer,current_object_hash)
			 values($1,$2,$3,$1::uuid::text,'Fixture','text/html','1',$4,$5)`, id, collection, space, actor, hash); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_document_versions(document_id,version,source_revision,object_hash,media_type,parse_next_at)
			 values($1,1,'1',$2,'text/html',clock_timestamp()-make_interval(secs=>$3))`, id, hash, float64(10-index)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	valid := `{"body":"品类 | 单位 | 限额\n差旅 | 晚 | 500","breadcrumb":["采购政策","差旅制度"],"anchor":{"kind":"pdf_table","page":2,"endPage":3,"startRow":4,"endRow":9}}` + "\n"
	var reject atomic.Bool
	parser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer fixture" || r.URL.Path != "/extract/html" || string(body) != input || err != nil {
			t.Errorf("unexpected parser input: %s %v", r.URL.Path, err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		if !reject.Load() {
			io.WriteString(w, valid)
			return
		}
		io.WriteString(w, valid+`{"body":"broken","breadcrumb":[],"anchor":{"kind":"pdf","page":0}}`+"\n")
	}))
	defer parser.Close()
	if ok, err := s.parseNext(ctx, parser.Client(), parser.URL, "fixture"); !ok || err != nil {
		t.Fatalf("valid layout parse: %t %v", ok, err)
	}
	reject.Store(true)
	if ok, err := s.parseNext(ctx, parser.Client(), parser.URL, "fixture"); !ok || err != nil {
		t.Fatalf("invalid layout parse: %t %v", ok, err)
	}
	var result, rejected string
	var count, page, endPage int
	err = db.QueryRow(ctx, `select
	 (select parse_status from kb_document_versions where document_id=$1),(select parse_status from kb_document_versions where document_id=$2),
	 (select count(*) from kb_chunks where document_id=$2),
	 (select (citation_anchor->>'page')::int from kb_chunks where document_id=$1),
	 (select (citation_anchor->>'endPage')::int from kb_chunks where document_id=$1)`, document, invalid).
		Scan(&result, &rejected, &count, &page, &endPage)
	if err != nil || result != "ready" || rejected != "invalid" || count != 0 || page != 2 || endPage != 3 {
		t.Fatalf("layout commit: status=%s invalid=%s count=%d pages=%d-%d %v", result, rejected, count, page, endPage, err)
	}
}

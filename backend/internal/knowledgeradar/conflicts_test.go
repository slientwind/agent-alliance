package knowledgeradar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStandardsNormalizeUnitsAndRetainTableRowContext(t *testing.T) {
	first := standardClaims("差旅住宿每晚上限为 500 元。", nil)
	second := standardClaims("差旅住宿上限 0.06 万元/晚", nil)
	if len(first) != 1 || len(second) != 1 || first[0].metric != second[0].metric || first[0].unit != "元/晚" || second[0].unit != first[0].unit ||
		first[0].value != "500.0000000000" || second[0].value != "600.0000000000" {
		t.Fatalf("equivalent constraints did not align: %+v %+v", first, second)
	}
	table := standardClaims("城市 | 酒店费上限\n杭州 | 450 元/晚\n上海 | 500 元/晚", []string{"差旅标准"})
	if len(table) != 2 || table[0].metric == table[1].metric || !strings.Contains(table[0].label, "杭州") || table[0].value != "450.0000000000" {
		t.Fatalf("row labels and thresholds separated: %+v", table)
	}
	if got := standardClaims("工单 234 已签收。发票 2026-003。", nil); len(got) != 0 {
		t.Fatalf("unrelated IDs were read as official claims: %+v", got)
	}
}

func TestKnowledgeConflictRadarOnlyComparesAuthorizedEffectiveDocuments(t *testing.T) {
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
	actor, outsider, team, collection := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	defer db.Exec(context.Background(), "delete from kb_collections where id=$1", collection)
	objects := knowledge.Objects{Root: t.TempDir()}
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	var space string
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into members(id,email,display_name,password_hash,role) values
		 ($1,$1::uuid::text||'@example.test','Conflict manager','','member'),($2,$2::uuid::text||'@example.test','Other admin','','admin')`, actor, outsider); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into teams(id,name) values($1,$1::uuid::text)`, team); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into team_members(team_id,member_id) values($1,$2)", team, actor); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select id from spaces where team_id=$1", team).Scan(&space); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into kb_collections(id,space_id,name,sync_provider,acl_mode,maintainer)
		 values($1,$2,'Travel','manual','space',$3)`, collection, space, actor); err != nil {
			return err
		}
		for i, sample := range []struct {
			name, body, status string
			expires            int
		}{
			{"政策甲", "差旅住宿每晚上限为 500 元。", "active", 5},
			{"政策乙", "差旅住宿上限 600 元/晚。", "active", 8},
			{"已过期", "差旅住宿上限 800 元/晚。", "active", -1},
			{"已归档", "差旅住宿上限 800 元/晚。", "archived", 5},
		} {
			hash, _, err := objects.Put(ctx, tx, strings.NewReader(sample.body), 8192)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_documents(id,collection_id,space_id,remote_id,title,media_type,source_revision,maintainer,current_object_hash,doc_status,effective_until)
			 values($1,$2,$3,$1::uuid::text,$4,'text/plain','1',$5,$6,$7,current_date+$8::int)`, ids[i], collection, space, sample.name, actor, hash, sample.status, sample.expires); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_document_versions(document_id,version,source_revision,object_hash,media_type,parse_status,parsed_at)
			 values($1,1,'1',$2,'text/plain','ready',clock_timestamp())`, ids[i], hash); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_chunks(document_id,version,ordinal,body,citation_anchor)
			 values($1,1,1,$2,'{"kind":"pdf","page":7}')`, ids[i], sample.body); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s := Server{DB: db}
	for range 3 {
		if scope, err := s.scanStandards(ctx); err != nil || scope != space {
			t.Fatalf("missing standards scan: %s %v", scope, err)
		}
	}
	for range 2 {
		if err := s.findConflicts(ctx, space); err != nil {
			t.Fatal(err)
		}
	}
	var alert string
	var count int
	if err := db.QueryRow(ctx, `select id,count(*) over() from kb_conflict_alerts where space_id=$1`, space).Scan(&alert, &count); err != nil || count != 1 {
		t.Fatalf("overlapping dated documents did not produce one alert: %d %v", count, err)
	}
	list := func(id string) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/conflicts", nil)
		r.SetPathValue("space", space)
		w := httptest.NewRecorder()
		if err := s.conflicts(w, r, identity.Member{ID: id}); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	if got := list(actor); !strings.Contains(got, `"value":"500"`) || !strings.Contains(got, `"value":"600"`) || !strings.Contains(got, `"page":7`) {
		t.Fatalf("conflict lost its source anchors: %s", got)
	}
	if got := strings.TrimSpace(list(outsider)); got != "[]" {
		t.Fatalf("foreign administrator could see claim details: %s", got)
	}
	r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/resolution", strings.NewReader(`{"expectedVersion":1,"resolution":"use_left","note":"按负责人核定口径"}`))
	r.SetPathValue("space", space)
	r.SetPathValue("alert", alert)
	if err := s.resolveConflict(httptest.NewRecorder(), r, identity.Member{ID: actor}); err != nil {
		t.Fatal(err)
	}
	if got := list(actor); !strings.Contains(got, `"status":"resolved"`) || !strings.Contains(got, `"resolution":"use_left"`) {
		t.Fatalf("decision was not recorded: %s", got)
	}
	if _, err := db.Exec(ctx, "update kb_documents set doc_status='deprecated' where id=$1", ids[0]); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(list(actor)); got != "[]" {
		t.Fatalf("outdated claim stayed visible: %s", got)
	}
}

package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"alliance/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestKnowledgeLexemesPreserveExactCodesAndChineseWords(t *testing.T) {
	lexemes := KnowledgeLexemes("采购 ＢＺ－４２ 工单 A-123/4.2；退费")
	for _, word := range []string{"采购", "bz-42", "工单", "a-123/4.2", "退费", "退"} {
		if !slices.Contains(lexemes, word) {
			t.Errorf("expected %q in %v", word, lexemes)
		}
	}
	if slices.Contains(lexemes, "42") || slices.Contains(lexemes, "费工") {
		t.Fatalf("exact codes or boundaries were split: %v", lexemes)
	}
}

const fixtureKBEncoder = "bge-m3/0123456789abcdef0123"
const fixtureKBReranker = "bge-reranker-v2-m3/0123456789abcdef0123"

type fixtureKBInference struct {
	seen []string
}

func (*fixtureKBInference) EmbeddingRevision(context.Context) (string, error) {
	return fixtureKBEncoder, nil
}

func (*fixtureKBInference) Embed(_ context.Context, texts []string) (string, []json.RawMessage, error) {
	vectors := make([]json.RawMessage, len(texts))
	for i, text := range texts {
		vec := make([]float64, 1024)
		at := 0
		if strings.Contains(text, "BZ-4200") {
			at = 1
		}
		vec[at] = 1
		vectors[i], _ = json.Marshal(vec)
	}
	return fixtureKBEncoder, vectors, nil
}

func (f *fixtureKBInference) Rerank(_ context.Context, _ string, passages []string) (string, []float64, error) {
	f.seen = append([]string{}, passages...)
	scores := make([]float64, len(passages))
	for i, text := range passages {
		scores[i] = 0.41
		if strings.Contains(text, "current") {
			scores[i] = 0.98
		}
	}
	return fixtureKBReranker, scores, nil
}

func TestKnowledgeHybridRankingRespectsEffectiveDatesAndLiveScope(t *testing.T) {
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
	objects := Objects{Root: t.TempDir()}
	actor, outsider, team := uuid.NewString(), uuid.NewString(), uuid.NewString()
	collection, commonCollection, foreignCollection, restrictedCollection := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	defer db.Exec(context.Background(), `delete from kb_collections where id=any($1::uuid[])`, []string{collection, commonCollection, foreignCollection, restrictedCollection})
	var space string
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into members(id,email,display_name,password_hash,role)
		 values($1,$1::uuid::text||'@example.test','Analyst','','member'),($2,$2::uuid::text||'@example.test','Other analyst','','admin')`, actor, outsider); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into teams(id,name) values($1,$1::uuid::text)`, team); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into team_members(team_id,member_id) values($1,$2)`, team, actor); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select id from spaces where team_id=$1", team).Scan(&space); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into kb_collections(id,space_id,name,sync_provider,acl_mode,maintainer,sync_status)
		 values($1,$2,'Team','manual','space',$3,'ready'),($4,(select id from spaces where kind='commons'),'Commons','manual','space',$3,'ready'),
		 ($5,(select id from spaces where owner_member_id=$6),'Private','manual','space',$6,'ready'),
		 ($7,$2,'External restrictions','feishu_drive','source',$3,'ready')`, collection, space, actor, commonCollection, foreignCollection, outsider, restrictedCollection); err != nil {
			return err
		}
		hash, _, err := objects.Put(ctx, tx, bytes.NewBufferString("fixture"), 8192)
		if err != nil {
			return err
		}
		for _, sample := range []struct {
			collection, status, title, body string
			age, expires, authority         int
		}{
			{collection, "active", "Current team policy", "current BZ-42 delivery limits and terms", -2, 20, 5},
			{commonCollection, "active", "Company baseline", "BZ-42 company baseline", -4, 20, 3},
			{collection, "archived", "Old procedure", "archive BZ-42 delivery limits", -10, 20, 4},
			{collection, "active", "Other code", "other BZ-4200 in catalog", -2, 20, 2},
			{collection, "active", "Future", "FUTURE BZ-42 unreleased", 30, 50, 5},
			{collection, "active", "Expired", "EXPIRED BZ-42 stale", -20, -3, 5},
			{collection, "deprecated", "Deprecated", "DEPRECATED BZ-42 obsolete", -10, 20, 5},
			{foreignCollection, "active", "Personal", "PRIVATE-FOREIGN BZ-42", -2, 20, 5},
			{restrictedCollection, "active", "Provider ACL", "UNMAPPED BZ-42", -2, 20, 5},
		} {
			doc := uuid.NewString()
			if _, err := tx.Exec(ctx, `insert into kb_documents(id,collection_id,space_id,remote_id,title,media_type,source_revision,current_object_hash,maintainer,
			 doc_status,effective_from,effective_until,authority_level) select $1,c.id,c.space_id,$1::uuid::text,$3,'text/plain','1',$4,c.maintainer,
			 $5,current_date+$6::int,current_date+$7::int,$8 from kb_collections c where id=$2`, doc, sample.collection, sample.title,
				hash, sample.status, sample.age, sample.expires, sample.authority); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_document_versions(document_id,version,source_revision,object_hash,media_type,parsed_at,parse_status)
			 values($1,1,'1',$2,'text/plain',clock_timestamp(),'ready')`, doc, hash); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into kb_chunks(document_id,version,ordinal,body,citation_anchor)
			 values($1,1,1,$2,'{"kind":"lines","startLine":1,"endLine":1}')`, doc, sample.body); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count, err := maintainKnowledgeLexemes(ctx, db); err != nil || count != 9 {
		t.Fatalf("lexical indexing count %d: %v", count, err)
	}
	lexical, err := SearchKnowledge(ctx, db, nil, space, actor, "ＢＺ－４２", 12)
	if err != nil || lexical.Mode != "bm25" || len(lexical.Matches) != 3 || lexical.Matches[0].Title != "Current team policy" || lexical.Matches[1].Title != "Company baseline" {
		t.Fatalf("wrong exact-code lexical results: %+v %v", lexical, err)
	}
	if _, err := db.Exec(ctx, `insert into enterprise_glossary(space_id,term,aliases,definition,verified_by)
	 values($1,'BZ-42',array['蓝叶交付'],'协议编号',$2)`, space, actor); err != nil {
		t.Fatal(err)
	}
	synonym, err := SearchKnowledge(ctx, db, nil, space, actor, "蓝叶交付", 12)
	if err != nil || len(synonym.Matches) == 0 || synonym.Matches[0].Title != "Current team policy" {
		t.Fatalf("real knowledge retrieval ignored the approved alias: %+v %v", synonym, err)
	}
	var otherSpace string
	if err := db.QueryRow(ctx, "select id from spaces where owner_member_id=$1", outsider).Scan(&otherSpace); err != nil {
		t.Fatal(err)
	}
	synonym, err = SearchKnowledge(ctx, db, nil, otherSpace, outsider, "蓝叶交付", 12)
	if err != nil || len(synonym.Matches) != 0 {
		t.Fatalf("another space inherited the private alias: %+v %v", synonym, err)
	}
	model := new(fixtureKBInference)
	for range 2 {
		if _, err := maintainKnowledgeVectors(ctx, db, model, fixtureKBEncoder); err != nil {
			t.Fatal(err)
		}
	}
	search, err := SearchKnowledge(ctx, db, model, space, actor, "semantic alternative", 12)
	if err != nil || search.Mode != "cross_encoder" || len(search.Matches) != 3 || search.Matches[0].Title != "Current team policy" || search.Matches[0].Score != 0.98 {
		t.Fatalf("wrong semantic recall or reranking: %+v %v", search, err)
	}
	for _, item := range model.seen {
		for _, forbidden := range []string{"PRIVATE-FOREIGN", "UNMAPPED", "DEPRECATED", "EXPIRED", "FUTURE"} {
			if strings.Contains(item, forbidden) {
				t.Fatalf("unauthorized or stale evidence reached the reranker: %s", item)
			}
		}
	}
	if _, err := db.Exec(ctx, "delete from team_members where (team_id,member_id)=($1,$2)", team, actor); err != nil {
		t.Fatal(err)
	}
	search, err = SearchKnowledge(ctx, db, model, space, actor, "BZ-42", 12)
	if err != nil || len(search.Matches) != 0 {
		t.Fatalf("revoked space leaked documents: %+v %v", search, err)
	}
}

package knowledgesync

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReviewedDeliveryArchivesExactVersionAndRetainsRevocation(t *testing.T) {
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
	exec := func(query string, values ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, values...); err != nil {
			t.Fatal(err)
		}
	}
	actor, outsider, team, project, collection := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	run, artifact, delivery := uuid.NewString(), uuid.NewString(), uuid.NewString()
	const approved = "# 采购制度\n清单须经过正式审签。"
	exec(`insert into members(id,email,display_name,password_hash,role) values
	 ($1,$1::uuid::text||'@example.test','Reviewer','','member'),($2,$2::uuid::text||'@example.test','Foreign admin','','admin')`, actor, outsider)
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, actor)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version,stage) values($1,$2,$3,$2,'Reviewed project',1,'review')", project, actor, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, actor)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Release guidelines',$2)", project, actor)
	exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
	 values($1,$2,1,$3,'completed',$4,$1::uuid::text,$5,1)`, run, project, actor, team, web.Hash(run))
	exec("insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,1,'正式制度')", artifact, project, run)
	exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,1,$2,'Original approved'),($1,2,'Later unsigned','New draft')", artifact, approved)
	exec("insert into reviews(artifact_id,version,reviewer) values($1,1,$2)", artifact, actor)
	exec("update artifacts set current_version=2 where id=$1", artifact)
	var space string
	if err := db.QueryRow(ctx, "select space_id from projects where id=$1", project).Scan(&space); err != nil {
		t.Fatal(err)
	}
	exec("insert into kb_collections(id,space_id,name,sync_provider,acl_mode,maintainer,sync_status) values($1,$2,'Team archive','manual','space',$3,'ready')", collection, space, actor)
	var source string
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var err error
		source, err = objects.NewTextSource(ctx, tx, project, actor, "Source", "Official purchase criteria")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	archive := func(member string, version int) (archiveResult, int, error) {
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/review-archive",
			strings.NewReader(fmt.Sprintf(`{"artifactId":"%s","version":%d}`, artifact, version)))
		r.SetPathValue("space", space)
		r.SetPathValue("collection", collection)
		w := httptest.NewRecorder()
		var data archiveResult
		if err := s.archiveReviewed(w, r, identity.Member{ID: member}); err != nil {
			return data, w.Code, err
		}
		err := json.Unmarshal(w.Body.Bytes(), &data)
		return data, w.Code, err
	}
	if _, _, err := archive(actor, 1); err == nil {
		t.Fatal("reviewed but unconfirmed draft was archived")
	}
	exec("insert into deliveries(id,project_id,confirmed_by) values($1,$2,$3)", delivery, project, actor)
	exec("insert into delivery_items(delivery_id,artifact_id,version) values($1,$2,1)", delivery, artifact)
	exec("insert into delivery_source_evidence(delivery_id,source_id,pinned,observed,revoked,assessments) values($1,$2,1,1,false,'[]')", delivery, source)
	if _, _, err := archive(outsider, 1); err == nil {
		t.Fatal("unscoped enterprise admin archived a team's work")
	}
	if _, _, err := archive(actor, 2); err == nil {
		t.Fatal("new unsigned version replaced the approved original")
	}
	out, status, err := archive(actor, 1)
	if err != nil || status != 201 || out.Delivery != delivery || !out.Created || out.Document == "" {
		t.Fatalf("archive did not create the reviewed document: %+v %d %v", out, status, err)
	}
	if retry, status, err := archive(actor, 1); err != nil || status != 200 || retry.Document != out.Document || retry.Created {
		t.Fatalf("archive retry was not idempotent: %+v %d %v", retry, status, err)
	}
	var text, state string
	var allowed bool
	err = db.QueryRow(ctx, `select d.doc_status,(select body from kb_chunks where document_id=d.id and ordinal=1),kb_document_readable(d.id,$2)
	 from kb_documents d where d.id=$1`, out.Document, actor).Scan(&state, &text, &allowed)
	if err != nil || !allowed || state != "active" || text != approved {
		t.Fatalf("archived knowledge differed from the approved version: %s %q %t %v", state, text, allowed, err)
	}
	exec("update sources set revoked_at=clock_timestamp() where id=$1", source)
	if err := db.QueryRow(ctx, "select kb_document_readable($1,$2)", out.Document, actor).Scan(&allowed); err != nil || allowed {
		t.Fatalf("withdrawn delivery evidence still allowed archive reads: %t %v", allowed, err)
	}
}

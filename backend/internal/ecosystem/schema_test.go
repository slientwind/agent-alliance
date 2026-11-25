package ecosystem

import (
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCatalogKeepsApprovedVersionAfterDiscoveryRefreshPostgres(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	reject := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err == nil {
			t.Fatal("invalid ecosystem record was accepted")
		}
	}
	admin, user, source := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`insert into members(id,email,display_name,password_hash,role) values
	 ($1,$1::uuid::text||'@example.test','ecosystem admin','','admin'),
	 ($2,$2::uuid::text||'@example.test','ecosystem user','','member')`, admin, user)
	endpoint := "https://registry.example.test/" + source
	exec(`insert into ecosystem_sources(id,name,kind,endpoint,cron,created_by)
	 values($1,'MCP fixture','mcp_registry',$2,'0 7 * * *',$3)`, source, endpoint, admin)
	reject(`update ecosystem_sources set credential='fixture'::bytea where id=$1`, source)
	reject(`update ecosystem_sources set endpoint='http://registry.example.test' where id=$1`, source)
	candidate := func(version, hash string) string {
		t.Helper()
		id := uuid.NewString()
		exec(`insert into ecosystem_catalog(id,source_id,source_revision,upstream_key,upstream_version,upstream_digest,kind,name)
		 values($1,$2,1,'example/skill',$3,$4,'skill','Fixture skill')`, id, source, version, hash)
		return id
	}
	oldHash, newHash, packageHash := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	original := candidate("1", oldHash)
	approved := uuid.NewString()
	const approval = `insert into enterprise_approved_packages(id,catalog_id,content_hash,manifest,approved_by,review_digest,manual_review_reason)
	 values($1,$2,$3,'{"name":"fixture"}',$4,ecosystem_review_digest($2),$5)`
	reject(approval, approved, original, packageHash, admin, "")
	exec(`insert into ecosystem_candidate_reviews(catalog_id,status,report)
	 values($1,'blocked','{"status":"blocked"}')`, original)
	reject(approval, approved, original, packageHash, admin, "risk acknowledgement")
	exec(`update ecosystem_candidate_reviews set status='review_required',report='{"status":"review_required"}',
	 content_hash=$2,snapshot='fixture'::bytea where catalog_id=$1`, original, packageHash)
	reject(approval, approved, original, packageHash, admin, "  ")
	reject(approval, approved, original, packageHash, user, "checked by a member")
	reject(approval, approved, original, newHash, admin, "checked by an administrator")
	exec(approval, approved, original, packageHash, admin, "checked the destination and license")
	exec(`insert into user_skill_bindings(member_id,approved_package_id) values($1,$2)`, user, approved)
	_ = candidate("1", newHash)
	_ = candidate("2", newHash)
	reject(`insert into ecosystem_catalog(source_id,source_revision,upstream_key,upstream_version,upstream_digest,kind,name)
	 values($1,1,'example/skill','1',$2,'skill','Duplicate')`, source, oldHash)
	reject(`update enterprise_approved_packages set content_hash=$2 where id=$1`, approved, newHash)
	reject(`update enterprise_approved_packages set manual_review_reason='replacement' where id=$1`, approved)
	reject(`insert into user_skill_bindings(member_id,approved_package_id) values($1,$2)`, user, uuid.NewString())
	var pinned, hash string
	var count int
	if err := db.QueryRow(ctx, `select p.catalog_id,p.content_hash,(select count(*) from ecosystem_catalog where source_id=$2)
	 from user_skill_bindings b join enterprise_approved_packages p on p.id=b.approved_package_id
	 where b.member_id=$1 and b.enabled and p.revoked_at is null`, user, source).Scan(&pinned, &hash, &count); err != nil ||
		pinned != original || hash != packageHash || count != 3 {
		t.Fatalf("new discoveries changed the selected version: %s %s versions=%d (%v)", pinned, hash, count, err)
	}
	exec(`update enterprise_approved_packages set revoked_at=now(),revoked_by=$2 where id=$1`, approved, admin)
}

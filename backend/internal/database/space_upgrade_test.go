package database

import (
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSpaceProvisioningUpgradesLegacyProjects(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_MIGRATION_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_MIGRATION_TEST_DATABASE to an empty scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var blank bool
	if err := pool.QueryRow(ctx, "select to_regclass('public.schema_versions') is null and to_regclass('public.projects') is null").Scan(&blank); err != nil || !blank {
		t.Fatalf("migration test requires an empty database: %v", err)
	}
	plan, err := migrationPlan()
	if err != nil {
		t.Fatal(err)
	}
	if err := testMigrationInTransaction(t, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, createVersions); err != nil {
			return err
		}
		var spaceUpgrade *migration
		for _, item := range plan {
			if item.name == "schema/216_space_provisioning_and_projects.sql" {
				spaceUpgrade = &item
				break
			}
			if err := applyMigration(ctx, tx, item); err != nil {
				return err
			}
		}
		if spaceUpgrade == nil {
			t.Fatal("space provisioning migration missing")
		}
		owner, guest, admin, team, solo, shared := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := tx.Exec(ctx, sql, args...); err != nil {
				t.Fatal(err)
			}
		}
		for id, role := range map[string]string{owner: "member", guest: "member", admin: "admin"} {
			exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','unused',$3)", id, id+"@example.test", role)
		}
		exec("insert into teams(id,name) values($1,'Mailbox team')", team)
		exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, owner, admin)
		exec(`insert into projects(id,owner,team_id,final_reviewer,title) values
			($1,$3,$4,$3,'Private activity'),($2,$3,$4,$5,'Collaborative activity')`, solo, shared, owner, team, guest)
		exec("insert into project_memberships(project_id,member_id,can_review) values($1,$3,true),($2,$3,true),($2,$4,true)", solo, shared, owner, guest)
		if err := applyMigration(ctx, tx, *spaceUpgrade); err != nil {
			return err
		}
		var soloKind, sharedKind string
		var ownerPrivate, adminPrivate, guestShared bool
		err := tx.QueryRow(ctx, `select
			(select kind from projects p join spaces s on s.id=p.space_id where p.id=$1),
			(select kind from projects p join spaces s on s.id=p.space_id where p.id=$2),
			exists(select from project_members where project_id=$1 and member_id=$3),
			exists(select from project_members where project_id=$1 and member_id=$4),
			exists(select from project_members where project_id=$2 and member_id=$5)`,
			solo, shared, owner, admin, guest).Scan(&soloKind, &sharedKind, &ownerPrivate, &adminPrivate, &guestShared)
		if err != nil || soloKind != "personal" || sharedKind != "team" || !ownerPrivate || adminPrivate || !guestShared {
			t.Fatalf("legacy classifications %s/%s and access %v/%v/%v: %v", soloKind, sharedKind, ownerPrivate, adminPrivate, guestShared, err)
		}
		tag, err := tx.Exec(ctx, "update project_members set can_review=false where project_id=$1 and member_id=$2", shared, guest)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("the live membership view must remain updatable: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

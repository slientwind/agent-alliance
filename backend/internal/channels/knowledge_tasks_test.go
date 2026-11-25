package channels

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFeishuKnowledgeTasksRetryWithSameKeyAndCurrentAssignee(t *testing.T) {
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
	exec := func(query string, arguments ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	member, team, gap, task, tenant := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','Gap owner','','member')", member)
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, member)
	exec("insert into channel_links(tenant,open_id,member_id) values($1,'ou_radar_fixture',$2)", tenant, member)
	exec(`insert into kb_gap_clusters(id,space_id,topic_summary,terms,status,assignee_id,revision)
	 select $1,id,'住宿报销政策',array['住宿','报销'],'assigned',$2,2 from spaces where team_id=$3`, gap, member, team)
	exec("insert into kb_gap_task_outbox(id,cluster_id,assignment_revision,assignee_id) values($1,$2,2,$3)", task, gap, member)
	f := Feishu{DB: db, Tenant: tenant}
	taskGUID := uuid.NewString()
	count := 0
	send := func(_ context.Context, received knowledgeTask) (string, error) {
		count++
		if received.id != task || received.gap != gap || received.recipient != "ou_radar_fixture" || received.topic != "住宿报销政策" {
			t.Fatalf("incorrect task parameters: %+v", received)
		}
		if count == 1 {
			return "", errors.New("synthetic unconfirmed reply")
		}
		return taskGUID, nil
	}
	if err := f.dispatchKnowledgeTask(ctx, send); err != nil {
		t.Fatal(err)
	}
	exec("update kb_gap_task_outbox set next_attempt_at=clock_timestamp() where id=$1", task)
	if err := f.dispatchKnowledgeTask(ctx, send); err != nil {
		t.Fatal(err)
	}
	if err := f.dispatchKnowledgeTask(ctx, send); err != nil || count != 2 {
		t.Fatalf("acknowledged task was sent again: %d %v", count, err)
	}
	var status, guid string
	if err := db.QueryRow(ctx, "select status,task_guid from kb_gap_task_outbox where id=$1", task).Scan(&status, &guid); err != nil || status != "sent" || guid != taskGUID {
		t.Fatalf("task receipt was not persisted: %s %s %v", status, guid, err)
	}
}

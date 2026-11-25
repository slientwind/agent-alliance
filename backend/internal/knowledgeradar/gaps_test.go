package knowledgeradar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestKnowledgeGapsAnonymizeClusterAndAssignWithinSpace(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("f1", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, arguments ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, arguments...); err != nil {
			t.Fatal(err)
		}
	}
	manager, assignee, outsider, team, project := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`insert into members(id,email,display_name,password_hash,role) values
	 ($1,$1::uuid::text||'@example.test','张三','','member'),($2,$2::uuid::text||'@example.test','李四','','member'),
	 ($3,$3::uuid::text||'@example.test','Out of space','','admin')`, manager, assignee, outsider)
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, manager)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, assignee)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Gap test',1)", project, manager, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, manager)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Test gap',$2)", project, manager)
	var space string
	if err := db.QueryRow(ctx, "select space_id from projects where id=$1", project).Scan(&space); err != nil {
		t.Fatal(err)
	}
	exec("insert into kb_collections(space_id,name,sync_provider,acl_mode,maintainer) values($1,'Gap sources','manual','space',$2)", space, manager)
	newRun := func() string {
		id := uuid.NewString()
		exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
		 values($1,$2,1,$3,'running',$4,$1::uuid::text,$5,1)`, id, project, manager, team, web.Hash(id))
		exec("insert into general_agent_runs(run_id,tier) values($1,'default')", id)
		return id
	}
	first, second := newRun(), newRun()
	for _, event := range []struct{ id, text string }{
		{first, "张三 差旅报销政策和住宿标准 NR-1029 550元 联系人:赵子明 gap.fixture@example.test"},
		{second, "李四 差旅报销政策和住宿标准 800元 13800138000"},
		{first, "retry same task"},
	} {
		if err := RecordGap(ctx, db, vault, event.id, event.text, knowledge.KnowledgeSearchResult{Mode: "bm25"}); err != nil {
			t.Fatal(err)
		}
	}
	s := Server{DB: db}
	for range 2 {
		if busy, err := s.clusterOne(ctx); !busy || err != nil {
			t.Fatalf("gap worker did not group an event: %t %v", busy, err)
		}
	}
	var gap string
	var asks int
	var samples []string
	if err := db.QueryRow(ctx, `select g.id,g.ask_count,array(select e.sample_masked_query from kb_gap_events e where e.cluster_id=g.id)
	 from kb_gap_clusters g where g.space_id=$1`, space).Scan(&gap, &asks, &samples); err != nil || asks != 2 || len(samples) != 2 {
		t.Fatalf("duplicate event or missed cluster: %d %v %v", asks, samples, err)
	}
	for _, sample := range samples {
		if !strings.Contains(sample, "差旅报销") {
			t.Fatalf("topic lost during anonymization: %q", sample)
		}
		for _, sensitive := range []string{"张三", "李四", "赵子明", "1029", "550", "800", "13800138000", "example.test"} {
			if strings.Contains(sample, sensitive) {
				t.Errorf("raw detail survived anonymization: %q", sensitive)
			}
		}
	}
	assign := func(operator, target string) error {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"assigneeId": target, "expectedVersion": 1})
		r := httptest.NewRequestWithContext(ctx, http.MethodPut, "/assignee", strings.NewReader(string(body)))
		r.SetPathValue("space", space)
		r.SetPathValue("gap", gap)
		return s.assignGap(httptest.NewRecorder(), r, identity.Member{ID: operator})
	}
	if err := assign(outsider, assignee); err == nil {
		t.Fatal("foreign administrator managed a team's gaps")
	}
	if err := assign(manager, outsider); err == nil {
		t.Fatal("gap assigned to someone outside the team")
	}
	for range 2 {
		if err := assign(manager, assignee); err != nil {
			t.Fatal(err)
		}
	}
	var pending int
	if err := db.QueryRow(ctx, `select count(*) from kb_gap_task_outbox where cluster_id=$1 and status='pending' and assignee_id=$2`, gap, assignee).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("gap assignment did not queue one scoped notification: %d %v", pending, err)
	}
}

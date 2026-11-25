package monitor

import (
	"alliance/internal/database"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReadQualityMetricsKeepsTheTracksIndependent(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	team, actor, project := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into teams(id,name) values($1,$2)`, team, "quality tracks-"+team)
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'quality test','','member')`, actor, actor+"@example.test")
	exec(`insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'quality test')`, project, actor, team)
	exec(`insert into plans(project_id,version,brief) values($1,1,'quality test')`, project)

	for _, tc := range []struct {
		key, track, status string
		signals            []string
		deliver            string
	}{
		{"copied", "lightweight", "completed", []string{"reply_copied"}, ""},
		{"no-signal", "lightweight", "completed", nil, ""},
		{"regenerated", "lightweight", "completed", []string{"artifact_downloaded", "reply_regenerated"}, ""},
		{"failure", "lightweight", "failed", []string{"feedback_thumbs_up"}, ""},
		{"final", "collaborative", "completed", nil, "full"},
		{"partial", "collaborative", "completed", nil, "partial"},
		{"thumb", "collaborative", "completed", []string{"feedback_thumbs_up"}, ""},
	} {
		run := uuid.NewString()
		exec(`insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,
		 max_calls,status,quality_track) values($1,$2,1,$3,$4,$5,'\x00',1,$6,$7)`, run, project, actor, team, tc.key, tc.status, tc.track)
		for _, signal := range tc.signals {
			exec(`insert into run_quality_signals(run_id,actor,kind) values($1,$2,$3)`, run, actor, signal)
		}
		if tc.deliver != "" {
			artifact, delivery := uuid.NewString(), uuid.NewString()
			exec(`insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,1,'report')`, artifact, project, run)
			exec(`insert into artifact_versions(artifact_id,version,body,reason) values($1,1,'report','test')`, artifact)
			exec(`insert into reviews(artifact_id,version,reviewer) values($1,1,$2)`, artifact, actor)
			edition := 1
			if tc.deliver == "partial" {
				edition = 2
			}
			exec(`insert into deliveries(id,project_id,confirmed_by,edition,partial,coverage)
			 values($1,$2,$3,$4,$5,'{}')`, delivery, project, actor, edition, tc.deliver == "partial")
			exec(`insert into delivery_items(delivery_id,artifact_id,version) values($1,$2,1)`, delivery, artifact)
		}
	}
	start, end := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	metric, err := readQualityMetrics(ctx, tx, start, end, actor, &team)
	if err != nil {
		t.Fatal(err)
	}
	if q := metric.Lightweight; q.Total != 4 || q.Met != 1 || q.Rate == nil || *q.Rate != 0.25 {
		t.Fatalf("lightweight quality = %+v", q)
	}
	if q := metric.Collaborative; q.Total != 3 || q.Met != 1 || q.Rate == nil || *q.Rate != 1.0/3 {
		t.Fatalf("collaborative quality = %+v", q)
	}
	empty, err := readQualityMetrics(ctx, tx, end, end.Add(time.Hour), actor, &team)
	if err != nil || empty.Lightweight.Rate != nil || empty.Collaborative.Rate != nil {
		t.Fatalf("empty window = %+v, err = %v", empty, err)
	}
}

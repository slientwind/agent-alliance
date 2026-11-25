package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"alliance/internal/channels"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestScheduleSpecPreservesZoneAndSingleInstant(t *testing.T) {
	expression := " 30 09 * * MON-FRI "
	seed := config{Title: "日报", Goal: "生成报告", Team: uuid.NewString(), Zone: "Asia/Shanghai", Cron: &expression}
	now := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	if err := seed.validate(now); err != nil {
		t.Fatal(err)
	}
	spec := seed.spec()
	if spec.TimeZoneName != "Asia/Shanghai" || spec.CronExpressions[0] != "30 09 * * MON-FRI" {
		t.Fatalf("recurring spec: %+v", spec)
	}
	at := now.In(time.FixedZone("UTC+8", 8*60*60)).Add(65 * time.Minute)
	seed.Cron, seed.OnceAt = nil, &at
	if err := seed.validate(now); err != nil {
		t.Fatal(err)
	}
	single := seed.spec()
	rule := single.Calendars[0]
	if single.TimeZoneName != "UTC" || rule.Year[0].Start != 2026 || rule.Hour[0].Start != 9 || rule.Minute[0].Start != 5 {
		t.Fatalf("one-time spec: %+v", single)
	}
	seed.Zone = "Local"
	if err := seed.validate(now); err == nil {
		t.Fatal("server-local timezone accepted")
	}
}

func TestAutomationLifecycleWithDurableRunsAndCursor(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("3d", 32))
	if err != nil {
		t.Fatal(err)
	}
	owner, team, model, provider := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into teams(id,name) values($1,$1::uuid::text)`, team)
	exec(`insert into enterprise(name,default_team) values('Schedule fixture',$1) on conflict do nothing`, team)
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'scheduler','','member')`, owner, owner+"@example.test")
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, owner)
	exec(`insert into channel_links(tenant,open_id,member_id) values('automation-fixture',$1,$2)`, owner, owner)
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
		values($1,$2,'schedule-test-model',array['text','tool_call'],8192,512)`, model, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
		values($1,now(),0,0,0,0)`, model)
	exec(`insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)`, model)
	defer db.Exec(context.Background(), "delete from model_tier_routes where model_id=$1", model)

	a := Activities{DB: db, Vault: vault}
	s := Server{DB: db, Vault: vault, Feishu: channels.Feishu{DB: db, App: "fixture", Secret: "fixture", Encrypt: "fixture",
		Verify: "fixture", Tenant: "automation-fixture", Public: "https://fixture.example.test"}}
	member := identity.Member{ID: owner}
	ref := ScheduleRef{ID: uuid.NewString(), Revision: 1}
	body := fmt.Sprintf(`{"title":"每周报告","goal":"整理待处理工作","team":%q,"zone":"Asia/Shanghai","cron":"30 9 * * MON","notifyFeishu":true}`, team)
	for range 2 {
		r := httptest.NewRequestWithContext(ctx, "POST", "/", strings.NewReader(body))
		r.Header.Set("Idempotency-Key", ref.ID)
		if err := s.create(httptest.NewRecorder(), r, member); err != nil {
			t.Fatal(err)
		}
	}
	list := httptest.NewRecorder()
	if err := s.list(list, httptest.NewRequestWithContext(ctx, "GET", "/", nil), member); err != nil {
		t.Fatal(err)
	}
	var views []scheduleView
	if err := json.Unmarshal(list.Body.Bytes(), &views); err != nil || len(views) != 1 ||
		views[0].ID != ref.ID || views[0].Goal != "整理待处理工作" || views[0].Revision != 1 {
		t.Fatalf("unexpected schedule listing: %s %v", list.Body, err)
	}
	requireCursor := func(want time.Time) {
		t.Helper()
		var cursor *time.Time
		err := db.QueryRow(ctx, "select cursor_at from automation_schedules where id=$1", ref.ID).Scan(&cursor)
		if err != nil || (cursor == nil) != want.IsZero() || (cursor != nil && !cursor.Equal(want)) {
			t.Fatalf("cursor=%v want=%v err=%v", cursor, want, err)
		}
	}
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	first := Occurrence{Schedule: ref, Key: "first", At: at}
	run, err := a.Prepare(ctx, first)
	if err != nil || run == "" {
		t.Fatalf("first occurrence: %q %v", run, err)
	}
	if replay, err := a.Prepare(ctx, first); err != nil || replay != run {
		t.Fatalf("occurrence replay: %q %v", replay, err)
	}
	if overlap, err := a.Prepare(ctx, Occurrence{Schedule: ref, Key: "overlap", At: at.Add(time.Minute)}); err != nil || overlap != "" {
		t.Fatalf("overlapping occurrence: %q %v", overlap, err)
	}
	if done, err := a.Progress(ctx, first, run); err != nil || done {
		t.Fatalf("unfinished run returned done=%t err=%v", done, err)
	}
	requireCursor(time.Time{})
	exec("update runs set status='completed',finished_at=now() where id=$1", run)
	if done, err := a.Progress(ctx, first, run); err != nil || !done {
		t.Fatalf("successful run returned done=%t err=%v", done, err)
	}
	requireCursor(at)
	var notice, project string
	err = db.QueryRow(ctx, `select n.id,r.project_id from automation_notifications n join automation_runs r using(schedule_id,occurrence)
		where n.schedule_id=$1 and n.occurrence=$2 and n.state='pending'`, ref.ID, first.Key).Scan(&notice, &project)
	if err != nil {
		t.Fatal(err)
	}
	var sends int
	dispatcher := Notifications{DB: db, Feishu: func(_ context.Context, id, member, binding, target string) (string, error) {
		sends++
		if id != notice || member != owner || binding == "" || target != project {
			t.Errorf("unexpected notification destination: %s %s %s %s", id, member, binding, target)
		}
		if sends == 1 {
			return "", errors.New("fixture confirmation rejected")
		}
		return "ou_msg_receipt_fixture", nil
	}}
	if err := dispatcher.deliverNext(ctx); err != nil {
		t.Fatal(err)
	}
	readRun := func() runView {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, "GET", "/", nil)
		r.SetPathValue("id", ref.ID)
		w := httptest.NewRecorder()
		if err := s.runs(w, r, member); err != nil {
			t.Fatal(err)
		}
		var history []runView
		if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
			t.Fatal(err)
		}
		for _, row := range history {
			if row.Occurrence == first.Key {
				return row
			}
		}
		t.Fatal("run missing from history")
		return runView{}
	}
	failedNotice := readRun()
	if failedNotice.State != "completed" || len(failedNotice.Notifications) != 1 || failedNotice.Notifications[0].State != "failed" {
		t.Fatalf("runner and delivery status were coupled: %+v", failedNotice)
	}
	retry := func(actor identity.Member) error {
		r := httptest.NewRequestWithContext(ctx, "POST", "/",
			strings.NewReader(fmt.Sprintf(`{"expected":%d}`, failedNotice.Notifications[0].Revision)))
		r.SetPathValue("id", ref.ID)
		r.SetPathValue("notice", notice)
		return s.retryNotification(httptest.NewRecorder(), r, actor)
	}
	if err := retry(identity.Member{ID: uuid.NewString()}); err == nil {
		t.Fatal("unrelated user could resend the notification")
	}
	if err := retry(member); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := dispatcher.deliverNext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	delivered := readRun()
	if len(delivered.Notifications) != 1 || delivered.Notifications[0].State != "accepted" ||
		delivered.Notifications[0].Receipt != "ou_msg_receipt_fixture" || delivered.Notifications[0].AcceptedAt == nil || sends != 2 {
		t.Fatalf("notification receipt or resend: %+v sends=%d", delivered, sends)
	}
	requireCursor(at)

	second := Occurrence{Schedule: ref, Key: "second", At: at.Add(2 * time.Minute)}
	run, err = a.Prepare(ctx, second)
	if err != nil || run == "" {
		t.Fatalf("next occurrence: %q %v", run, err)
	}
	var goal string
	if err := db.QueryRow(ctx, `select p.brief from runs r join plans p on (p.project_id,p.version)=(r.project_id,r.plan_version)
		where r.id=$1`, run).Scan(&goal); err != nil || !strings.Contains(goal, at.Format(time.RFC3339)) {
		t.Fatalf("cursor missing from task goal: %q %v", goal, err)
	}
	exec("update runs set status='failed',finished_at=now() where id=$1", run)
	if done, err := a.Progress(ctx, second, run); err != nil || !done {
		t.Fatalf("failed run returned done=%t err=%v", done, err)
	}
	requireCursor(at)

	third := Occurrence{Schedule: ref, Key: "third", At: at.Add(3 * time.Minute)}
	run, err = a.Prepare(ctx, third)
	if err != nil || run == "" {
		t.Fatalf("third occurrence: %q %v", run, err)
	}
	exec("update runs set status='completed',finished_at=now() where id=$1", run)
	if done, err := a.Progress(ctx, third, run); err != nil || !done {
		t.Fatalf("successful third run returned done=%t err=%v", done, err)
	}
	exec("delete from channel_links where (tenant,member_id)=('automation-fixture',$1)", owner)
	if err := dispatcher.deliverNext(ctx); err != nil || sends != 2 {
		t.Fatalf("notification escaped a deleted binding: %d %v", sends, err)
	}
	var cancelled string
	if err := db.QueryRow(ctx, `select state from automation_notifications where schedule_id=$1 and occurrence=$2`, ref.ID, third.Key).
		Scan(&cancelled); err != nil || cancelled != "cancelled" {
		t.Fatalf("cancelled notification not recorded: %q %v", cancelled, err)
	}

	var tc client.Client
	queue := "automation-test-" + ref.ID
	if address := os.Getenv("ALLIANCE_LOOKUP_TEST_TEMPORAL"); address != "" {
		tc, err = client.Dial(client.Options{HostPort: address})
		if err != nil {
			t.Fatal(err)
		}
		defer tc.Close()
		defer func() {
			short, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			_ = tc.ScheduleClient().GetHandle(short, "automation/"+ref.ID).Delete(short)
		}()
	}
	sync := func(paused bool) {
		t.Helper()
		if tc == nil {
			return
		}
		if err := syncSchedule(ctx, db, tc.ScheduleClient(), queue); err != nil {
			t.Fatal(err)
		}
		desc, err := tc.ScheduleClient().GetHandle(ctx, "automation/"+ref.ID).Describe(ctx)
		if err != nil || desc.Schedule.Spec.TimeZoneName != "Asia/Shanghai" || desc.Schedule.State.Paused != paused ||
			desc.Schedule.Policy.Overlap != enums.SCHEDULE_OVERLAP_POLICY_SKIP {
			t.Fatalf("Temporal schedule incorrect: %+v %v", desc, err)
		}
	}
	sync(false)
	setActive := func(rev int, active bool, who identity.Member) error {
		r := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/", strings.NewReader(fmt.Sprintf(`{"expected":%d,"active":%t}`, rev, active)))
		r.SetPathValue("id", ref.ID)
		return s.setActive(httptest.NewRecorder(), r, who)
	}
	if err := setActive(1, false, identity.Member{ID: uuid.NewString()}); err == nil {
		t.Fatal("another account could pause the schedule")
	}
	if err := setActive(1, false, member); err != nil {
		t.Fatal(err)
	}
	sync(true)
	if stale, err := a.Prepare(ctx, Occurrence{Schedule: ref, Key: "stale", At: at.Add(3 * time.Minute)}); err != nil || stale != "" {
		t.Fatalf("paused schedule accepted a stale occurrence: %q %v", stale, err)
	}
	if err := setActive(1, true, member); err == nil {
		t.Fatal("a stale edit changed the schedule")
	}
	if err := setActive(2, true, member); err != nil {
		t.Fatal(err)
	}
	sync(false)

	if tc != nil {
		w := worker.New(tc, queue, worker.Options{})
		w.RegisterWorkflowWithOptions(AutomationWorkflow, workflow.RegisterOptions{Name: WorkflowName})
		w.RegisterActivityWithOptions(a.Prepare, activity.RegisterOptions{Name: PrepareActivity})
		w.RegisterActivityWithOptions(a.Progress, activity.RegisterOptions{Name: ProgressActivity})
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()
		handle := tc.ScheduleClient().GetHandle(ctx, "automation/"+ref.ID)
		if err := handle.Trigger(ctx, client.ScheduleTriggerOptions{}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(25 * time.Second)
		var actualRun string
		var scheduled time.Time
		for time.Now().Before(deadline) {
			err := db.QueryRow(ctx, `select run_id,scheduled_at from automation_runs
				where schedule_id=$1 and schedule_revision=3 and state='running'`, ref.ID).Scan(&actualRun, &scheduled)
			if err == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if actualRun == "" || scheduled.Before(at) {
			t.Fatalf("Temporal did not dispatch a fresh scheduled run: %q %v", actualRun, scheduled)
		}
		exec("update runs set status='completed',finished_at=now() where id=$1", actualRun)
	}
	exec("delete from team_members where (team_id,member_id)=($1,$2)", team, owner)
	if err := pauseRevoked(ctx, db); err != nil {
		t.Fatal(err)
	}
	sync(true)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, owner)
	req := httptest.NewRequestWithContext(ctx, "DELETE", "/?expected=4", nil)
	req.SetPathValue("id", ref.ID)
	if err := s.remove(httptest.NewRecorder(), req, member); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if err := s.list(w, httptest.NewRequestWithContext(ctx, "GET", "/", nil), member); err != nil || w.Body.String() != "[]\n" {
		t.Fatalf("deleted schedule remained visible: %s %v", w.Body, err)
	}
	if tc != nil {
		if err := syncSchedule(ctx, db, tc.ScheduleClient(), queue); err != nil {
			t.Fatal(err)
		}
		var missing *serviceerror.NotFound
		if _, err := tc.ScheduleClient().GetHandle(ctx, "automation/"+ref.ID).Describe(ctx); !errors.As(err, &missing) {
			t.Fatalf("Temporal did not remove the schedule: %v", err)
		}
	}
}

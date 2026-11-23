"""Upgrade a populated 078 database with an outstanding review and an existing private binding."""

import json
import subprocess
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from uuid import uuid4


def verify_concurrent_scan(psql):
    def query(sql):
        return subprocess.check_output(psql,input=sql,text=True,timeout=20).strip()

    query("update projects set stage='review';")
    with subprocess.Popen(psql, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True) as writer:
        writer.stdin.write("begin; set local statement_timeout='12s'; select id from projects for update;\n")
        writer.stdin.flush()
        project = writer.stdout.readline().strip()
        with ThreadPoolExecutor(max_workers=1) as pool:
            scan = pool.submit(query, "set application_name='review_clock_test'; set lock_timeout='10s'; select sync_review_clocks(id) from projects;")
            try:
                for _ in range(40):
                    if query("select exists(select from pg_stat_activity where datname=current_database() and application_name='review_clock_test' and wait_event_type='Lock');") == "t":
                        break
                    time.sleep(0.05)
                else:
                    raise AssertionError("Background scan did not wait on the open project transaction")
                writer.stdin.write("select sync_review_clocks(id) from projects; commit;\n")
                writer.stdin.close()
                assert writer.wait(timeout=15) == 0
                scan.result(timeout=12)
            finally:
                if writer.poll() is None:
                    if not writer.stdin.closed:
                        writer.stdin.close()
                    writer.wait(timeout=15)
    assert project and query("select count(*) from live_review_clocks;") == "1"
    return {"backgroundScanWaited": True, "bothCommitted": True, "openClocks": 1}


def verify_review_upgrade(pg):
    database = "review_upgrade_" + uuid4().hex
    schema = Path(__file__).parent / "schema"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
    sql = "\n".join(
        p.read_text() for p in sorted(schema.glob("*.sql")) if p.name[:3] <= "078"
    )
    sql += """
    create temp table fixture as select gen_random_uuid() owner,gen_random_uuid() reviewer,gen_random_uuid() team,
       gen_random_uuid() project,gen_random_uuid() run,gen_random_uuid() artifact;
    insert into members(id,email,display_name,password_hash,role)
      select owner,'owner@sample.invalid','Owner','','admin' from fixture union all
      select reviewer,'review@sample.invalid','Reviewer','','member' from fixture;
    insert into teams(id,name) select team,'Existing team' from fixture;
    insert into projects(id,owner,team_id,final_reviewer,title,stage,updated_at)
       select project,owner,team,reviewer,'Historical project','review',now()-interval '4 hours' from fixture;
    insert into project_memberships(project_id,member_id,can_review) select project,owner,false from fixture;
    insert into plans(project_id,version,brief) select project,1,'Original scope' from fixture;
    insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
       select run,project,1,owner,'completed',team,'history',decode('01','hex'),1 from fixture;
    insert into artifacts(id,project_id,run_id,ordinal,name) select artifact,project,run,1,'Original draft' from fixture;
    insert into artifact_versions(artifact_id,version,body,reason,created_at)
       select artifact,1,'Untouched','Original',now()-interval '5 hours' from fixture;
    insert into channel_links(tenant,open_id,member_id) select 'existing','ou_old',owner from fixture;
    """
    sql += (schema / "079_review_reminders.sql").read_text()
    sql += (schema / "080_close_review_period.sql").read_text()
    sql += (schema / "081_review_clock_lock_order.sql").read_text()
    sql += """
    select sync_review_clocks(project) from fixture;
    create temp table earlier as select clock,opened_at,original_due,due_at from live_review_clocks;
    create temp table no_reviewer as select phase,recipient from review_reminder_targets;
    insert into project_memberships(project_id,member_id,can_review) select project,reviewer,true from fixture;
    select sync_review_clocks(project) from fixture;
    select json_build_object('upgradeUsesRecordedTime',p.review_started_at=p.updated_at and e.opened_at=p.updated_at,
       'hours',extract(epoch from e.original_due-e.opened_at)/3600,'immediateCoordination',(select phase='unassigned' and recipient=f.owner from no_reviewer),
       'reviewerAssigned',(select phase='initial' and recipient=f.reviewer from review_reminder_targets),
       'sameClock',e.clock=c.clock and e.due_at=c.due_at,'silentBinding',(select not review_reminders from channel_links),
       'businessUntouched',p.stage='review' and not exists(select from reviews) and not exists(select from invocations))
       from fixture f join projects p on p.id=f.project cross join earlier e cross join live_review_clocks c;
    update review_clocks set due_at=now()+interval '1 hour';
    select phase from review_reminder_targets;
    update projects set stage='running';
    update projects set stage='review';
    select count(*) from live_review_clocks;
    select sync_review_clocks(project) from fixture;
    select c.clock<>e.clock from live_review_clocks c cross join earlier e;
    update projects set stage='cancelled';
    select json_build_object('openClocks',(select count(*) from review_clocks where closed_at is null),
       'recipients',(select count(*) from review_reminder_targets),'queued',(select count(*) from review_notifications));
    """
    run = [*pg[:-1], "-i", pg[-1]]
    subprocess.run(run + ["createdb", "-U", "alliance", database], check=True)
    try:
        result = subprocess.run(
            run
            + [
                "psql",
                "-XAtq",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                database,
            ],
            input=sql,
            text=True,
            capture_output=True,
        )
        if result.returncode:
            raise RuntimeError(result.stderr[-1600:])
        rows = [v for v in result.stdout.splitlines() if v]
        data = json.loads(rows[-5])
        assert data == dict(
            upgradeUsesRecordedTime=True,
            hours=24,
            immediateCoordination=True,
            reviewerAssigned=True,
            sameClock=True,
            silentBinding=True,
            businessUntouched=True,
        ), data
        assert rows[-4:-1] == ["due", "0", "t"], rows[-4:-1]
        closed = json.loads(rows[-1])
        assert closed == dict(openClocks=0, recipients=0, queued=0)
        concurrent = verify_concurrent_scan([*run,"psql","-XAtq","-v","ON_ERROR_STOP=1","-U","alliance","-d",database])
        return {
            **data,
            "concurrentPolicySnapshot": concurrent,
            "beforeDeadline": rows[-4],
            "newReviewPeriodGetsNewClock": True,
            "afterCancellation": closed,
        }
    finally:
        subprocess.run(run + ["dropdb", "-U", "alliance", database], check=True)

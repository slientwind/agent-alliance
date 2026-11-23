"""Exercise reporting boundaries and the failure receipt gate on an isolated database."""

import json
import subprocess
from pathlib import Path
from uuid import uuid4


def verify():
    root = Path(__file__).resolve().parents[1] / "backend/internal"
    db = "state_check_" + uuid4().hex
    docker = ["docker", "exec", "-i", "alliance-workspace-database-1"]
    admin, team, project, department, grant = [str(uuid4()) for _ in range(5)]
    start, end = "2021-05-01T00:00:00Z", "2021-05-02T00:00:00Z"
    states = [
        "queued",
        "running",
        "paused",
        "blocked",
        "reconcile",
        "failed",
        "cancelled",
        "completed",
        "historic_internal_code",
    ]

    def sql(statement):
        return subprocess.check_output(
            docker
            + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db],
            text=True,
            input=statement,
        ).strip()

    def run(state, at):
        run_id = str(uuid4())
        sql(
            f"""insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls,created_at)
            values ('{run_id}','{project}',1,'{admin}','{state}','{team}','{run_id}','x',3,'{at}')"""
        )
        return run_id

    usage = (root / "monitor/usage.sql").read_text()
    scoped = (root / "monitor/scope_report.sql").read_text()

    def aggregates(team_id):
        data = sql(
            "prepare usage(timestamptz,timestamptz,text,uuid) as "
            + usage
            + f"; execute usage('{start}','{end}','{admin}','{team_id}')"
        ).split("|")
        return int(data[3]), json.loads(data[-1])

    subprocess.run(docker + ["createdb", "-U", "alliance", db], check=True)
    try:
        sql(
            "\n".join(
                p.read_text() for p in sorted((root / "database/schema").glob("*.sql"))
            )
        )
        sql(
            f"""insert into teams(id,name) values('{team}','Synthetic');
        insert into departments(id,name) values('{department}','Historical department');
        insert into enterprise(id,name,default_team) values(true,'Synthetic','{team}');
        insert into members(id,email,display_name,password_hash,role,department_id) values
            ('{admin}','sample@example.invalid','Synthetic','!','admin','{department}');
        insert into projects(id,owner,team_id,final_reviewer,title)
            values('{project}','{admin}','{team}','{admin}','Synthetic');
        insert into plans(project_id,version,brief) values('{project}',1,'Summary');
        insert into management_grants(id,member_id,department_id,include_costs,granted_by,expires_at,reason)
            values('{grant}','{admin}','{department}',false,'{admin}',now()+interval '1 hour','Summary');"""
        )
        records = {state: run(state, start) for state in states}
        run("running", end)
        run("failed", "2021-04-30T23:59:59Z")
        expected = {s if s != "historic_internal_code" else "other": 1 for s in states}
        assert aggregates(team) == (len(states), expected)
        assert aggregates(str(uuid4())) == (0, {})

        def scope():
            return json.loads(
                sql(
                    "prepare report(uuid,uuid,timestamptz,timestamptz) as "
                    + scoped
                    + f"; execute report('{grant}','{admin}','{start}','{end}')"
                )
            )

        assert (
            scope()["runStates"] == expected
            and scope()["waiting"] == 3
            and scope()["costs"] is None
        )
        target = records["reconcile"]

        def gate():
            return sql(f"select run_failure_confirmable('{target}')") == "t"

        assert not gate()
        sql(
            f"""insert into invocations(run_id,ordinal,status,remote_status) values('{target}',1,'remote_closed','failed');
                insert into remote_jobs(run_id,ordinal,phase,deadline,detail) values('{target}',1,'finished',now(),'remote_failed');"""
        )
        assert gate()
        sql(
            f"insert into invocations(run_id,ordinal,status) values('{target}',2,'submitted')"
        )
        assert not gate()
        sql(
            f"""update invocations set status='completed',attempt_no=2 where run_id='{target}' and ordinal=2;
          with control as (insert into controls(run_id,actor,request_key,signature,action,reason,revision)
              values('{target}','{admin}','retry','x','redo','Synthetic',2) returning id),
          review as (insert into call_reviews(control_id,run_id,ordinal,attempt_no,decision)
              select id,'{target}',2,1,'redo' from control returning control_id)
          insert into superseded_calls(run_id,ordinal,attempt_no,started_at,ended_at,review_control)
              select '{target}',2,1,now(),now(),control_id from review;"""
        )
        assert not gate()
        sql(
            f"insert into late_call_receipts(run_id,ordinal,attempt_no,outcome,remote_state) values('{target}',2,1,'remote','working')"
        )
        assert not gate()
        sql(
            f"update late_call_receipts set remote_state='completed' where run_id='{target}'"
        )
        assert gate()
        sql(f"update runs set status='failed' where id='{target}'")
        assert not gate()
        current = scope()
        assert (
            current["runs"] == len(states)
            and current["waiting"] == 2
            and current["runStates"]["failed"] == 2
        )
        assert (
            sql(
                f"select finished_at is not null and trace_expires_at>=finished_at from runs where id='{target}'"
            )
            == "t"
        )
        takeover, prior, offer, request = [str(uuid4()) for _ in range(4)]
        sql(
            f"""insert into members(id,email,display_name,password_hash,role)
            values('{prior}','prior@sample.invalid','Prior reviewer','!','member');
            insert into reviewer_handoffs(id,project_id,previous_reviewer,successor,proposer,emergency,reason,end_condition,state,recovery_until)
              values('{takeover}','{project}','{prior}','{admin}','{admin}',true,'Test','Finish','accepted',now()+interval '2 hours');
            update projects set reviewer_assignment='{takeover}' where id='{project}';
            insert into project_memberships(project_id,member_id,can_review,recovery_assignment)
              values('{project}','{admin}',true,'{takeover}');
            insert into reviewer_handoffs(id,project_id,previous_reviewer,successor,proposer,emergency,reason,end_condition)
              values('{offer}','{project}','{admin}','{prior}','{admin}',false,'Test','Finish');
            insert into budget_requests(id,project_id,scope,team_id,requester,request_key,signature,requested_increase,baseline_revision,baseline_limit,
              project_used,project_unknown,reason,remaining_work,alternative)
              values('{request}','{project}','project','{team}','{admin}','test','x',1,1,0,0,0,'Test','Pending','None');"""
        )
        assert (
            sql(
                f"select count(*) from project_members where project_id='{project}' and member_id='{admin}'"
            )
            == "1"
        )
        sql(f"update projects set stage='failed' where id='{project}'")
        assert (
            sql(
                f"select state||'/'||decision_reason from budget_requests where id='{request}'"
            )
            == "closed/运行已确认失败"
        )
        assert (
            sql(f"select state from reviewer_handoffs where id='{offer}'") == "closed"
        )
        assert (
            sql(
                f"select access_end_reason from reviewer_handoffs where id='{takeover}'"
            )
            == "项目已结束"
        )
        assert (
            sql(
                f"select count(*) from project_members where project_id='{project}' and member_id='{admin}'"
            )
            == "0"
        )
        return {
            "initial": expected,
            "temporaryTakeoverAccessAfterFailure": False,
            "requestAndOfferClosed": True,
            "afterConfirmation": current["runStates"],
            "includedAtStart": True,
            "excludedAtEnd": True,
            "unknownHistoricalCodeRedacted": True,
            "crossTeamCount": 0,
            "parallelSubmissionBlockedClosure": True,
            "unresolvedSupersededAttemptBlockedClosure": True,
            "unfinishedLateRemoteReceiptBlockedClosure": True,
            "failureFinishAndTraceExpiryRecorded": True,
        }
    finally:
        subprocess.run(docker + ["dropdb", "-U", "alliance", db], check=True)


if __name__ == "__main__":
    print(json.dumps(verify(), indent=2))

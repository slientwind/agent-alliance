"""Exercise review clocks, permission checks and recovery against the loopback Feishu service."""

import http.cookiejar
import json
import secrets
from smoke_submissions import receipt_time
import subprocess
import time
import threading
import urllib.request
from datetime import datetime, timedelta, timezone


def verify_review_reminders(req, fixture, frontend, env, agent, report_dir, pg, db):
    email = "reminder-owner@sample.invalid"
    password = secrets.token_urlsafe(24)
    actor = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
    )
    invite = req("admin/invitations", {})
    req(
        "register",
        dict(token=invite["token"], name="提醒试用人", email=email, password=password),
        session=actor,
    )

    def me(*a, **kw):
        return req(*a, session=actor, **kw)

    def sql(q):
        return subprocess.check_output(
            pg
            + [
                "psql",
                "-At",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                db,
                "-c",
                q,
            ],
            text=True,
        ).strip()

    def until(fn, seconds=40):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            val = fn()
            if val:
                return val
            time.sleep(0.3)
        raise AssertionError("No expected review reminder state: " + str(fn()))

    def denied(code, fn):
        try:
            fn()
        except RuntimeError as exc:
            assert exc.args[1] == code, exc
        else:
            raise AssertionError("Forbidden or stale review update accepted")

    sender = "ou_review_scenario"
    bound = me("channels/feishu/bind", {})["command"]
    assert "身份已绑定" in fixture.reply(
        fixture.send(req, bound, sender=sender, chat="oc_review_scenario")
    )
    pref = "channels/feishu/review-reminders"
    settings = me(pref)
    assert not settings["enabled"] and not settings["latest"]
    team = next(t for t in me("teams") if t["default"])["id"]
    team_policy = "teams/" + team + "/review-policy"
    team_before = req(team_policy)
    denied(
        403,
        lambda: me(
            team_policy,
            dict(
                hours=48,
                lead=2,
                escalation=4,
                expected=team_before["revision"],
                reason="越权测试",
            ),
            method="PUT",
        ),
    )
    base = "projects/" + me("projects", dict(goal="校对培训手册"))["id"]
    project = base.split("/")[1]
    policy = base + "/review-policy"
    req(
        team_policy,
        dict(
            hours=18,
            lead=3,
            escalation=2,
            expected=team_before["revision"],
            reason="验证团队默认节奏",
        ),
        method="PUT",
    )
    inherited = me(policy)
    assert inherited["source"] == "team" and inherited["hours"] == 18
    req(
        team_policy,
        dict(
            hours=team_before["hours"],
            lead=team_before["lead"],
            escalation=team_before["escalation"],
            expected=team_before["revision"] + 1,
            reason="恢复测试前节奏",
        ),
        method="PUT",
    )
    config = me(policy)
    assert config["hours"] == team_before["hours"]
    me(
        policy,
        dict(
            hours=12,
            lead=2,
            escalation=4,
            expected=config["revision"],
            reason="本项目审核周期",
        ),
        method="PUT",
    )
    denied(
        409,
        lambda: me(
            policy,
            dict(
                hours=10,
                lead=2,
                escalation=4,
                expected=config["revision"],
                reason="并发更新",
            ),
            method="PUT",
        ),
    )
    assert me(policy)["source"] == "project"
    me(
        base + "/plan",
        dict(
            expected=me(base)["revision"],
            brief="生成一份待审核的培训手册",
            agents=[agent],
        ),
    )
    me(
        base + "/runs",
        dict(expected=me(base)["revision"], authorizeCalls=1),
        headers={"Idempotency-Key": "reminders-run"},
    )
    until(lambda: me(base)["stage"] == "review")
    artifact = me(base)["artifacts"][0]["id"]
    path = base + "/artifacts/" + artifact

    def clocks():
        return [r for r in me("projects/review-reminders") if r["project"] == project]

    first = clocks()[0]

    def stamp(v):
        return receipt_time(v.replace("Z", "+00:00"))

    assert stamp(first["due"]) - stamp(first["opened"]) == timedelta(hours=12)
    me(
        policy,
        dict(hours=9, lead=1, escalation=0, expected=1, reason="仅用于新待办"),
        method="PUT",
    )
    assert clocks()[0]["due"] == first["due"]
    reviewer = req("me")["id"]
    assert not any(r["project"] == project for r in req("projects/review-reminders"))
    me(
        base + "/participants",
        dict(member=reviewer, review=True, expected=me(base)["revision"]),
    )
    fb = me(
        path + "/feedback",
        dict(version=1, kind="suggestion", position="标题", body="核对标题"),
    )["id"]
    row = next(r for r in clocks() if r["kind"] == "suggested")
    assert stamp(row["due"]) - stamp(row["opened"]) == timedelta(hours=9)
    # Advance only this isolated clock into its escalation window.
    sql(
        "update review_clocks set opened_at=now()-interval '10 hours',due_at=now()-interval '1 hour',original_due=now()-interval '1 hour' where id='"
        + row["clock"]
        + "'"
    )
    before = next(r for r in clocks() if r["kind"] == "suggested")
    me(
        path + "/feedback/" + fb,
        dict(
            action="transfer",
            assignee=reviewer,
            expected=1,
            version=1,
            reason="换审核人，保留已等待时间",
        ),
    )
    after = next(r for r in clocks() if r["kind"] == "suggested")
    assert (
        after["clock"] == before["clock"]
        and after["opened"] == before["opened"]
        and after["phase"] == "escalated"
        and not after["canHandle"]
        and after["originalDue"] == before["originalDue"]
    )
    denied(
        403,
        lambda: me(
            path + "/feedback/" + fb,
            dict(action="decline", expected=2, version=1, reason="验证协调与审核分离"),
        ),
    )
    ext = base + "/review-reminders/" + row["clock"] + "/extend"
    change = dict(
        expected=1,
        due=(datetime.now(timezone.utc) + timedelta(hours=5)).isoformat(),
        reason="等待业务确认",
    )
    denied(403, lambda: req(ext, change))
    me(ext, change)
    denied(409, lambda: me(ext, change))
    assert not any(r["kind"] == "suggested" for r in clocks())
    moved = next(
        r for r in req("projects/review-reminders") if r["clock"] == after["clock"]
    )
    assert (
        moved["canHandle"]
        and moved["originalDue"] == before["originalDue"]
        and moved["due"] != moved["originalDue"]
    )
    req(
        path + "/feedback/" + fb,
        dict(action="decline", expected=2, version=1, reason="已核对标题"),
    )
    assert not any(
        r["clock"] == after["clock"] for r in req("projects/review-reminders")
    )

    def browser(action):
        subprocess.run(
            ["node", str(frontend / "tests" / "review-reminders.mjs")],
            cwd=frontend,
            env={
                **env,
                "REMINDER_EMAIL": email,
                "REMINDER_PASSWORD": password,
                "REMINDER_ACTION": action,
                "VISUAL_EVIDENCE": str(report_dir),
            },
            check=True,
            timeout=60,
        )

    # No proactive notification is allowed before the recipient opts in.
    attempts = lambda: [r for r in fixture.direct if r[0] == sender]
    assert not attempts()
    fixture.direct_down.add(sender)
    browser("enable")
    failed = until(lambda: me(pref)["retry"], 55)
    assert len(attempts()) == 3 and {r[1] for r in attempts()} == {failed}
    # Explicit retry while still down. Then make the original decision in the browser.
    browser("retry")
    until(lambda: len(attempts()) >= 4)
    retry = attempts()[-1][1]
    assert failed != retry
    browser("review")
    fixture.direct_down.discard(sender)
    until(
        lambda: sql("select status from review_notifications where id='" + retry + "'")
        == "obsolete"
    )
    until(lambda: any(not row[3] for row in attempts()))
    accepted = next(r for r in reversed(attempts()) if not r[3])
    assert (
        accepted[1] not in (failed, retry)
        and artifact not in accepted[2]
        and project not in accepted[2]
    )
    assert sql(
        "select string_agg(c.action_key,',') from review_notification_items i join review_clocks c on c.id=i.clock where i.batch='"
        + accepted[1]
        + "'"
    ).startswith("deliver:" + project)
    assert me(base)["stage"] == "review" and clocks()[0]["kind"] == "deliver"
    final = clocks()[0]
    assert me(policy)["hours"] == 10 and stamp(final["due"]) - stamp(
        final["opened"]
    ) == timedelta(hours=9)
    denied(409, lambda: me(pref + "/retry", dict(batch=failed)))
    checked = me(base)
    me(base + "/deliver", dict(expected=checked["revision"]))
    assert clocks() == [] and me(base)["stage"] == "delivered"
    assert sql("select count(*) from runs where project_id='" + project + "'") == "1"
    assert (
        sql(
            "select count(*) from invocations i join runs r on r.id=i.run_id where r.project_id='"
            + project
            + "'"
        )
        == "1"
    )
    # Multiple decisions in one outage digest. One member approves the first
    # output before recovery; the replay keeps its UUID and excludes that item.
    fixture.direct_down.add(sender)
    group_project = me("projects", dict(goal="审阅两份培训材料"))["id"]
    group = "projects/" + group_project
    me(
        group + "/plan",
        dict(
            expected=me(group)["revision"],
            brief="分别制作两个可审核版本",
            agents=[agent, agent],
        ),
    )
    me(
        group + "/runs",
        dict(expected=me(group)["revision"], authorizeCalls=2),
        headers={"Idempotency-Key": "group-reminders-run"},
    )
    until(lambda: me(group)["stage"] == "review")
    group_items = me(group)["artifacts"]
    group_id = until(
        lambda: next(
            (
                r[1]
                for r in reversed(attempts())
                if r[1] not in (failed, retry, accepted[1])
            ),
            None,
        )
    )
    assert (
        sql(
            "select count(*) from review_notification_items where batch='"
            + group_id
            + "'"
        )
        == "2"
    )
    gate = (threading.Event(), threading.Event())
    fixture.auth_gate = gate
    assert gate[0].wait(15), "notification retry did not request a token"
    try:
        me(group + "/artifacts/" + group_items[0]["id"] + "/review", dict(version=1))
        fixture.direct_down.discard(sender)
    finally:
        gate[1].set()
    until(lambda: any(r[1] == group_id and not r[3] for r in attempts()))
    assert until(
        lambda: sql(
            "select count(*) from review_notification_items where batch='"
            + group_id
            + "' and superseded_at is not null"
        )
        == "1"
    )
    assert me(group)["stage"] == "review"
    previous = {r[1] for r in attempts()}
    fixture.direct_down.add(sender)
    me(group + "/artifacts/" + group_items[1]["id"] + "/review", dict(version=1))
    long_id = until(
        lambda: next((r[1] for r in attempts() if r[1] not in previous), None)
    )
    until(
        lambda: sql(
            "select last_error<>'' from review_notifications where id='" + long_id + "'"
        )
        == "t"
    )
    sql(
        "update review_notifications set first_attempt=now()-interval '2 hours', next_at=now() where id='"
        + long_id
        + "'"
    )
    fixture.direct_down.discard(sender)
    until(lambda: me(pref)["retry"] == long_id)
    assert len([r for r in attempts() if r[1] == long_id]) == 1
    browser("retry")
    until(
        lambda: any(r[1] not in previous | {long_id} and not r[3] for r in attempts())
    )
    assert me(group)["stage"] == "review" and me(pref)["enabled"]
    me(pref, dict(binding=settings["binding"], enabled=False), method="PUT")
    assert not me(pref)["enabled"]
    channel = req("admin/channels/feishu/review-health")
    assert channel["accepted"] >= 2 and channel["obsolete"] >= 2
    denied(403, lambda: me("admin/channels/feishu/review-health"))
    me("channels/feishu/bind", method="DELETE")
    result = dict(
        input="synthetic",
        directAttemptsBeforeRepair=3,
        originalClockRetained=True,
        mixedDigestSize=2,
        mixedDigestSuperseded=1,
        remoteTokenPendingDuringReview=True,
        longOutageRetryRequiredExplicitConsent=True,
        adminHealth=channel,
        manualRetryOf=failed,
        obsoleteRetry=retry,
        acceptedCurrentFinalDigest=accepted[1],
        runs=1,
        invocations=1,
        delivered=True,
    )
    (report_dir / "review-reminders.json").write_text(
        json.dumps(result, ensure_ascii=False, indent=2)
    )

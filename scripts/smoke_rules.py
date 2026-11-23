"""Exercise team rule revisions and current admission against isolated services."""

from concurrent.futures import ThreadPoolExecutor
import subprocess
import time
from datetime import datetime, timedelta, timezone
from uuid import UUID


def verify_rules(
    req,
    pg,
    db,
    team,
    member,
    agent,
    service,
    received,
    release,
    model,
    planning_received,
):
    base = f"teams/{team}/rules"

    def member_req(path, body=None):
        return req(path, body, session=member)

    def denied(status, fn):
        try:
            fn()
        except RuntimeError as e:
            assert e.args[1] == status, (status, e.args[1:])
            return e.args[2]
        raise AssertionError(f"expected {status}")

    def sql(command):
        return subprocess.check_output(
            pg
            + [
                "psql",
                "-XAtq",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                db,
                "-c",
                command,
            ],
            text=True,
        ).strip()

    def rule():
        return next(r for r in member_req(base) if r["id"] == rid)

    def publish(v, disabled=False):
        req(
            f"{base}/{rid}/publish",
            dict(revision=v, disabled=disabled, expected=rule()["lifecycle"]),
        )

    def plan():
        project = req("projects", dict(goal="核对活动规则"))["id"]
        path = f"projects/{project}"
        req(
            path + "/plan",
            dict(expected=req(path)["revision"], brief="整理活动说明", agents=[agent]),
        )
        return path

    def start(path, key):
        return req(
            path + "/runs",
            dict(expected=req(path)["revision"], authorizeCalls=1),
            headers={"Idempotency-Key": key},
        )

    def waitstage(path, target):
        for _ in range(120):
            p = req(path)
            if p["stage"] == target:
                return p
            time.sleep(0.2)
        raise AssertionError((target, p["stage"], p.get("run")))

    expires = (datetime.now(timezone.utc) + timedelta(days=10)).isoformat()
    rid = str(
        UUID(
            member_req(
                base, dict(body="RULE_HOLD_V1 · 复核报价有效期", expiresAt=expires)
            )["id"]
        )
    )
    pub = f"{base}/{rid}/publish"
    assert not rule()["published"]
    denied(403, lambda: member_req(pub, dict(revision=1)))
    publish(1)
    sql(
        f"update rule_versions set review_at=now()-interval '1 day' where rule_id='{rid}'"
    )
    first = rule()
    assert first["due"] and not first["expired"]
    rev = f"{base}/{rid}/review"
    review = dict(
        revision=1, expected=first["lifecycle"], reason="已核对有效的部门约定"
    )
    denied(404, lambda: req(rev, review))
    member_req(rev, review)
    assert (
        not rule()["due"]
        and rule()["publishedExpiresAt"] == first["publishedExpiresAt"]
    )
    denied(409, lambda: member_req(rev, review))
    # A new draft cannot change shared work until a publisher adopts it.
    member_req(base, dict(id=rid, expected=1, body="RULE_ACTIVE_V2 · 标明审批依据"))
    denied(409, lambda: member_req(base, dict(id=rid, expected=1, body="过时草稿")))
    assert rule()["publishedBody"] == first["body"]
    live, pending = plan(), plan()
    assert req(pending)["plan"]["rules"][0]["body"] == first["body"]
    application = req(
        "services/" + service + "/applications", dict(name="Rule verification", days=1)
    )
    headers = {
        "Authorization": "Bearer " + application["token"],
        "Idempotency-Key": "rule-service",
    }
    route = f"v1/services/{service}/runs"
    body = dict(version=1, input="核对发布规则", authorizeCalls=1)

    def suggest(path, key):
        return req(
            path + "/suggestions",
            dict(expected=1, resource=model, revision=1, authorizeOneCall=True),
            headers={"Idempotency-Key": key},
            timeout=40,
        )

    answer = "projects/" + req("projects", dict(goal="核对报价"))["id"]
    waiting = "projects/" + req("projects", dict(goal="WAIT_RULE_CHANGE"))["id"]
    answer_revision = req(answer)["revision"]
    assert answer_revision == 1
    ready = suggest(answer, "rules-answer")
    assert ready["result"]["text"] == "已采用旧版报价要求"
    pool = ThreadPoolExecutor(max_workers=1)
    delayed = pool.submit(suggest, waiting, "rule-inflight-planning")
    try:
        assert planning_received.wait(10)
        start(live, "rule-live")
        assert received.wait(12), "provider did not receive the pinned rule"
        sql(
            f"update rule_versions set expires_at=now()-interval '1 minute' where rule_id='{rid}' and revision=1"
        )
        assert rule()["expired"]
        assert "规则" in denied(409, lambda: suggest(waiting, "expired-suggest"))
        snapshot = req(answer)
        assert (
            snapshot["suggestion"]["status"] == "stale"
            and snapshot["suggestion"]["result"] is None
        )
        assert "已采用旧版报价要求" not in str(snapshot["messages"])
        assert suggest(answer, "rules-answer")["status"] == "stale"
        denied(
            409,
            lambda: req(
                answer + "/adoption",
                dict(expected=snapshot["revision"], request=ready["id"]),
            ),
        )
        assert "规则" in denied(409, lambda: start(pending, "rule-denied"))
        assert "规则" in denied(409, lambda: req(route, body, headers=headers))
        denied(409, lambda: publish(1))
        assert req(pending)["plan"]["rules"] == [
            dict(body=None, available=False, revision=1)
        ]
        publish(2)
    finally:
        release.set()
        pool.shutdown(wait=True)
    assert delayed.result()["status"] == "stale"
    assert waitstage(live, "blocked")["artifacts"] == []
    assert req(pending)["plan"]["rules"][0]["available"] is False
    fresh = req(route, body, headers=headers)
    completed = waitstage("projects/" + fresh["project"], "review")
    assert completed["plan"]["rules"][0]["revision"] == 2
    assert "已核对二版规则" in completed["artifacts"][0]["body"]
    publish(2, True)
    assert not req("projects/" + fresh["project"])["plan"]["rules"][0]["available"]
    assert req(plan())["plan"]["rules"] == []
    return dict(
        input="synthetic rule and controlled HTTPS provider",
        oldPlan=pending.split("/")[-1],
        planningReplay="stale",
        latePlanning="stale",
        expiredAnswerAdoption=409,
        reviewChangesDeadline=False,
        staleReview=409,
        staleDraft=409,
        expiredWeb=409,
        expiredService=409,
        inFlight="blocked after provider response",
        newServiceRevision=2,
        disabledOmitted=True,
    )

"""Exercise the published HTTP surface, including a live resumable notification stream."""

import json
import time
import urllib.request


def verify_service_api(req, origin, service, agent, call, worker, restart):
    desc = "v1/services/" + service
    token = req(
        "services/" + service + "/applications", dict(name="Lifecycle consumer", days=1)
    )
    headers = {"Authorization": "Bearer " + token["token"]}
    spec = req("v1/openapi.json")
    assert spec["openapi"] == "3.1.0" and "/runs/{id}/actions" in spec["paths"]

    def deny(code, fn):
        try:
            fn()
        except RuntimeError as exc:
            assert exc.args[1] == code, exc
        else:
            raise AssertionError("Restricted operation succeeded")

    def submit(key, extra=None):
        return req(
            desc + "/runs",
            call,
            headers={**headers, **(extra or {}), "Idempotency-Key": key},
        )

    def read(route):
        return req(route, headers=headers)

    deny(401, lambda: req(desc))
    info = read(desc)
    assert info["ready"] and info["authorizeCalls"] == 1 and info["steps"][0]["name"]
    second = req(
        "services",
        dict(name="Separate service", agent=agent, instructions="Another audience"),
    )
    deny(404, lambda: read("v1/services/" + second["id"]))
    worker.terminate()
    worker.wait(timeout=20)
    try:
        started = submit("lifecycle")
        path = "v1/runs/" + started["id"]

        def action(key, body, route=path):
            return req(
                route + "/actions", body, headers={**headers, "Idempotency-Key": key}
            )

        state = read(path)
        assert state["version"] == 1 and state["steps"][0]["canSupplement"]
        extra = dict(
            action="supplement",
            step=1,
            expected=state["controlRevision"],
            body="SERVICE_API_INPUT_523",
        )
        receipt = action("more", extra)
        assert receipt == action("more", extra)
        deny(409, lambda: action("more", {**extra, "body": "different"}))
        paused = action(
            "pause",
            dict(
                action="pause",
                expected=receipt["controlRevision"],
                reason="Wait for review",
            ),
        )
        assert read(path)["nextAction"] == "resume_or_cancel"
        action(
            "continue",
            dict(
                action="resume",
                expected=paused["controlRevision"],
                reason="Conditions restored",
            ),
        )
        separate = submit("cancel-only")
        other = "v1/runs/" + separate["id"]
        cancel = dict(
            action="cancel",
            expected=read(other)["controlRevision"],
            reason="No longer required",
        )
        assert action("cancel", cancel, other) == action("cancel", cancel, other)
        assert read(other)["status"] == "cancelled"
        removed = req(
            "services/" + service + "/applications",
            dict(name="Grant under review", days=1),
        )
        borrowed = req(
            "services/" + service + "/delegate", dict(app=removed["id"], hours=1)
        )
        restricted = {
            "Authorization": "Bearer " + removed["token"],
            "X-Alliance-Delegation": borrowed["token"],
        }
        preaccepted = submit("queued-before-removal", restricted)
        team = next(t["id"] for t in req("teams") if t["default"])
        req(
            "admin/teams/" + team + "/members/" + removed["id"],
            dict(joined=False, reason="结束应用的团队资源授权"),
            method="PUT",
        )
        deny(401, lambda: req("v1/runs/" + preaccepted["id"], headers=restricted))
        assert any(
            a["id"] == removed["id"] and a["state"] == "unavailable"
            for a in req("services/" + service + "/access")["applications"]
        )
    finally:
        restart()
    for _ in range(120):
        state = read(path)
        if state["stage"] == "review":
            break
        time.sleep(0.2)
    assert state["nextAction"] == "review_artifacts", state
    for _ in range(80):
        stopped = req("projects/" + preaccepted["project"])
        if stopped["run"]["status"] == "blocked":
            break
        time.sleep(0.2)
    assert (
        stopped["run"]["status"] == "blocked" and stopped["costSummary"]["calls"] == 0
    )
    assert not stopped["artifacts"]
    req("applications/" + removed["id"], method="DELETE")

    item = state["steps"][0]
    assert not item["canSupplement"] and item["version"] == 1 and not item["approved"]
    deny(409, lambda: action("late", {**extra, "expected": state["controlRevision"]}))
    suggestion = dict(
        action="propose_revision",
        artifact=item["artifact"],
        version=1,
        body="请注明资料日期",
        position="正文",
    )
    receipt = action("date", suggestion)
    assert receipt == action("date", suggestion)
    deny(409, lambda: action("old", {**suggestion, "version": 2}))
    deny(
        422,
        lambda: action(
            "cannot-approve",
            dict(action="approve", artifact=item["artifact"], version=1),
        ),
    )
    project = "projects/" + started["project"]
    artifact = req(project)["artifacts"][0]
    assert (
        "SERVICE_API_INPUT_523" in artifact["body"] and len(artifact["feedback"]) == 1
    )
    deny(409, lambda: read(path + "/delivery"))
    events = read(path + "/events")["events"]
    assert events and [e["seq"] for e in events] == list(range(1, len(events) + 1))
    assert any(e["kind"] == "feedback" for e in events)
    cursor = events[-1]["seq"]
    assert not read(path + "/events?after=" + str(cursor))["events"]
    request = urllib.request.Request(
        origin + "/api/" + path + "/events",
        headers={
            **headers,
            "Accept": "text/event-stream",
            "Last-Event-ID": str(cursor),
        },
    )
    with urllib.request.urlopen(request, timeout=8) as stream:
        assert stream.headers.get_content_type() == "text/event-stream"
        req(project + "/artifacts/" + item["artifact"] + "/review", dict(version=1))
        while True:
            line = stream.readline()
            assert line, "SSE ended before review notification"
            if line.startswith(b"data:"):
                event = json.loads(line[5:])
                break
        assert event["state"] == "approved" and event["seq"] == cursor + 1
    assert read(path)["nextAction"] == "confirm_delivery"
    req(project + "/deliver", dict(expected=req(project)["revision"]))
    assert (
        read(path)["nextAction"] == "download"
        and read(path + "/delivery")["items"][0]["version"] == 1
    )
    tail = read(path + "/events?after=" + str(cursor))["events"]
    assert any(e["state"] == "delivered" for e in tail)
    assert "SERVICE_API_INPUT_523" not in json.dumps(events + tail)
    delegation = req(
        "services/" + service + "/delegate", dict(app=token["id"], hours=1)
    )
    delegated = {**headers, "X-Alliance-Delegation": delegation["token"]}
    deny(404, lambda: req(path + "/events", headers=delegated))
    delegatedRun = submit("member-run", delegated)
    channel = origin + "/api/v1/runs/" + delegatedRun["id"] + "/events"
    with urllib.request.urlopen(
        urllib.request.Request(
            channel, headers={**delegated, "Accept": "text/event-stream"}
        ),
        timeout=8,
    ) as stream:
        while True:
            line = stream.readline()
            assert line, "SSE closed before first heartbeat"
            if line.startswith(b": connected"):
                break
        req("delegations/" + delegation["id"], method="DELETE")
        start = time.monotonic()
        stream.read()
        assert time.monotonic() - start < 6, "revoked stream remained open"
    req("applications/" + token["id"], method="DELETE")
    deny(401, lambda: read(path))
    return dict(
        run=started["id"],
        serviceVersion=1,
        originalInputPreserved=True,
        supplementApplied=True,
        idempotentFeedback=receipt["feedback"],
        cancelled=separate["id"],
        reviewEvent=event,
        replayedAfterCursor=len(tail),
        liveDelegationRevocation=True,
        queuedRunRecheckedAppTeamGrant=True,
        deliveryVersion=1,
    )

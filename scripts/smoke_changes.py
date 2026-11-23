"""Integration probes for scoped regeneration against a disposable project."""

import time


def verify_changes(req, writer, checker, guest, member, received, release):
    pid = req("projects", {"goal": "Operations handover"})["id"]
    base = "projects/" + pid
    req(
        base + "/plan",
        dict(
            expected=1,
            brief="Prepare an operations handover",
            agents=[checker, writer, checker],
            dependencies=[[], [], [2]],
        ),
    )
    original = req(
        base + "/runs",
        dict(expected=req(base)["revision"], authorizeCalls=3),
        headers={"Idempotency-Key": "handover"},
    )["id"]

    def wait_for(predicate):
        for _ in range(100):
            state = req(base)
            if predicate(state):
                return state
            time.sleep(0.2)
        raise AssertionError("change run did not reach the expected state")

    state = wait_for(lambda p: p["stage"] == "review")
    for a in state["artifacts"]:
        req(base + "/artifacts/" + a["id"] + "/review", dict(version=1))
    item = state["artifacts"][1]["id"]
    path = base + "/artifacts/" + item
    req(
        base + "/participants",
        dict(expected=req(base)["revision"], member=member, review=False),
    )
    first = req(
        path + "/feedback",
        dict(
            version=1,
            body="Verify the revised handover date",
            position="Introduction",
            kind="change",
        ),
    )["id"]
    start = path + "/feedback/" + first + "/change-runs"
    payload = dict(version=1, expected=1, authorizeOneCall=True, excerpt="passed")
    try:
        req(
            start,
            {**payload, "excerpt": "not present"},
            headers={"Idempotency-Key": "bad-excerpt"},
        )
        raise AssertionError("invalid excerpt accepted")
    except RuntimeError as error:
        assert error.args[1] == 422
    hdr = {"Idempotency-Key": "handover-change"}
    try:
        req(start, payload, headers=hdr, session=guest)
        raise AssertionError("commenter could start billable work")
    except RuntimeError as error:
        assert error.args[1] == 403
    accepted = req(start, payload, headers=hdr)
    assert req(start, payload, headers=hdr) == accepted
    state = wait_for(lambda p: p["artifacts"][1]["version"] == 2)
    assert state["run"]["id"] == original and state["stage"] == "review"
    assert [a["version"] for a in state["artifacts"]] == [1, 2, 2]
    assert state["artifacts"][0]["approved"]
    assert state["artifacts"][1]["feedback"][0]["state"] == "verification"
    assert state["artifacts"][1]["body"] == "RECEIPT Agent handover draft"
    costs = [c for c in state["costDetails"] if c["scope"] == "revision"]
    assert len(costs) == 1 and costs[0]["step"] == 2
    req(
        path + "/feedback/" + first,
        dict(
            version=2,
            expected=2,
            action="resolve",
            reason="Reviewer checked the revised date",
        ),
    )

    second = req(
        path + "/feedback",
        dict(version=2, body="HOLD_REVISION: shorten the summary", kind="change"),
    )["id"]
    res = req(
        path + "/feedback/" + second + "/change-runs",
        dict(version=2, expected=1, authorizeOneCall=True),
        headers={"Idempotency-Key": "concurrent-revision"},
    )
    try:
        assert received.wait(15), "provider did not receive the scoped revision"
        req(
            path + "/revise",
            dict(
                version=2,
                body="Human correction",
                reason="Updated by hand while Agent was running",
            ),
        )
    finally:
        release.set()
    state = wait_for(
        lambda p: p["artifacts"][1]["feedback"][1]["changes"][0]["disposition"]
        == "conflict"
    )
    target = state["artifacts"][1]
    assert target["body"] == "Human correction" and target["version"] == 3
    assert target["feedback"][1]["changes"][0]["body"] == "Agent handover draft"
    decision = base + "/change-runs/" + res["run"] + "/decision"
    try:
        req(decision, dict(version=2, expected=1, action="apply", reason="stale"))
        raise AssertionError("stale decision accepted")
    except RuntimeError as error:
        assert error.args[1] == 409
    req(
        decision,
        dict(
            version=3,
            expected=1,
            action="merge",
            reason="Keep human date and agent summary",
            mergedBody="Merged handover summary",
        ),
    )
    state = req(base)
    assert [a["version"] for a in state["artifacts"]] == [1, 4, 4] and state[
        "artifacts"
    ][0]["approved"]
    assert state["artifacts"][1]["feedback"][1]["state"] == "verification"
    assert req(path + "/versions?version=3")[0]["body"] == "Human correction"
    assert req(path + "/versions?version=4")[0]["body"] == "Merged handover summary"
    assert len([c for c in state["costDetails"] if c["scope"] == "revision"]) == 2

    return pid


def verify_office_change(req, client, origin):
    import io
    import zipfile

    agent = req(
        "agents/tools",
        dict(
            name="培训讲义排版",
            description="按正文排版讲义",
            tool=dict(operation="docx", config={}),
        ),
    )["id"]
    validation = req(
        "agents/" + agent + "/validate",
        dict(
            revision=1,
            input="Training sample",
            expected="Training sample",
            authorizeOneCall=True,
        ),
    )
    assert validation["status"] == "passed", validation
    req("agents/" + agent + "/publish", dict(revision=1))
    base = "projects/" + req("projects", dict(goal="Staff training"))["id"]
    req(base + "/plan", dict(expected=1, brief="Initial lesson", agents=[agent]))
    req(
        base + "/runs",
        dict(expected=req(base)["revision"], authorizeCalls=1),
        headers={"Idempotency-Key": "training"},
    )

    def wait_for(version):
        for _ in range(100):
            p = req(base)
            if p["artifacts"] and p["artifacts"][0]["version"] == version:
                return p["artifacts"][0]
            time.sleep(0.2)
        raise AssertionError("Office version did not arrive")

    item = wait_for(1)
    path = base + "/artifacts/" + item["id"]
    f = req(
        path + "/feedback",
        dict(kind="change", version=1, body="Update the handover instructions"),
    )["id"]
    req(
        path + "/feedback/" + f + "/change-runs",
        dict(
            expected=1,
            version=1,
            authorizeOneCall=True,
            replacement="Pass the device to the duty manager",
        ),
        headers={"Idempotency-Key": "training-revision"},
    )
    updated = wait_for(2)
    assert (
        updated["file"] == "deliverable.docx"
        and updated["feedback"][0]["state"] == "verification"
    )
    for version, expected in [
        (1, b"Initial lesson"),
        (2, b"Pass the device to the duty manager"),
    ]:
        with client.open(
            origin + "/api/" + path + "/file?version=" + str(version)
        ) as response:
            with zipfile.ZipFile(io.BytesIO(response.read())) as document:
                assert expected in document.read("word/document.xml")

    return base.split("/")[-1]

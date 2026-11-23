"""Project and team budget checks through independently authenticated product calls."""

from smoke_budget_requests import verify_increases
from concurrent.futures import ThreadPoolExecutor
import time


def verify_budgets(req, team, guest, person, endpoint, unknown_agent):
    team_path = "teams/" + team
    manager = req("me")["id"]

    def denied(fn, code):
        try:
            fn()
        except RuntimeError as e:
            assert e.args[1] == code, e
            return e.args[2]
        raise AssertionError("call unexpectedly succeeded")

    def appoint(old, new):
        req(
            team_path + "/budget-owner",
            dict(previous=old, member=new, reason="test delegation"),
            method="PUT",
        )

    def settings(expected, limit):
        return dict(
            expected=expected,
            limit=limit,
            warningPercent=80,
            reason="synthetic project ceiling",
        )

    def set_limit(path, revision, limit, session=None):
        req(path + "/budget", settings(revision, limit), method="PUT", session=session)

    model = req(
        "resources",
        dict(
            name="Estimated receipt",
            endpoint=endpoint,
            model="fixture",
            key="local-fixture",
            estimate="0.100001",
        ),
    )["id"]
    agent = req(
        "agents",
        dict(
            name="Bounded receipt",
            instructions="Give receipt",
            resource=model,
            resourceRevision=1,
        ),
    )["id"]
    payload = dict(
        revision=1, input="sample", expected="RECEIPT", authorizeOneCall=True
    )
    req("agents/" + agent + "/validate", payload)
    req("agents/" + agent + "/publish", dict(revision=1))
    project = req("projects", dict(goal="Two exact reservations"))["id"]
    path = "projects/" + project
    req(path + "/plan", dict(expected=1, brief="Produce receipt", agents=[agent]))

    def trial(key, included=True):
        return req(
            "agents/" + agent + "/validate",
            {**payload, **({"project": project} if included else {})},
            headers={"Idempotency-Key": key},
        )

    # A delegated financial manager can allocate money without project membership or access to work.
    appoint(manager, person["id"])
    denied(lambda: req(path, session=guest), 404)
    denied(lambda: set_limit(path, 0, "50"), 403)
    denied(lambda: req("admin/budget"), 404)
    set_limit(path, 0, "0.100001", guest)
    denied(lambda: set_limit(path, 0, "5", guest), 409)
    view = req(team_path + "/budget", session=guest)
    row = next(p for p in view["projects"] if p["id"] == project)
    assert row["revision"] == 1 and row["title"] is None
    denied(
        lambda: req(
            "agents/" + unknown_agent + "/validate", dict(payload, project=project)
        ),
        403,
    )

    # Both requests fit individually. Exactly one may reserve the last amount.
    def compete(key):
        try:
            return (key, trial(key)["id"])
        except RuntimeError as e:
            assert e.args[1] == 409 and "项目预算" in e.args[2], e
            return (key, None)

    with ThreadPoolExecutor(max_workers=2) as pool:
        competing = list(pool.map(compete, ["budget-race-a", "budget-race-b"]))
    winners = [(k, v) for k, v in competing if v]
    assert len(winners) == 1
    key, receipt = winners[0]
    assert trial(key)["id"] == receipt
    denied(lambda: trial(key, False), 409)
    quote = req(path)["plan"]["quote"]
    assert (
        quote["projectShort"]
        and not quote["teamShort"]
        and quote["known"] == "0.100001"
        and quote["unknown"] == 0
    )
    assert req(path)["budget"]["exhausted"]
    run = req(
        path + "/runs",
        dict(expected=req(path)["revision"], authorizeCalls=1),
        headers={"Idempotency-Key": "budget-run"},
    )

    def wait(stage):
        for _ in range(120):
            snapshot = req(path)
            if snapshot["stage"] == stage:
                return snapshot
            time.sleep(0.2)
        raise AssertionError(snapshot["stage"])

    held = wait("blocked")
    assert held["costSummary"]["calls"] == 1
    increases = verify_increases(
        req, path, team, guest, person["id"], run["id"], denied, appoint, manager
    )
    # The team's tighter ceiling still rules after a project top-up.
    team_budget = req(team_path + "/budget", session=guest)
    set_limit(team_path, team_budget["revision"], "0", guest)
    assert req(path)["plan"]["quote"]["teamShort"]
    assert "团队预算" in denied(lambda: trial("team-denial"), 409)
    set_limit(team_path, team_budget["revision"] + 1, team_budget["limit"], guest)
    req(
        "runs/" + run["id"] + "/control",
        dict(
            action="resume",
            expected=held["run"]["controlRevision"],
            reason="budget available",
        ),
        headers={"Idempotency-Key": "budget-resume"},
    )
    review = wait("review")
    assert (
        review["costSummary"]["calls"] == 2
        and review["costSummary"]["reserved"] == "0.200002"
    )
    # Settlement may exceed an estimate. It must remain visible and block later work.
    line = next(c for c in review["costDetails"] if c["scope"] == "validation")
    assert line["agent"] == "Bounded receipt" and line["model"] == "fixture"
    bill = dict(
        line="project-budget-line", spend=line["id"], amount="0.125000", final=True
    )
    req("admin/bills", bill, session=guest)
    req("admin/bills", bill, session=guest)
    totals = req(path)["costSummary"]
    assert totals["recorded"] == "0.125000" and totals["reserved"] == "0.100001"
    denied(lambda: trial("after-overrun"), 409)
    later = req(
        path + "/budget-requests",
        dict(
            scope="project",
            team=team,
            amount="1",
            reason="another copy",
            work="extra revision",
            alternative="deliver current version",
            expected=2,
        ),
        headers={"Idempotency-Key": "request-before-delivery"},
    )["id"]
    existing = next(
        x
        for x in req(path + "/budget-requests")["items"]
        if x["state"] == "approved" and x["scope"] == "project"
    )
    assert not existing["canResume"]
    req(path + "/artifacts/" + review["artifacts"][0]["id"] + "/review", {"version": 1})
    req(path + "/deliver", {"expected": req(path)["revision"]})
    ended = next(x for x in req(path + "/budget-requests")["items"] if x["id"] == later)
    assert (
        ended["state"] == "closed"
        and not ended["canApprove"]
        and not ended["canWithdraw"]
    )
    assert req(path)["budget"]["pending"] == 0
    denied(
        lambda: req(
            "budget-requests/" + later + "/decision",
            dict(action="approve", expected=2, reason="late decision"),
            session=guest,
        ),
        409,
    )
    increases["pendingAtDelivery"] = ended["state"]
    # Unknown prices need scoped consent and only proceed below the applicable stop line.
    probe = req("projects", dict(goal="Unknown amount project"))["id"]
    probe_path = "projects/" + probe
    req(
        probe_path + "/plan",
        dict(expected=1, brief="Unknown costs", agents=[unknown_agent]),
    )
    set_limit(probe_path, 0, "0", guest)
    assert (
        req(probe_path)["plan"]["quote"]["unknown"] == 1
        and req(probe_path)["plan"]["quote"]["projectShort"]
    )
    payload_unknown = dict(payload, project=probe)
    denied(lambda: req("agents/" + unknown_agent + "/validate", payload_unknown), 409)
    assert req(probe_path)["costSummary"]["calls"] == 0
    set_limit(probe_path, 1, "1", guest)
    denied(
        lambda: req(
            "agents/" + unknown_agent + "/validate",
            dict(payload_unknown, authorizeOneCall=False),
        ),
        422,
    )
    req("agents/" + unknown_agent + "/validate", payload_unknown)
    assert req(probe_path)["costSummary"]["unknown"] == 1
    # Removing a member clears the finance delegation permanently, even if they rejoin.
    membership = team_path + "/members/" + person["id"]
    req("admin/" + membership, dict(joined=False, reason="team change"), method="PUT")
    req("admin/" + membership, dict(joined=True, reason="return to team"), method="PUT")
    denied(lambda: set_limit(path, 2, "30", guest), 403)
    appoint("", manager)
    denied(lambda: set_limit(path, 2, "30", guest), 403)
    assert req(path)["budget"]["canManage"]
    return dict(
        source="controlled HTTPS model; real database, worker and API",
        project=project,
        increases=increases,
        contendingRequests=2,
        accepted=len(winners),
        projectReservation=review["costSummary"]["reserved"],
        settled=totals,
        checks=[
            "scope authorization",
            "concurrent reservation",
            "replay",
            "team and project ceilings",
            "resume",
            "settlement",
            "delegation revocation",
        ],
    )

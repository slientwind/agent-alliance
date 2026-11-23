"""Resource limits and approvals use the same calls as the project and team ledgers."""

from concurrent.futures import ThreadPoolExecutor
import time


def verify_resource_budgets(req, team, endpoint, external, tool, guest):
    finance = "teams/" + team

    def rows():
        return req(finance + "/budget")["resources"]

    def read(bid):
        return next(x for x in rows() if x["id"] == bid)

    def denied(fn, code):
        try:
            fn()
        except RuntimeError as e:
            assert e.args[1] == code, e
            return e.args[2]
        raise AssertionError("unexpected acceptance")

    def settings(bid, money, count, session=None, revision=None):
        budget = read(bid)
        req(
            finance + "/resource-budgets/" + bid,
            dict(
                limit=money,
                callLimit=count,
                expected=budget["revision"] if revision is None else revision,
                warningPercent=80,
                reason="Resource budget validation",
            ),
            method="PUT",
            session=session,
        )

    def trial(agent, key, pid=""):
        return req(
            "agents/" + agent + "/validate",
            dict(
                revision=1,
                project=pid,
                input="sample",
                expected="RECEIPT",
                authorizeOneCall=True,
            ),
            headers={"Idempotency-Key": key},
        )

    def project(goal):
        return "projects/" + req("projects", dict(goal=goal))["id"]

    model_spec = dict(
        name="Shared spend resource",
        endpoint=endpoint,
        model="fixture",
        key="local-fixture",
        estimate="0.125001",
    )
    model = req("resources", model_spec)["id"]
    bid = next(r["id"] for r in rows() if r["model"] == model)

    def make_agent(name, revision):
        return req(
            "agents",
            dict(
                name=name,
                instructions="Produce receipt",
                resource=model,
                resourceRevision=revision,
            ),
        )["id"]

    a = make_agent("First resource user", 1)
    b = make_agent("Second resource user", 1)
    settings(bid, "0.125001", 10)
    denied(lambda: settings(bid, None, None, guest), 403)
    denied(lambda: settings(bid, None, None, revision=0), 409)

    def enter(agent):
        try:
            return trial(agent, "concurrent-" + agent)["id"]
        except RuntimeError as err:
            assert err.args[1] == 409 and "资源预算" in err.args[2], err

    with ThreadPoolExecutor(max_workers=2) as pool:
        receipts = list(pool.map(enter, [a, b]))
    assert len([x for x in receipts if x]) == 1
    winner, loser = (a, b) if receipts[0] else (b, a)
    assert trial(winner, "concurrent-" + winner)["id"] in receipts
    assert read(bid)["calls"] == 1 and read(bid)["reserved"] == "0.125001"
    settings(bid, "1", 2)
    trial(loser, "concurrent-" + loser)
    for agent in [a, b]:
        req("agents/" + agent + "/publish", dict(revision=1))
    # An edit of the same registered model must not reset its spending history.
    req("resources", dict(model_spec, id=model, expected=1, estimate="0.250000"))
    assert len([r for r in rows() if r["model"] == model]) == 1
    later = make_agent("New resource revision", 2)
    assert "次数" in denied(lambda: trial(later, "new-revision"), 409)
    p = project("short answer")
    suggestion = dict(resource=model, revision=2, expected=1, authorizeOneCall=True)

    def plan():
        return req(
            p + "/suggestions",
            suggestion,
            headers={"Idempotency-Key": "bounded-planner"},
        )

    assert "次数" in denied(plan, 409)
    assert req(p)["costSummary"]["calls"] == 0

    def increase(path, scope, amount):
        info = req(path + "/budget-requests")
        target = next(
            t for t in info["targets"] if t["resource"] == bid and t["scope"] == scope
        )
        request = dict(
            scope=scope,
            resource=bid,
            team=team,
            amount=amount,
            expected=target["revision"],
            reason="Retain pinned resource",
            work="One more call",
            alternative="Manual review",
        )
        denied(
            lambda: req(
                path + "/budget-requests",
                dict(request, resource=external),
                headers={"Idempotency-Key": "wrong-target"},
            ),
            409,
        )
        if scope == "resource_calls":
            denied(
                lambda: req(
                    path + "/budget-requests",
                    dict(request, amount="0.5"),
                    headers={"Idempotency-Key": "fractional"},
                ),
                422,
            )
        item = req(
            path + "/budget-requests",
            request,
            headers={"Idempotency-Key": scope + path},
        )["id"]
        entry = next(r for r in req("budget-inbox") if r["id"] == item)
        assert entry["scope"] == scope and entry["resourceName"] == target["name"]
        detail = req("budget-inbox/" + item)
        assert detail["canApprove"] and detail["revision"] == target["revision"]
        d = dict(
            action="approve",
            expected=target["revision"],
            reason="Agreed resource allocation",
        )
        req("budget-requests/" + item + "/decision", d)
        req("budget-requests/" + item + "/decision", d)

    increase(p, "resource_calls", "1")
    reply = plan()
    assert plan()["id"] == reply["id"]
    assert read(bid)["calls"] == 3 and read(bid)["used"] == "0.500002"
    work = project("Resource-limited execution")
    req(work + "/plan", dict(expected=1, brief="Receipt", agents=[a]))
    assert req(work)["plan"]["quote"]["resourceShort"]
    run = req(
        work + "/runs",
        dict(expected=req(work)["revision"], authorizeCalls=1),
        headers={"Idempotency-Key": "resource-run"},
    )["id"]

    def wait(stage):
        for _ in range(130):
            state = req(work)
            if state["stage"] == stage:
                return state
            time.sleep(0.2)
        raise AssertionError(state["stage"])

    held = wait("blocked")
    assert read(bid)["calls"] == 3
    settings(bid, "0.500002", 4)
    assert req(work)["plan"]["quote"]["resourceShort"]
    increase(work, "resource", "0.125001")
    assert not req(work)["plan"]["quote"]["resourceShort"]
    req(
        "runs/" + run + "/control",
        dict(
            action="resume",
            reason="Resource approved",
            expected=held["run"]["controlRevision"],
        ),
        headers={"Idempotency-Key": "resource-resume"},
    )
    assert wait("review")["costSummary"]["calls"] == 1
    stamped = [x for x in req(finance + "/budget")["items"] if x["resource"] == bid]
    assert sorted(x["sourceRevision"] for x in stamped) == [1, 1, 1, 2] and all(
        x["sourceName"] == "fixture" for x in stamped
    )
    status = read(bid)
    assert status["calls"] == 4 and status["reserved"] == "0.625003"
    for agent, kind in [(external, "external"), (tool, "tool")]:
        before = next(x for x in rows() if x["agent"] == agent and x["kind"] == kind)
        assert before["calls"] > 0
        settings(before["id"], None, before["calls"])
        assert "次数" in denied(lambda: trial(agent, "cap-" + agent), 409)
        assert read(before["id"])["calls"] == before["calls"]
        settings(before["id"], None, None)
    # Zero-price local work remains possible when a paid resource has pushed the team over its currency ceiling.
    team_before = req(finance + "/budget")
    req(
        finance + "/budget",
        dict(
            expected=team_before["revision"],
            limit="0",
            warningPercent=80,
            reason="Money stop",
        ),
        method="PUT",
    )
    try:
        assert "团队预算" in denied(lambda: trial(a, "team-stop"), 409)
        free = req(
            "agents/" + tool + "/validate",
            dict(
                revision=1,
                input="region,amount,status,id\nEast,90.30,ok,001",
                expected="90.30",
                authorizeOneCall=True,
            ),
        )
        assert free["status"] == "passed"
    finally:
        req(
            finance + "/budget",
            dict(
                expected=team_before["revision"] + 1,
                limit=team_before["limit"],
                warningPercent=team_before["warningPercent"],
                reason="Restore team ceiling",
            ),
            method="PUT",
        )
    return dict(
        scope="real database, API and worker with controlled HTTPS provider",
        competingTrials=2,
        acceptedTrials=1,
        modelRevisions=2,
        modelCalls=status["calls"],
        reservedCNY=status["reserved"],
        approvals=["resource_calls", "resource"],
        limitsChecked=["model", "tool", "external"],
        resumedRun=run,
        project=work.split("/")[-1],
    )

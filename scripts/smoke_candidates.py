"""Check project-scoped candidate ranking against published revisions and live budgets."""

from concurrent.futures import ThreadPoolExecutor
import http.cookiejar
from decimal import Decimal
import urllib.request


def verify_candidates(req, env, endpoint, planner_model, team, hold, received, release):
    project = req("projects", dict(goal="COMPARE_RANK_PROBE 比较培训材料的能力"))["id"]
    path = "projects/" + project
    resource = req(
        "resources",
        dict(
            name="Candidate pricing",
            model="fixture",
            endpoint=endpoint,
            key="local-fixture",
            estimate="0.015001",
        ),
    )["id"]
    cfg = dict(
        resource=resource,
        resourceRevision=1,
        name="培训摘要助手",
        instructions="整理培训讲义的摘要文本",
    )
    agent = req("agents", cfg)["id"]
    ap = "agents/" + agent
    req(
        ap + "/checks/1",
        dict(
            version=0,
            items=[
                dict(
                    name="培训要点覆盖",
                    sample="synthetic training",
                    expected="RECEIPT",
                    match="contains",
                    reviewer="",
                )
            ],
        ),
        "PUT",
    )
    check = req(ap + "/checks/1")["items"][0]
    proof = req(
        ap + "/validate",
        dict(
            revision=1,
            checkID=check["id"],
            input=check["sample"],
            expected="RECEIPT",
            authorizeOneCall=True,
        ),
    )
    assert proof["status"] == "passed"
    req(ap + "/publish", dict(revision=1))
    req(
        "agents",
        dict(
            **{**cfg, "name": "新版名称仅供草稿", "instructions": "另一种工作要求"},
            id=agent,
            expected=1
        ),
    )
    ranked = req(
        path
        + "/candidates?"
        + urllib.parse.urlencode(
            dict(q="请核实培训要点覆盖", output="text", priority="cost")
        )
    )
    selected = ranked[0]
    assert (
        selected["id"] == agent
        and selected["revision"] == 1
        and selected["name"] == cfg["name"]
    ), selected
    assert (
        selected["state"] == "ready"
        and selected["match"] == "check"
        and selected["verifiedChecks"] == ["培训要点覆盖"]
    )
    assert (
        selected["estimateCny"] == "0.015001"
        and selected["trialCount"] == 1
        and selected["medianSeconds"] >= 0
        and selected["lastVerified"]
    )
    formats = req(path + "/candidates?output=xlsx&priority=time")
    assert next(r for r in formats if r["id"] == agent)["state"] == "unsuitable"
    limited = next(
        (
            r
            for r in formats
            if r["state"] == "ready" and r["operation"] in ("aggregate", "xlsx")
        ),
        None,
    )
    assert limited, "missing real spreadsheet candidate"
    for priority in ("cost", "time"):
        comparable = [
            row
            for row in req(path + "/candidates?priority=" + priority)
            if row["state"] == "ready"
            and not row["needsReview"]
            and not row.get("match")
        ]
        values = [
            (
                (
                    Decimal(row["estimateCny"])
                    if row["estimateCny"] is not None
                    else None
                )
                if priority == "cost"
                else row["medianSeconds"]
            )
            for row in comparable
        ]
        assert len(values) >= 2 and values == sorted(
            values, key=lambda v: (v is None, v or 0)
        ), (priority, values)
    # A returned employee has no team grants. Project participation alone must not grant catalog access.
    visitor = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
    )
    email = "preference-owner@sample.invalid"
    req(
        "login",
        dict(email=email, password=env["PREFERENCE_BROWSER_PASSWORD"]),
        session=visitor,
    )
    try:
        req(path + "/candidates", session=visitor)
        raise AssertionError("visible without project access")
    except RuntimeError as e:
        assert e.args[1] == 404
    req(
        path + "/participants",
        dict(email=email, expected=req(path)["revision"], review=False),
    )
    assert req(path + "/candidates", session=visitor) == []

    # The budget owner is explicitly granted for the default team by the main suite.
    def set_limit(amount):
        value = req(path)["budget"]
        req(
            path + "/budget",
            dict(
                expected=value["revision"],
                limit=amount,
                warningPercent=80,
                reason="synthetic comparison",
            ),
            "PUT",
        )

    set_limit("0")
    blocked = next(r for r in req(path + "/candidates") if r["id"] == agent)
    assert blocked["projectShort"] and blocked["state"] == "conditional"
    set_limit(None)

    def suggest(key):
        return req(
            path + "/suggestions",
            dict(
                expected=req(path)["revision"],
                resource=planner_model,
                revision=1,
                authorizeOneCall=True,
            ),
            headers={"Idempotency-Key": key},
        )

    team_path = "teams/" + team
    payer = next(
        b for b in req(team_path + "/budget")["resources"] if b["model"] == resource
    )

    def resource_limit(count, revision):
        req(
            team_path + "/resource-budgets/" + payer["id"],
            dict(
                expected=revision,
                limit=None,
                callLimit=count,
                warningPercent=80,
                reason="Regression fixture",
            ),
            "PUT",
        )

    resource_limit(0, payer["revision"])
    try:
        assert suggest("omit-blocked")["result"]["kind"] == "question"
    finally:
        resource_limit(None, payer["revision"] + 1)
    proposed = suggest("published-proof")["result"]
    assert (
        proposed["agents"] == [agent]
        and proposed["revisions"] == [1]
        and len(proposed["reasons"]) == 1
    )
    prior = req(path)["revision"]
    with ThreadPoolExecutor(max_workers=1) as pool:
        hold.set()
        future = pool.submit(suggest, "recheck-after-model")
        try:
            assert received.wait(6)
            resource_limit(0, payer["revision"] + 2)
        finally:
            hold.clear()
            release.set()
        late = future.result(timeout=10)
        assert late["status"] == "stale" and late.get("result") is None
    assert req(path)["revision"] == prior
    resource_limit(None, payer["revision"] + 3)
    env["CANDIDATE_PROJECT"] = project
    env["CANDIDATE_AGENT"] = agent
    return dict(
        publishedName=selected["name"],
        publishedRevision=selected["revision"],
        draftRevision=2,
        matchedChecks=selected["verifiedChecks"],
        estimate=selected["estimateCny"],
        formatMismatch="unsuitable",
        projectBudget="conditional",
        projectOnlyVisibleCount=0,
        planningPinsPublishedVersion=True,
        planningExcludesBudgetBlocked=True,
        changedDuringPlanning="stale",
    )

"""Project-scoped use from a second team through a real worker and synthetic provider."""

import http.cookiejar
import json
import subprocess
import time
import urllib.request
from uuid import UUID

from smoke_borrowed_connectors import verify_borrowed_connectors


def verify_project_agent_grants(
    req, env, frontend, port, report_dir, gates, pg, db, remote
):
    def account(email):
        session = urllib.request.build_opener(
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
        )
        req(
            "register",
            dict(
                email=email,
                name=email.split("@")[0],
                password=env["BROWSER_PASSWORD"],
                token=req("admin/invitations", {})["token"],
            ),
            session=session,
        )
        return session

    borrower = account("grant-requester@sample.invalid")
    payer = account("grant-payer@sample.invalid")
    team = req("admin/teams", dict(name="培训内容提供方"))["id"]
    person = req("me", session=payer)["id"]
    req(
        "admin/teams/" + team + "/members/" + person,
        dict(joined=True, reason="负责培训费用"),
        "PUT",
    )
    req(
        "teams/" + team + "/budget-owner",
        dict(member=person, previous="", reason="项目费用审批"),
        "PUT",
    )
    resource = req(
        "resources",
        dict(
            team=team,
            name="培训模型",
            endpoint=f"https://localhost:{port}/v1",
            model="fixture",
            key="synthetic",
            estimate="0.012300",
        ),
    )["id"]
    config = dict(
        name="培训摘要校订助手",
        resource=resource,
        resourceRevision=1,
        instructions="PRIVATE_PROJECT_GRANT_INSTRUCTIONS",
    )
    agent = req("agents", config)["id"]
    ap = "agents/" + agent

    def publish(rev):
        proof = req(
            ap + "/validate",
            dict(
                revision=rev,
                input="synthetic",
                expected="RECEIPT",
                authorizeOneCall=True,
            ),
        )
        assert proof["status"] == "passed", proof
        req(ap + "/publish", dict(revision=rev))

    publish(1)

    def get(path, *args, **kwargs):
        return req(path, *args, session=borrower, **kwargs)

    def denied(op, code):
        try:
            op()
        except RuntimeError as error:
            assert error.args[1] == code, error
        else:
            raise AssertionError(f"Expected {code}")

    project = get("projects", dict(goal="为新同事整理内部培训材料"))["id"]
    path = "projects/" + project
    other = "projects/" + get("projects", dict(goal="另一个未授权的项目"))["id"]
    get(
        path + "/participants",
        dict(
            email="review@sample.invalid",
            expected=get(path)["revision"],
            review=False,
            reason="邀请提供方参与",
        ),
    )
    auxiliary = get("projects", dict(goal="并行采购的受限项目"))["id"]
    aux_path = "projects/" + auxiliary
    get(aux_path + "/participants", dict(email="review@sample.invalid", expected=get(aux_path)["revision"], review=False))
    aux_grant = req(aux_path+"/agent-grants",dict(agent=agent,revision=1,description="核对另一份采购申请",days=1,calls=2))["id"]
    assert agent not in [a["id"] for a in get("agents")]
    assert agent not in [a["id"] for a in get(path + "/candidates")]
    subprocess.run(
        ["node", str(frontend / "tests/project-agent-grants.mjs")],
        env={
            **env,
            "GRANT_PROJECT": project,
            "GRANT_AUXILIARY": auxiliary,
            "GRANT_TEAM": team,
            "GRANT_AGENT": agent,
            "VISUAL_EVIDENCE": str(report_dir),
        },
        check=True,
        timeout=100,
    )
    # Terminal projects cannot be approved even when the user still holds the finance role.
    # This isolated legacy row keeps the pending grant while the project is closed.
    subprocess.run(pg+["psql","-v","ON_ERROR_STOP=1","-U","alliance","-d",db,"-c",
        f"update projects set stage='cancelled' where id='{UUID(auxiliary)}'"],check=True,stdout=subprocess.DEVNULL)
    inbox = "project-agent-grants/inbox"
    assert all(g["id"]!=aux_grant for g in req(inbox,session=payer))
    denied(lambda: req("project-agent-grants/"+aux_grant,dict(action="budget",expected=1,reason="结束后尝试"),session=payer),409)
    assert req(aux_path+"/agent-grants")[0]["generation"]==1
    grant = get(path + "/agent-grants")[0]
    gp = "project-agent-grants/" + grant["id"]
    assert (
        grant["resourceApproved"]
        and grant["budgetApproved"]
        and grant["generation"] == 3
    )
    listing = next(
        g for g in req("project-agent-grants", session=payer) if g["id"] == grant["id"]
    )
    assert listing["projectName"] is None
    denied(lambda: req(path, session=payer), 404)
    denied(lambda: get(gp, dict(action="budget", expected=3, reason="无承担权")), 403)
    denied(lambda: req(gp, dict(action="revoke", expected=2, reason="旧版本")), 409)
    assert agent not in [a["id"] for a in get(other + "/candidates")]
    assert agent not in [a["id"] for a in get("agents")]
    cand = next(c for c in get(path + "/candidates") if c["id"] == agent)
    assert cand["state"] == "ready" and "PRIVATE_PROJECT" not in json.dumps(cand)
    denied(
        lambda: get(
            ap + "/validate",
            dict(revision=1, input="try", expected="RECEIPT", authorizeOneCall=True),
        ),
        404,
    )
    denied(
        lambda: get(
            other + "/plan",
            dict(expected=get(other)["revision"], brief="试用", agents=[agent]),
        ),
        422,
    )
    run = get(
        path + "/runs",
        dict(expected=get(path)["revision"], authorizeCalls=1),
        headers={"Idempotency-Key": "grant-launch"},
    )["id"]
    entered, release = gates
    try:
        assert entered.wait(25), get(path)["run"]
        req(gp, dict(action="revoke", expected=3, reason="在途撤权"))
        assert agent not in [a["id"] for a in get(path + "/candidates")]
    finally:
        release.set()

    def wait_for(state):
        for _ in range(140):
            view = get(path)
            if view["run"]["status"] == state or view["stage"] == state:
                return view
            time.sleep(0.2)
        raise AssertionError(view["run"])

    blocked = wait_for("blocked")
    assert not blocked["artifacts"] and blocked["costSummary"]["calls"] == 1

    def approve_again():
        grant2 = req(
            path + "/agent-grants",
            dict(
                agent=agent, revision=1, description="继续核对原回执", days=2, calls=1
            ),
        )["id"]
        endpoint = "project-agent-grants/" + grant2
        req(endpoint, dict(action="resource", expected=1, reason="恢复使用"))
        req(
            endpoint,
            dict(action="budget", expected=2, reason="恢复承担"),
            session=payer,
        )

    # Borrowers who omit a revision must still get the approved version after a supplier upgrade.
    get(
        other + "/participants",
        dict(
            email="review@sample.invalid", expected=get(other)["revision"], review=False
        ),
    )
    g2 = req(
        other + "/agent-grants",
        dict(agent=agent, revision=1, description="限额验证", days=1, calls=1),
    )["id"]
    gp2 = "project-agent-grants/" + g2
    req(gp2, dict(action="resource", expected=1, reason="限额验证"))
    assert agent not in [a["id"] for a in get(other + "/candidates")]
    req(gp2, dict(action="budget", expected=2, reason="仅一次调用"), session=payer)
    req("agents", dict(**config, id=agent, expected=1))
    publish(2)
    approve_again()
    get(
        "runs/" + run + "/control",
        dict(
            action="resume",
            expected=blocked["run"]["controlRevision"],
            reason="核对原结果",
        ),
        headers={"Idempotency-Key": "grant-resume"},
    )
    done = wait_for("review")
    assert done["costSummary"]["calls"] == 1 and len(done["artifacts"]) == 1
    assert (
        len(done["costDetails"]) == 1
        and done["costDetails"][0]["payer"] == "培训内容提供方"
    )
    assert sorted(g["used"] for g in get(path + "/agent-grants")) == [0, 1]
    item = done["artifacts"][0]
    get(path + "/artifacts/" + item["id"] + "/review", dict(version=1))
    get(path + "/deliver", dict(expected=get(path)["revision"]))
    assert get(path + "/delivery")["items"][0]["version"] == 1
    get(
        other + "/plan",
        dict(
            expected=get(other)["revision"],
            brief="两个摘要步骤",
            agents=[agent, agent],
            dependencies=[[], []],
        ),
    )
    second = get(other)
    assert second["plan"]["quote"]["grantShort"]
    assert [n["revision"] for n in second["plan"]["nodes"]] == [1, 1]
    get(
        other + "/runs",
        dict(expected=second["revision"], authorizeCalls=2),
        headers={"Idempotency-Key": "grant-parallel"},
    )
    for _ in range(120):
        detail = get(other)
        if (
            detail["run"]["status"] == "blocked"
            and get(other + "/agent-grants")[0]["used"] == 1
        ):
            break
        time.sleep(0.2)
    assert (
        detail["run"]["status"] == "blocked" and detail["costSummary"]["calls"] == 1
    ), detail["run"]
    # Even an enterprise administrator cannot turn single-project use into a service.
    req(
        "admin/members/" + get("me")["id"],
        dict(
            role="admin",
            enabled=True,
            password=env["BROWSER_PASSWORD"],
            reason="验证项目授权与服务发布的区别",
        ),
        "PATCH",
    )
    req(
        "login",
        dict(email="grant-requester@sample.invalid", password=env["BROWSER_PASSWORD"]),
        session=borrower,
    )
    denied(
        lambda: get(
            "services",
            dict(name="受限能力发布检查", project=project, instructions="用于内部培训"),
        ),
        409,
    )
    assert agent not in [a["id"] for a in get("agents")]
    # Advance only this synthetic grant's expiry, in the disposable database.
    expires = f"update project_agent_grants set expires_at=now()-interval '1 second' where id='{UUID(g2)}'"
    subprocess.run(
        pg
        + ["psql", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db, "-c", expires],
        check=True,
        stdout=subprocess.DEVNULL,
    )
    assert get(other + "/agent-grants")[0]["closed"] and not get(other)["sharedAgents"]
    assert all(g["id"]!=g2 for g in req(inbox,session=payer))
    assert agent not in [a["id"] for a in get(other + "/candidates")]
    (report_dir / "project-agent-grants.json").write_text(
        json.dumps(
            dict(
                data="synthetic",
                browserRoles=["requester", "resource admin", "payer"],
                provider="local HTTPS fixture",
                runtime="Temporal / Eino / PostgreSQL",
                heldResultRecovered=True,
                chargesForRecoveredRun=1,
                deliveredVersion=1,
                pinnedAfterUpgrade=1,
                payer=done["costDetails"][0]["payer"],
                republishDenied=True,
                expiryRemovedCandidate=True,
                parallelSteps=2,
                parallelChargedCalls=1,
            ),
            indent=2,
        )
    )

    verify_borrowed_connectors(
        req, env, frontend, port, report_dir, team, borrower, payer, remote
    )

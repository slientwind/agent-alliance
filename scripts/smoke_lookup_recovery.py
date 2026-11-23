"""Exercise the real checkpoint between an Agent's search and answer calls."""

import base64
import json
import time
from smoke_receipt_recovery import verify_receipt_recovery


def verify_lookup_recovery(req, endpoint, worker, restart_worker, out, channel=None):
    # A known per-call estimate makes the boundary independently reproducible.
    team = req("admin/teams", dict(name="资料检索恢复验证"))["id"]
    actor = req("me")["id"]
    req(
        "teams/" + team + "/budget-owner",
        dict(member=actor, previous="", reason="独立试验预算"),
        method="PUT",
    )
    model = req(
        "resources",
        dict(
            team=team,
            name="检索预算复验",
            endpoint=endpoint,
            model="source-lookup-fixture",
            key="synthetic",
            estimate="0.20",
        ),
    )
    agent = req(
        "agents",
        dict(
            name="有预算的核对助手",
            instructions="根据资料回答。",
            resource=model["id"],
            resourceRevision=1,
        ),
    )["id"]
    req(
        "agents/" + agent + "/validate",
        dict(revision=1, input="授权验证", expected="RECEIPT", authorizeOneCall=True),
    )
    req("agents/" + agent + "/publish", dict(revision=1))

    def until(project, stage):
        for _ in range(150):
            p = req(project)
            if p["stage"] == stage:
                return p
            time.sleep(0.2)
        raise AssertionError((p["stage"], stage))

    def budget(path, revision, amount):
        req(
            path + "/budget",
            dict(
                expected=revision,
                limit=amount,
                warningPercent=80,
                reason="隔离试验按步放行",
            ),
            method="PUT",
        )

    def prepare(calculate=False):
        goal = "两张运费收据合计多少元？请列出算式。" if calculate else "冷链交接由谁核对铅封？"
        material = ("去程运费为 16.85 元。\n返程运费为 9.20 元。" if calculate else
                    "冷链交接时由值班主管核对铅封编号，温度超过 8°C 时暂停接收。")
        path = (
            "projects/"
            + req("projects", dict(team=team, goal=goal))["id"]
        )
        src = req(
            path + "/sources?name=items.txt",
            material.encode(),
        )
        plan = dict(
            expected=req(path)["revision"],
            brief=goal,
            agents=[agent],
            agentRevisions=[1],
            sourceLookup=True,
            calculationSteps=[1] if calculate else [],
            sources=[dict(id=src["id"], version=1)],
        )
        # Invalid expansions must leave the previous plan and revision untouched.
        for patch in [
            {"sourceLookup": False, "lookupSteps": [1, 1]},
            {"sourceLookup": False, "lookupSteps": [0]},
            {"sources": []},
            {"sourceLookup": False, "lookupSteps": [], "calculationSteps": [1]},
            {"calculationSteps": [1,1]},
            {
                "sourceLookup": False,
                "agents": [agent] * 7,
                "agentRevisions": [1] * 7,
                "lookupSteps": list(range(1, 8)),
            },
            {
                "sourceLookup": False,
                "agents": [agent, agent],
                "agentRevisions": [1, 1],
                "lookupSteps": [2],
                "dependencies": [[2], []],
            },
        ]:
            try:
                req(path + "/plan", {**plan, **patch})
                raise AssertionError("invalid plan accepted")
            except RuntimeError as error:
                assert error.args[1] == 422, error
            assert req(path)["revision"] == plan["expected"]
        req(path + "/plan", plan)
        if calculate:
            before=req("services")
            try:
                req("services",dict(name="未确认的核算方案",instructions="发布前核对",project=path.split("/")[1]))
                raise AssertionError("unconfirmed plan published")
            except RuntimeError as error:
                assert error.args[1]==409,error
            assert req("services")==before
        budget(path, 0, "0.20")
        req(
            path + "/runs",
            dict(expected=req(path)["revision"], authorizeCalls=2),
            headers={"Idempotency-Key": path},
        )
        held = until(path, "blocked")
        assert held["costSummary"]["calls"] == 1 and len(held["artifacts"]) == 1
        return path, src["id"], held

    permit, withdraw = prepare(), prepare()
    calc, revoked = prepare(True), prepare(True)
    def stop_with_budget_tasks():
        worker.kill()
        worker.wait(timeout=15)
        for path, source, state in (permit, withdraw, calc, revoked):
            budget(path, 1, "0.40")
            if path in (withdraw[0],revoked[0]):
                req(path + "/sources/" + source, method="DELETE")
            req(
                "runs/" + state["run"]["id"] + "/control",
                dict(
                    action="resume",
                    expected=state["run"]["controlRevision"],
                    reason="核对预算后恢复",
                ),
                headers={"Idempotency-Key": "resume-" + path},
            )
    verify_receipt_recovery(req,team,agent,stop_with_budget_tasks,restart_worker,out)

    ready = until(permit[0], "review")
    held = until(withdraw[0], "blocked")
    assert (
        ready["costSummary"]["calls"] == 2
        and ready["run"]["id"] == permit[2]["run"]["id"]
    )
    assert held["costSummary"]["calls"] == 1 and len(held["artifacts"]) == 1
    assert "原文依据" in ready["artifacts"][1]["body"]
    # Publish the approved plan structure, then run against a fresh caller attachment.
    published = req(
        "services",
        dict(
            name="交接查询服务",
            instructions="根据调用方提供的规程作答",
            project=ready["id"],
        ),
    )
    svc = "services/" + published["id"]
    assert [s["lookupPhase"] for s in req(svc + "/entry")["steps"]] == [
        "search",
        "answer",
    ]
    if channel:
        binding = "channels/feishu/bind"
        if not req(binding)["bound"]:
            channel.reply(channel.send(req, req(binding, {}, method="POST")["command"]))
        preview = channel.reply(
            channel.send(req, "发起 " + published["id"] + " 核对交接规程")
        )
        assert (
            "附上要核对的资料" in preview
            and "?service=" + published["id"] in preview
            and "发送：确认" not in preview
        ), preview
        assert req(svc + "/entry")["recent"] == []
        (out / "agent-source-lookup-channel.json").write_text(
            json.dumps(
                {"privatePreview": preview, "newRuns": 0}, ensure_ascii=False, indent=2
            )
        )
    body = dict(version=1, input="冷链交接由谁核对？", authorizeCalls=2)
    try:
        req(svc + "/chat", body, headers={"Idempotency-Key": "no-attachment"})
        raise AssertionError("expected missing-attachment rejection")
    except RuntimeError as err:
        assert err.args[1] == 422, err
    sourceText = "冷链交接由值班主管核对铅封，超过 8°C 时暂停接收，使用 Z7 交接单。"
    submitted = req(
        svc + "/chat",
        {
            **body,
            "attachment": {
                "name": "service.txt",
                "data": base64.b64encode(sourceText.encode()).decode(),
            },
        },
        headers={"Idempotency-Key": "fresh-lookup"},
    )
    fresh = until("projects/" + submitted["project"], "review")
    assert "Z7" in fresh["artifacts"][1]["body"]
    (out / "agent-source-lookup-recovery.json").write_text(
        json.dumps(
            dict(
                workerExit=worker.returncode,
                resumedCalls=ready["costSummary"]["calls"],
                revokedCalls=held["costSummary"]["calls"],
                revokedStage=held["stage"],
                serviceAnswer=fresh["artifacts"][1]["body"],
            ),
            ensure_ascii=False,
            indent=2,
        )
        + "\n"
    )

    calculated = until(calc[0],"review")
    stopped = until(revoked[0],"blocked")
    assert calculated["run"]["id"] == calc[2]["run"]["id"]
    assert calculated["costSummary"]["calls"] == 2
    assert "合计为 26.05 元" in calculated["artifacts"][1]["body"]
    assert stopped["costSummary"]["calls"] == 1 and len(stopped["artifacts"]) == 1
    service = req("services",dict(name="运费核算服务",instructions="根据本次收据核算运费",project=calculated["id"]))
    entry = "services/"+service["id"]
    phases = [row["lookupPhase"] for row in req(entry+"/entry")["steps"]]
    assert phases == ["search","answer_calc"]
    new_source = "第一张运费收据为 3.45 元。\n第二张运费收据为 7.55 元。"
    other = req(entry+"/chat",dict(version=1,input="两张运费收据合计多少？",authorizeCalls=2,
                attachment=dict(name="本期收据.txt",data=base64.b64encode(new_source.encode()).decode())),
                headers={"Idempotency-Key":"new-calculation-receipts"})
    latest = until("projects/"+other["project"],"review")
    answer = latest["artifacts"][1]["body"]
    assert "合计为 11.00 元" in answer and "本期收据.txt" in answer and "26.05" not in answer
    (out/"source-calculation-recovery.json").write_text(json.dumps(dict(
        phases=phases,resumed=calculated["run"]["id"],resumedAnswer=calculated["artifacts"][1]["body"],
        resumedCalls=calculated["costSummary"]["calls"],withdrawnCalls=stopped["costSummary"]["calls"],
        withdrawnStage=stopped["stage"],serviceAnswer=answer),ensure_ascii=False,indent=2)+"\n")
    return published["id"], agent, service["id"]

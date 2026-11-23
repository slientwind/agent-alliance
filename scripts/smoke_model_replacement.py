"""Replace two model-backed agents while a pinned workflow is in flight."""

import json
import http.cookiejar
from concurrent.futures import ThreadPoolExecutor
import subprocess
import time
import urllib.request


def verify_model_replacement(req, env, frontend, port, directory, gates, pg, db):
    def deny(fn, code):
        try:
            fn()
        except RuntimeError as exc:
            assert exc.args[1] == code, exc
        else:
            raise AssertionError(f"expected HTTP {code}")

    team = req("admin/teams", dict(name="供应商替换业务验收组"))["id"]

    def resource(which, price, **overrides):
        conf = dict(
            name="模型" + which,
            model="replacement-" + which,
            endpoint=f"https://localhost:{port}/{which}/v1",
            team=team,
            key="synthetic-" + which,
            estimate=price,
        )
        return req("resources", {**conf, **overrides})["id"]

    source = resource("old", "0.002000")
    target = resource("new", "0.003000")
    mismatch = resource("image", "0.020000", operation="png")
    cross = resource(
        "cross", "0.001000", team=next(t["id"] for t in req("teams") if t["default"])
    )
    base = "admin/model-resources/" + source + "/revisions/1"

    def create(name, private=False):
        cfg = dict(
            name=name,
            instructions="按所给证据编写摘要",
            resource=source,
            resourceRevision=1,
            key="synthetic-private" if private else "",
        )
        agent = req("agents", cfg)["id"]
        req(
            f"agents/{agent}/checks/1",
            dict(
                version=0,
                items=[
                    dict(
                        name="固定业务样例",
                        sample="采购合同需双方审批",
                        expected="双人审核",
                        match="contains",
                        reviewer="",
                    )
                ],
            ),
            "PUT",
        )
        trial(agent, 1)
        req(f"agents/{agent}/publish", dict(revision=1))
        return agent, cfg

    def trial(agent, rev):
        check = req(f"agents/{agent}/checks/{rev}")["items"][0]
        assert check["sample"] == "采购合同需双方审批"
        reply = req(
            f"agents/{agent}/validate",
            dict(
                revision=rev,
                input=check["sample"],
                expected=check["expected"],
                checkID=check["id"],
                authorizeOneCall=True,
            ),
        )
        assert reply["status"] == "passed", reply
        return reply["id"]

    a, original = create("业务摘要助手", True)
    b, _ = create("发布材料助手")
    custom, cfg = create("已有定制的助手")
    req("agents", dict(cfg, id=custom, expected=1, instructions="企业定制"))
    other = req(
        "services", dict(name="已发布的业务摘要服务", agent=a, instructions="摘要")
    )["id"]

    def launch(title, two_steps=False):
        path = "projects/" + req("projects", dict(team=team, goal=title))["id"]
        req(
            path + "/plan",
            dict(
                expected=req(path)["revision"],
                brief=title,
                agents=[a, a] if two_steps else [a],
                dependencies=[[], [1]] if two_steps else [[]],
            ),
        )
        req(
            path + "/runs",
            dict(expected=req(path)["revision"], authorizeCalls=2 if two_steps else 1),
            headers={"Idempotency-Key": "run-" + title.encode().hex()},
        )
        return path

    def completed(path, body):
        for _ in range(180):
            project = req(path)
            if project["stage"] == "review":
                text = project["artifacts"][0]["body"]
                assert body in text, text
                return text
            time.sleep(0.2)
        raise AssertionError(project["stage"])

    seed = dict(
        target=target,
        targetRevision=1,
        targetGeneration=1,
        reason="对比",
        useTeamCredential=True,
        agents=[dict(id=a, published=1, generation=1)],
    )

    def submit(data, key="verify"):
        return req(base + "/replacements", data, headers={"Idempotency-Key": key})

    member = urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())
    )
    req(
        "register",
        dict(
            name="业务复验人",
            email="replacement-review@sample.invalid",
            password=env["BROWSER_PASSWORD"],
            token=req("admin/invitations", {})["token"],
        ),
        session=member,
    )
    deny(
        lambda: req(
            base + "/replacements",
            seed,
            headers={"Idempotency-Key": "unauthorized"},
            session=member,
        ),
        403,
    )
    deny(lambda: submit(dict(seed, target=mismatch)), 409)
    deny(lambda: submit(dict(seed, target=cross)), 404)
    deny(lambda: submit(dict(seed, targetGeneration=2)), 409)
    deny(lambda: submit(dict(seed, useTeamCredential=False)), 422)
    deny(
        lambda: submit(
            dict(
                seed,
                agents=seed["agents"] + [dict(id=custom, published=1, generation=1)],
            )
        ),
        409,
    )
    assert not req(base)["replacements"]

    def browser(adopt):
        subprocess.run(
            ["node", str(frontend / "tests/model-replacement.mjs")],
            env={
                **env,
                "VISUAL_EVIDENCE": str(directory),
                "SWAP_SOURCE": source,
                "SWAP_ADOPT": adopt,
                "SWAP_TARGET": target,
            },
            check=True,
            timeout=45,
        )

    entered, release = gates
    path = launch("REPLACEMENT_HELD_891", two_steps=True)
    try:
        assert entered.wait(25), req(path)["run"]
        impact = req(base)
        assert impact["pinnedProjects"] == 1 and impact["pinnedServices"] == 1, impact
        browser("")
        receipt = json.loads((directory / "replacement-request.json").read_text())
        data = receipt["payload"]
        assert (
            submit(dict(data, agents=data["agents"][::-1]), receipt["key"])["id"]
            == receipt["id"]
        )
        deny(lambda: submit(dict(data, reason="不同选择"), receipt["key"]), 409)
        deny(lambda: req(f"agents/{a}/publish", dict(revision=2)), 409)

        def status():
            return {
                x["name"]: x["status"] for x in req(base)["replacements"][0]["agents"]
            }

        assert set(status().values()) == {"pending"}
        req(
            f"agents/{a}/checks/2",
            dict(
                version=0,
                items=[
                    dict(
                        name="目标渠道对照",
                        sample="采购合同需双方审批",
                        expected="双人审核",
                        match="contains",
                        reviewer="",
                    )
                ],
            ),
            "PUT",
        )
        trial(b, 2)
        assert set(status().values()) == {"ready", "pending"}
        req(
            "agents",
            dict(
                original,
                id=b,
                expected=2,
                name="企业最新定制",
                instructions="保留我的修改",
            ),
        )
        deny(lambda: req(f"agents/{b}/publish", dict(revision=2)), 409)
        browser("yes")
        assert status() == {"业务摘要助手": "published", "发布材料助手": "superseded"}
        assert len(req(path)["run"]["attempts"]) == 1
    finally:
        release.set()
    old = completed(path, "old · private")
    originals = req(path)["artifacts"]
    assert len(originals) == 2 and all(item["body"] == old for item in originals)
    new = completed(launch("新项目复验"), "new · team")
    token = req(f"services/{other}/applications", dict(name="替换验收应用", days=1))[
        "token"
    ]
    run = req(
        f"v1/services/{other}/runs",
        dict(version=1, input="固定服务调用", authorizeCalls=1),
        headers={"Authorization": "Bearer " + token, "Idempotency-Key": "frozen"},
    )
    service = completed("projects/" + run["project"], "old · private")
    adopted = status()
    deny(lambda: req(f"agents/{a}/publish", dict(revision=1)), 409)
    route = f"admin/model-replacements/{receipt['id']}/agents/{a}/rollback"
    reason = dict(reason="完成对照后恢复原模型")
    deny(lambda: req(route, reason, session=member), 403)
    req(
        "admin/catalog/models/" + source,
        dict(action="disable", reason="核对原资源可用性"),
    )
    deny(lambda: req(route, reason), 409)
    req("admin/catalog/models/" + source, dict(action="enable", reason="已修复"))
    browser("rollback")
    assert status()["业务摘要助手"] == "restored"
    req(route, reason)
    deny(lambda: req(route, dict(reason="另一条指令")), 409)
    restored = completed(launch("恢复后新方案"), "old · private")
    assert completed("projects/" + run["project"], "old · private") == service
    ledger = json.loads(
        subprocess.check_output(
            pg
            + [
                "psql",
                "-XAtq",
                "-U",
                "alliance",
                "-d",
                db,
                "-c",
                f"""select json_agg(row_to_json(t) order by kind,model) from (
             select kind,source_name as model,count(*) as calls,sum(reserved_cny)::text as reserved,
                    sum(amount_cny)::text as posted,count(*) filter(where not settled) as unsettled
             from spend where team_id='{team}' group by kind,source_name) t""",
            ],
            text=True,
        )
    )
    assert [(r["kind"], r["model"], r["calls"], r["reserved"]) for r in ledger] == [
        ("execution", "replacement-new", 1, "0.003000"),
        ("execution", "replacement-old", 4, "0.008000"),
        ("validation", "replacement-new", 2, "0.006000"),
        ("validation", "replacement-old", 3, "0.006000"),
    ], ledger
    assert all(
        row["posted"] == "0.000000" and row["unsettled"] == row["calls"]
        for row in ledger
    )
    deny(lambda: submit(seed, "stale-after-rollback"), 409)
    available = next(
        row for row in req(base)["agents"] if row["id"] == a and row["published"]
    )
    assert available["eligible"] and available["generation"] == 3
    next_request = dict(
        seed, agents=[dict(id=a, published=1, generation=available["generation"])]
    )
    with ThreadPoolExecutor(max_workers=2) as pool:
        one, two = [
            pool.submit(submit, next_request, "repeat-after-rollback") for _ in range(2)
        ]
        repeat = one.result()
        assert repeat == two.result()
    created = next(
        row for row in req(base)["replacements"] if row["id"] == repeat["id"]
    )
    assert (
        created["agents"][0]["revision"] == 3
        and created["agents"][0]["status"] == "pending"
    )
    repeated_check = req(f"agents/{a}/checks/3")["items"][0]
    assert repeated_check["passed"] is None and repeated_check["name"] == "固定业务样例"
    result = dict(
        provider="two isolated HTTPS paths with distinct credentials on a controlled server",
        sameSample="采购合同需双方审批",
        batch=receipt["id"],
        adopted=adopted,
        outcomes=next(
            {x["name"]: x["status"] for x in batch["agents"]}
            for batch in req(base)["replacements"]
            if batch["id"] == receipt["id"]
        ),
        nextDraft=3,
        afterRollback=restored,
        originalTask=old,
        dependentStepAfterPublication=originals[1]["body"],
        newTask=new,
        fixedService=service,
        costLedger=ledger,
    )
    (directory / "model-replacement.json").write_text(
        json.dumps(result, ensure_ascii=False, indent=2)
    )

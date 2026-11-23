"""Cross-team consumption of a real local calculation and a correlated A2A fixture."""

import hashlib
import io
import json
import subprocess
import time
import zipfile
from decimal import Decimal
from xml.etree import ElementTree as ET


def verify_borrowed_connectors(
    req, env, frontend, port, directory, team, borrower, payer, remote
):
    def get(path, *args, **kwargs):
        return req(path, *args, session=borrower, **kwargs)

    borrower_id = get("me")["id"]
    req(
        "admin/members/" + borrower_id,
        dict(
            role="member",
            enabled=True,
            password=env["BROWSER_PASSWORD"],
            reason="以普通业务成员发起协作",
        ),
        "PATCH",
    )
    req(
        "login",
        dict(email="grant-requester@sample.invalid", password=env["BROWSER_PASSWORD"]),
        session=borrower,
    )
    assert get("me")["role"] == "member"
    temporary = req("admin/teams", dict(name="临时准备组"))["id"]
    req(
        "admin/teams/" + temporary + "/members/" + borrower_id,
        dict(joined=True, reason="起草后撤回临时范围"),
        "PUT",
    )
    recipient = req("admin/teams", dict(name="地区运营组"))["id"]
    req(
        "admin/teams/" + recipient + "/members/" + get("me")["id"],
        dict(joined=True, reason="发起地区核算"),
        "PUT",
    )

    default_team = next(t["id"] for t in req("teams") if t["default"])
    req(
        "admin/teams/" + default_team + "/members/" + borrower_id,
        dict(joined=False, reason="仅属于业务团队"),
        "PUT",
    )

    def browser(project="", budget=False):
        subprocess.run(
            ["node", str(frontend / "tests/borrowed-connectors.mjs")],
            env={
                **env,
                "PEER_TEAM": team,
                "PEER_BUDGET": "yes" if budget else "",
                "PEER_TEMPORARY": temporary,
                "PEER_RECIPIENT": recipient,
                "PEER_ENDPOINT": f"https://localhost:{port}/remote",
                "PEER_PROJECT": project,
                "VISUAL_EVIDENCE": str(directory),
            },
            check=True,
            timeout=90,
        )

    def denied(fn, code):
        try:
            fn()
        except RuntimeError as e:
            assert e.args[1] == code, e
        else:
            raise AssertionError(f"Expected HTTP {code}")

    browser()
    ids = json.loads((directory / "peer-registration.json").read_text())
    tool, external = ids["tool"], ids["external"]
    team_name = next(t["name"] for t in req("teams") if t["id"] == team)
    library = {a["id"]: a for a in req("agents") if a["id"] in (tool, external)}
    assert all(
        library[a]["team"] == team and library[a]["published"] == 0
        for a in (tool, external)
    )
    other = next(t["id"] for t in req("teams") if t["default"])
    card_config = dict(card=library[external]["card"], keepCredential=True)
    tool_config = dict(
        name="另一份草稿", description="跨团队能力", tool=library[tool]["tool"]
    )
    for route, config, agent in [
        ("agents/tools", tool_config, tool),
        ("agents/cards", card_config, external),
    ]:
        denied(lambda: req(route, dict(config, id=agent, expected=1, team=other)), 409)
        denied(lambda: get(route, dict(config, team=team)), 403)
    csv = "region,amount,status\n上海,57.30,ok\n上海,33.00,ok\n上海,11.00,no"
    for agent, sample, expected in [
        (tool, csv, "90.30"),
        (external, "AUTH_CARD_TEST sample", "RECEIPT"),
    ]:
        endpoint = "agents/" + agent
        evidence = req(
            endpoint + "/validate",
            dict(revision=1, input=sample, expected=expected, authorizeOneCall=True),
        )
        assert evidence["status"] == "passed", evidence
        req(endpoint + "/publish", dict(revision=1))
    project = ids["project"]
    path = "projects/" + project
    assert get(path)["team"] == recipient
    get(
        path + "/participants",
        dict(
            email="review@sample.invalid", expected=get(path)["revision"], review=False
        ),
    )
    src = get(path + "/sources?name=sales.csv", csv.encode())

    def grant(agent, budget_ui=False):
        grant = req(
            path + "/agent-grants",
            dict(
                agent=agent,
                revision=1,
                description="仅用于本项目的地区核算",
                days=2,
                calls=1,
            ),
        )["id"]
        endpoint = "project-agent-grants/" + grant
        req(
            endpoint, dict(action="resource", expected=1, reason="授权项目使用绑定资源")
        )
        assert agent not in [c["id"] for c in get(path + "/candidates")]
        if budget_ui:
            browser(budget=True)
        else:
            req(
                endpoint,
                dict(
                    action="budget",
                    expected=2,
                    reason="仅此调用，远端费用以实际账单为准",
                ),
                session=payer,
            )
        return endpoint

    grant(tool)
    original = grant(external, budget_ui=True)
    assert {a["id"] for a in get(path)["sharedAgents"]} == {tool, external}
    get(
        path + "/plan",
        dict(
            expected=get(path)["revision"],
            brief="AUTH_CARD_TEST REMOTE_BORROW 核对总额",
            agents=[tool, external],
            dependencies=[[], [1]],
            sources=[dict(id=src["id"], version=1)],
        ),
    )
    plan = get(path)
    assert (
        plan["plan"]["quote"]["unknown"] == 1 and plan["plan"]["quote"]["priced"] == 1
    )
    run = get(
        path + "/runs",
        dict(expected=plan["revision"], authorizeCalls=2),
        headers={"Idempotency-Key": "borrowed-case"},
    )["id"]

    def until(fn):
        for _ in range(160):
            value = fn()
            if value:
                return value
            time.sleep(0.2)
        raise AssertionError(get(path)["run"])

    until(lambda: "tasks/get" in remote.methods("BORROW"))
    req(original, dict(action="revoke", expected=3, reason="临时收回远端核对权限"))

    def detail():
        attempts = get(path)["run"]["attempts"]
        return attempts[-1]["remote"]["detail"]

    until(lambda: detail() == "remote_denied")
    methods = remote.methods("BORROW")
    remote.finish("BORROW")

    def recheck():
        get(
            f"runs/{run}/remote/2/check",
            dict(expected=get(path)["run"]["controlRevision"], reason="核对原任务"),
        )

    denied(recheck, 409)
    assert detail() == "remote_denied"
    assert remote.methods("BORROW") == methods
    grant(external)
    recheck()
    until(lambda: get(path)["stage"] == "review")
    view = get(path)
    assert view["costSummary"]["calls"] == 2 and view["costSummary"]["unknown"] == 1
    assert len(view["costDetails"]) == 2 and all(
        c["payer"] == team_name for c in view["costDetails"]
    )
    assert sorted(g["used"] for g in get(path + "/agent-grants")) == [0, 1, 1]
    artifacts = view["artifacts"]
    assert (
        len(artifacts) == 2
        and "90.30" in artifacts[0]["body"]
        and artifacts[1]["body"] == "RECEIPT asynchronous BORROW"
    )
    with remote.lock:
        traffic = [(r[1], r[2]) for r in remote.requests if r[0] == "BORROW"]
    assert (
        len(set(task for _, task in traffic)) == 1
        and [m for m, _ in traffic].count("message/send") == 1
    )
    browser(project)
    delivery = get(path + "/delivery")
    with borrower.open(
        env["SMOKE_ORIGIN"] + "/api/" + path + "/delivery/package"
    ) as response:
        archive = zipfile.ZipFile(io.BytesIO(response.read()))
    spreadsheet = next(n for n in archive.namelist() if n.endswith(".xlsx"))
    with zipfile.ZipFile(io.BytesIO(archive.read(spreadsheet))) as workbook:
        sheet = workbook.read("xl/worksheets/sheet1.xml").decode()
    amounts = [Decimal(v.text) for v in ET.fromstring(sheet).findall(".//{*}v")]
    assert amounts == [Decimal("90.30"), Decimal("90.30")], amounts
    assert delivery["items"][1]["dependencies"][0]["step"] == 1
    receipt = dict(
        data="synthetic",
        connector="DuckDB and openpyxl",
        external="local authenticated A2A JSON-RPC fixture",
        payer=team_name,
        requesterRole="member",
        sourceTeam=team_name,
        projectTeam="地区运营组",
        draftPreservedAfterWithdrawal=True,
        chargedCalls=2,
        unknownPrices=1,
        remoteSubmissions=1,
        remoteReads=len(traffic) - 1,
        readsDuringWithdrawal=0,
        receiptIds=1,
        reviewedVersions=[i["version"] for i in delivery["items"]],
        total="90.30",
        xlsxSha256=hashlib.sha256(archive.read(spreadsheet)).hexdigest(),
    )
    (directory / "borrowed-connectors.json").write_text(
        json.dumps(receipt, ensure_ascii=False, indent=2)
    )

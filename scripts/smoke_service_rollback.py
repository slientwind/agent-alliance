"""Verify a service rollback against an already-dispatched call and old data."""

import hashlib
import io
import json
from pathlib import Path
import re
import subprocess
from uuid import UUID, uuid4
from zipfile import ZipFile


def verify_service_rollback(req, fixture, frontend, env, directory, provider, pg, db):
    subprocess.run(
        ["node", str(frontend / "tests/service-rollback.mjs")],
        env={**env, "QUICK_ENDPOINT": provider, "VISUAL_EVIDENCE": str(directory)},
        check=True,
        timeout=175,
    )
    report = directory / "service-rollback.json"
    proof = json.loads(report.read_text())
    with ZipFile(directory / "service-rollback-held-delivery.zip") as package:
        manifest = json.loads(package.read("manifest.json"))
        items = {item["step"]: item for item in manifest["items"]}
        assert set(items) == {1, 2} and manifest["confirmedBy"]
        for item in items.values():
            assert item["reviewer"] and item["reviewedAt"]
            assert hashlib.sha256(package.read(item["path"])).hexdigest() == item["sha256"]
        assert b"SERVICE_ROLLBACK_HOLD_937" in package.read(items[1]["path"])
        assert items[2]["path"].endswith(".docx")
        with ZipFile(io.BytesIO(package.read(items[2]["path"]))) as word:
            assert b"SERVICE_ROLLBACK_HOLD_937" in word.read("word/document.xml")
    proof["verifiedOriginalV2WordAndHashes"] = True

    if fixture:
        service = proof["service"]
        path = "services/" + service
        bind = "channels/feishu/bind"

        def ask(text):
            return fixture.reply(fixture.send(req, text, chat="oc_service_rollback"))

        if not req(bind)["bound"]:
            assert "身份已绑定" in ask(req(bind, {}, method="POST")["command"])
        pending = ask("发起 " + service + " 核对已填退单")
        assert "v3" in pending
        old = re.search(r"确认 ([0-9a-f-]{36})$", pending)[1]
        route = req(path + "/versions")
        req(path + "/rollback", dict(
            expected=3, expectedRoutingRevision=route["routingRevision"],
            target=1, reason="飞书预览核对",
        ), headers={"Idempotency-Key": "feishu-rollback-check"})
        stale = ask("确认 " + old)
        assert "已换版" in stale, stale
        new = ask("发起 " + service + " 核对同一退单")
        assert "v1" in new and "最多授权 1 次调用" in new
        current = re.search(r"确认 ([0-9a-f-]{36})$", new)[1]
        launched = ask("确认 " + current)
        assert "已受理" in launched, launched
        assert req(path + "/entry")["recent"][0]["version"] == 1
        proof["feishu"] = dict(staleProposalRejected=True, newProposalVersion=1, acceptedVersion=1)
    subject = UUID(proof["service"])
    audit = json.loads(subprocess.check_output(pg + [
        "psql", "-At", "-U", "alliance", "-d", db, "-c",
        f"select json_agg(detail order by seq) from audit_events where action='service.rollback' and subject='{subject}'",
    ], text=True))
    assert [item["to"] for item in audit] == ([1, 2, 1, 1] if fixture else [1, 2, 1]), audit
    assert audit[0]["routingRevision"] == proof["receipt"]["routingRevision"]
    proof["rollbackAuditEvents"] = audit
    report.write_text(json.dumps(proof, ensure_ascii=False, indent=2) + "\n")


def verify_service_rollback_upgrade(pg, directory):
    schema = Path(__file__).parent / "schema"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
    pg = [*pg[:-1], "-i", pg[-1]]
    db = "routing_upgrade_" + uuid4().hex
    owner, team, service, project, run = (str(uuid4()) for _ in range(5))
    sql = "\n".join(file.read_text() for file in sorted(schema.glob("*.sql")) if file.name[:3] <= "109")
    sql += f"""
    insert into members(id,email,display_name,password_hash,role) values('{owner}','upgrade@sample.invalid','换版迁移','!','admin');
    insert into teams(id,name) values('{team}','换版历史团队');
    insert into team_members values('{team}','{owner}');
    insert into services(id,team_id,name,owner,current_version) values('{service}','{team}','迁移时默认名','{owner}',2);
    insert into service_versions(service_id,version,instructions,reviewer,example_input)
      values('{service}',1,'历史执行约定','{owner}','旧样例'),('{service}',2,'默认执行约定','{owner}','新样例');
    insert into projects(id,owner,final_reviewer,team_id,title,stage,plan_version)
      values('{project}','{owner}','{owner}','{team}','先前已受理的服务','paused',1);
    insert into plans(project_id,version,brief) values('{project}',1,'固定为历史版');
    insert into runs(id,project_id,plan_version,actor,charge_team,status,request_key,request_hash,max_calls,service_id,service_version)
      values('{run}','{project}',1,'{owner}','{team}','paused','legacy-service-run',decode('a0','hex'),1,'{service}',1);
    """
    sql += (schema / "110_service_rollbacks.sql").read_text()
    sql += f"""
    select jsonb_build_object(
      'defaultPreserved',(select current_version=2 and routing_revision=1 and name='迁移时默认名' from services where id='{service}'),
      'onlyKnownNameRecorded',(select count(*)=2 and count(name)=1 and max(name) filter(where version=2)='迁移时默认名' from service_versions where service_id='{service}'),
      'historicalContractPreserved',(select name is null and instructions='历史执行约定' and example_input='旧样例' from service_versions where service_id='{service}' and version=1),
      'acceptedRunPreserved',(select service_version=1 and status='paused' and request_key='legacy-service-run' from runs where id='{run}'),
      'noAutomaticRollback',not exists(select from service_rollbacks));
    """
    subprocess.run(pg + ["createdb", "-U", "alliance", db], check=True)
    try:
        response = subprocess.run(
            pg + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db],
            input=sql, text=True, capture_output=True, check=True,
        )
        proof = json.loads(response.stdout.strip().splitlines()[-1])
        assert all(proof.values()), proof
        (directory / "service-rollback-upgrade.json").write_text(json.dumps(proof, indent=2) + "\n")
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", db], check=True)

"""Verify project reuse through the browser and read the approved originals."""

import json
import subprocess
from hashlib import sha256
from pathlib import Path
from uuid import uuid4
from zipfile import ZipFile


def verify_workflow_template(frontend, env, directory, python, endpoint):
    subprocess.run(
        ["node", str(frontend / "tests/workflow-template.mjs")],
        env={**env, "QUICK_ENDPOINT": endpoint, "VISUAL_EVIDENCE": str(directory)},
        timeout=155,
        check=True,
    )
    report_path = directory / "workflow-template.json"
    report = json.loads(report_path.read_text())
    report["verifiedTotals"] = {}
    for label, total, items in (
        ("original", "12.40", 3), ("reused", "35.20", 3),
        ("v2-reference", "22.80", 2), ("adopted", "41.15", 2),
    ):
        with ZipFile(directory / f"workflow-template-{label}.zip") as package:
            manifest = json.loads(package.read("manifest.json"))
            assert len(manifest["items"]) == items
            assert [source["pinned"] for source in manifest["sources"]] == [1]
            for artifact in manifest["items"]:
                assert artifact["version"] == 1 and artifact["reviewer"] and artifact["reviewedAt"]
                assert sha256(package.read(artifact["path"])).hexdigest() == artifact["sha256"]
            workbook, = [entry["path"] for entry in manifest["items"] if entry["path"].endswith(".xlsx")]
            document, = [entry["path"] for entry in manifest["items"] if entry["path"].endswith(".docx")]
            assert len(package.read(document)) > 1000
            subprocess.run(
                [str(python), "-c", """
from decimal import Decimal
from io import BytesIO
import sys
from openpyxl import load_workbook
sheet = load_workbook(BytesIO(sys.stdin.buffer.read()), read_only=True, data_only=True)["汇总"]
label, amount = list(sheet.values)[-1]
assert label == "合计" and Decimal(str(amount)) == Decimal(sys.argv[1]), (label, amount)
""", total],
                input=package.read(workbook),
                check=True,
            )
        report["verifiedTotals"][label] = total
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    update_path = directory / "workflow-template-update.json"
    update = json.loads(update_path.read_text())
    update["verifiedTotal"] = report["verifiedTotals"]["adopted"]
    update_path.write_text(json.dumps(update, ensure_ascii=False, indent=2) + "\n")


def verify_workflow_template_upgrade(pg, directory):
    schema = Path(__file__).parent / "schema"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
    pg = [*pg[:-1], "-i", pg[-1]]
    db = "workflow_upgrade_" + uuid4().hex
    owner, team, template, origin, draft, running, run = (str(uuid4()) for _ in range(7))
    sql = "\n".join(file.read_text() for file in sorted(schema.glob("*.sql")) if file.name[:3] <= "108")
    sql += f"""
    insert into members(id,display_name,email,password_hash,role) values('{owner}','迁移核验','workflow-upgrade@sample.invalid','','admin');
    insert into teams(id,name) values('{team}','迁移测试团队');
    insert into team_members values('{team}','{owner}');
    insert into projects(id,owner,final_reviewer,team_id,title,stage,plan_version) values
      ('{origin}','{owner}','{owner}','{team}','模板来源','delivered',1),
      ('{draft}','{owner}','{owner}','{team}','已保存方案','plan',1),
      ('{running}','{owner}','{owner}','{team}','已受理任务','paused',1);
    insert into plans(project_id,version,brief) values('{origin}',1,'来源方案'),('{draft}',1,'原待执行方案'),('{running}',1,'正在办理的方案');
    insert into runs(id,project_id,plan_version,actor,charge_team,status,request_key,request_hash,max_calls)
      values('{run}','{running}',1,'{owner}','{team}','paused','legacy-flow-run',decode('a0','hex'),1);
    insert into workflow_templates(id,team_id,owner,current_version,published_version) values('{template}','{team}','{owner}',2,2);
    insert into workflow_template_versions(template_id,version,name,instructions,inputs,outcomes,requires_sources,nodes,origin_project,origin_plan,author,request_key,request_hash)
      select '{template}',v,'旧模板','业务约定','原件','工作簿',false,'[{{"operation":"aggregate"}}]'::jsonb,'{origin}',1,'{owner}',v::text,decode('a1','hex')
       from generate_series(1,2) v;
    insert into workflow_template_uses values
      ('{draft}','{template}',1,'{owner}','old-draft',decode('b1','hex')),
      ('{running}','{template}',1,'{owner}','old-run',decode('b2','hex'));
    """
    sql += (schema / "109_workflow_template_adoptions.sql").read_text()
    sql += f"""
    select jsonb_build_object(
      'originalBindingsPreserved',(select count(*) from project_workflow_template_binding where template_id='{template}' and version=1)=2,
      'existingPlanPreserved',(select brief='原待执行方案' from plans where project_id='{draft}' and version=1),
      'acceptedRunPreserved',(select plan_version=1 and status='paused' from runs where id='{run}'),
      'newVersionOffered',offered_workflow_template_version('{template}','{owner}')=2,
      'noAutomaticAdoption',not exists(select from workflow_template_adoptions));
    """
    subprocess.run(pg + ["createdb", "-U", "alliance", db], check=True)
    try:
        response = subprocess.run(
            pg + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db],
            input=sql, text=True, capture_output=True, check=True,
        )
        proof = json.loads(response.stdout.strip().splitlines()[-1])
        assert all(proof.values()), proof
        (directory / "workflow-template-update-upgrade.json").write_text(json.dumps(proof, indent=2) + "\n")
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", db], check=True)

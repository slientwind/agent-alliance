"""Verify that the management upgrade does not invent old department attribution."""

import json
from pathlib import Path
import subprocess
from uuid import uuid4


def verify_scope_upgrade(pg):
    target = "scope_upgrade_" + uuid4().hex
    root = Path(__file__).parent
    schema = root / "schema"
    monitor = root / "monitor"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
        monitor = Path("backend/internal/monitor")
    old = "\n".join(
        f.read_text() for f in sorted(schema.glob("*.sql")) if f.name[:3] <= "064"
    )
    fixture = """
      INSERT INTO teams(name) VALUES('Legacy team');
      INSERT INTO departments(name) VALUES('Present department');
      INSERT INTO members(display_name,email,password_hash,role,department_id)
      SELECT 'Legacy member','old@example.invalid','!','admin',id FROM departments;
      INSERT INTO enterprise(id,name,default_team) SELECT true,'Synthetic',id FROM teams;
      CREATE TEMP VIEW fixture AS SELECT m.id actor,t.id team,m.department_id dept FROM members m CROSS JOIN teams t;
      INSERT INTO model_resources(name,team_id,created_by) SELECT 'Fixture model',team,actor FROM fixture;
    """
    project = """
      INSERT INTO projects(owner,team_id,final_reviewer,title) SELECT actor,team,actor,'Synthetic' FROM fixture;
      INSERT INTO plans(project_id,version,brief) SELECT id,1,'Synthetic' FROM projects ON CONFLICT DO NOTHING;
      INSERT INTO runs(project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
      SELECT p.id,1,p.owner,'completed',p.team_id,p.id::text,'x'::bytea,1 FROM projects p
      WHERE NOT EXISTS(SELECT FROM runs r WHERE r.project_id=p.id);
    """

    def spend(key):
        return f"""
        INSERT INTO spend(op_key,team_id,actor,kind,unknown_authorized,resource_budget,source_revision,source_name)
        SELECT '{key}',team,actor,'validation',true,b.id,1,'Fixture model' FROM fixture CROSS JOIN resource_budgets b;
        """

    grants = """
      INSERT INTO management_grants(member_id,team_id,department_id,include_costs,granted_by,expires_at,reason)
      SELECT actor,team,NULL,true,actor,now()+interval '1 day','Scope' FROM fixture
      UNION ALL SELECT actor,NULL,dept,true,actor,now()+interval '1 day','Scope' FROM fixture;
      UPDATE members SET department_id=NULL;
    """
    # Call the same catalog query used by the administrator page.
    sql = (
        old
        + fixture
        + project
        + spend("old")
        + (schema / "065_management_scopes.sql").read_text()
        + project
        + spend("new")
        + grants
        + (schema / "066_management_exports.sql").read_text()
        + (schema / "067_export_policy_revision.sql").read_text()
        + (schema / "068_management_details.sql").read_text()
        + (schema / "069_enterprise_management.sql").read_text()
        + (schema / "073_financial_exports.sql").read_text()
    )
    sql += """
      SELECT json_build_object('oldAndNewRuns',count(*),'runsWithHistoricalDepartment',count(department_id),
        'callsWithHistoricalDepartment',(SELECT count(originating_department) FROM spend),
        'oldCallDepartmentUnknown',(SELECT originating_department IS NULL FROM spend WHERE op_key='old'))
      FROM management_outcome_events WHERE kind='run';
    """
    sql += (monitor / "grant_catalog.sql").read_text().replace(
        "$1", "(SELECT id FROM members)"
    ) + ";"
    def summary(condition):
        query = (monitor / "scope_report.sql").read_text()
        for marker, value in {
            "$1": f"(SELECT id FROM management_grants WHERE {condition})",
            "$2": "(SELECT id FROM members)",
            "$3": "(now()-interval '1 day')",
            "$4": "(now()+interval '6 days')",
        }.items():
            query = query.replace(marker, value)
        return query + ";"

    sql += """
      INSERT INTO teams(name) VALUES ('Second team');
      INSERT INTO projects(owner,team_id,final_reviewer,title)
      SELECT m.id,t.id,m.id,'New work' FROM members m JOIN teams t ON t.name='Second team';
      INSERT INTO plans(project_id,version,brief) SELECT id,1,'New work' FROM projects WHERE title='New work';
      INSERT INTO runs(project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
      SELECT id,1,owner,'completed',team_id,'second-team','x'::bytea,1 FROM projects WHERE title='New work';
      INSERT INTO management_grants(member_id,enterprise_id,include_costs,granted_by,expires_at,reason)
      SELECT id,true,false,id,now()+interval '1 day','Explicit scope' FROM members;
      DO $$ BEGIN
        BEGIN
          UPDATE management_grants SET enterprise_id=true WHERE team_id IS NOT NULL;
          RAISE EXCEPTION 'mixed scope accepted';
        EXCEPTION WHEN check_violation THEN NULL; END;
        BEGIN
          UPDATE management_grants SET enterprise_id=NULL WHERE enterprise_id IS TRUE;
          RAISE EXCEPTION 'empty scope accepted';
        EXCEPTION WHEN check_violation THEN NULL; END;
      END $$;
    """
    sql += summary("team_id IS NOT NULL") + summary("enterprise_id IS TRUE")
    subprocess.run(pg + ["createdb", "-U", "alliance", target], check=True)
    try:
        result = subprocess.run(
            [
                *pg[:-1],
                "-i",
                pg[-1],
                "psql",
                "-qtAX",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                target,
            ],
            input=sql,
            text=True,
            capture_output=True,
        )
        if result.returncode:
            raise RuntimeError(result.stderr[-1000:])
        counts, catalog, report, enterprise = map(json.loads, result.stdout.splitlines()[-4:])
        assert enterprise["runs"] == 3 and enterprise["costs"] is None
        assert (
            report["runs"] == 2
            and report["checkedAt"] < report["expiresAt"] < report["to"]
        )
        assert counts == dict(
            oldAndNewRuns=2,
            runsWithHistoricalDepartment=1,
            callsWithHistoricalDepartment=1,
            oldCallDepartmentUnknown=True,
        ), counts
        assert len(catalog["grants"]) == len(catalog["ended"]) == 1, catalog
        assert catalog["grants"][0]["kind"] == "team"
        assert all(g["kind"] != "enterprise" for g in catalog["grants"]+catalog["ended"])
        assert [o["id"] for o in catalog["scopes"] if o["kind"] == "enterprise"] == ["enterprise"]
        assert catalog["grants"][0]["export"] is False
        assert not catalog["grants"][0]["details"] and not catalog["grants"][0]["detailCosts"]
        ended = catalog["ended"][0]
        assert ended["kind"] == "department" and ended["endReason"] == "已调离授权部门"
        assert ended["issuer"] == "Legacy member" and ended["endedAt"]
        return {
            **counts,
            "expiryCheckedAgainstDatabaseClock": True,
            "schemaFrom": 64,
            "schemaTo": 69,
            "legacyEnterpriseScopeGranted": False,
            "teamRunsAfterAnotherTeamStarted": report["runs"],
            "explicitEnterpriseRuns": enterprise["runs"],
            "mixedAndEmptyScopesRejected": True,
            "legacyDetailsAllowed": False,
            "legacyExportAllowed": False,
            "endedDepartmentReason": ended["endReason"],
            "remainingTeamGrants": 1,
        }
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", target], check=True)


if __name__ == "__main__":
    print(
        json.dumps(
            verify_scope_upgrade(["docker", "exec", "alliance-workspace-database-1"]),
            ensure_ascii=False,
            indent=2,
        )
    )

"""Upgrade a disposable v35 database populated with older pinned calls and one unlinked charge."""

from pathlib import Path
import subprocess
import uuid


def verify_resource_upgrade(pg):
    pg = [*pg[:-1], "-i", pg[-1]]
    target = "budget_upgrade_" + uuid.uuid4().hex
    path = Path(__file__).parent / "schema"
    if not path.is_dir():
        path = Path("backend/internal/database/schema")
    older = "\n".join(
        f.read_text() for f in sorted(path.glob("*.sql")) if f.name[:3] <= "035"
    )
    fixture = """
    DO $$
    DECLARE t uuid; u uuid; model uuid; agent uuid; task uuid; run uuid; trial uuid; planner uuid;
    BEGIN
      INSERT INTO teams(name) VALUES('Fixture') RETURNING id INTO t;
      INSERT INTO members(display_name,email,password_hash,role) VALUES('Sample','sample@example.invalid','','member') RETURNING id INTO u;
      INSERT INTO model_resources(name,team_id,created_by) VALUES('Old Model',t,u) RETURNING id INTO model;
      INSERT INTO model_revisions(resource_id,revision,endpoint,model,encrypted_key) VALUES(model,1,'local','sample','');
      INSERT INTO agents(owner,team_id) VALUES(u,t) RETURNING id INTO agent;
      INSERT INTO agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by) VALUES(agent,1,'Sample','Sample',model,1,u);
      INSERT INTO projects(owner,team_id,final_reviewer,title) VALUES(u,t,u,'Sample') RETURNING id INTO task;
      INSERT INTO plans(project_id,version,brief) VALUES(task,1,'Sample');
      INSERT INTO nodes(project_id,plan_version,agent_id,agent_revision,ordinal) VALUES(task,1,agent,1,1);
      INSERT INTO runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls) VALUES(task,1,u,t,'sample','',1) RETURNING id INTO run;
      INSERT INTO validations(agent_id,revision,actor,status) VALUES(agent,1,u,'passed') RETURNING id INTO trial;
      INSERT INTO planning_requests(project_id,actor,request_key,project_revision,resource_id,resource_revision) VALUES(task,u,'sample',1,model,1) RETURNING id INTO planner;
      INSERT INTO spend(op_key,team_id,actor,project_id,run_id,kind,reserved_cny,amount_cny,unknown_authorized,settled)
      VALUES('unknown-old-key',t,u,NULL,NULL,'legacy',NULL,0,true,false),
      ('planning/'||planner,t,u,task,NULL,'planning',1,2,false,true),
      ('validation/'||trial,t,u,NULL,NULL,'validation',1,0,false,false),
      (run::text||'-1',t,u,task,run,'execution',1,0,false,false);
    END $$;
    """
    subprocess.run(pg + ["createdb", "-U", "alliance", target], check=True)
    try:
        sql = (
            older
            + fixture
            + (path / "036_resource_budgets.sql").read_text()
            + """
        UPDATE spend SET settled=true,amount_cny=0.5 WHERE op_key='unknown-old-key';
        SELECT concat_ws('|',calls,used,recorded,reserved,(SELECT count(*) FROM spend WHERE resource_budget IS NULL),(SELECT sum(amount_cny) FROM spend)) FROM resource_budget_status;
        """
        )
        result = subprocess.run(
            pg
            + [
                "psql",
                "-U",
                "alliance",
                "-d",
                target,
                "-XAtq",
                "-v",
                "ON_ERROR_STOP=1",
            ],
            input=sql,
            capture_output=True,
            text=True,
        )
        if result.returncode:
            raise RuntimeError(result.stderr[-1200:])
        observed = result.stdout.strip().splitlines()[-1]
        assert observed == "3|4.000000|2.000000|2.000000|1|2.500000", observed
        return dict(
            priorSchema=35,
            matchedOperations=3,
            unlinkedHistoricalCharges=1,
            legacySettlement="0.500000",
            resourceUsedCNY="4.000000",
        )
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", target], check=True)

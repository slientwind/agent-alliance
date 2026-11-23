"""Check the export migration and both sides of the file size boundary in a fresh database."""

import json
import subprocess
from pathlib import Path
from uuid import uuid4


def verify():
    root = Path(__file__).resolve().parents[1] / "backend/internal"
    docker = ["docker", "exec", "-i", "alliance-workspace-database-1"]
    target = "financial_check_" + uuid4().hex
    args = ["-U", "alliance", target]
    actor, team, dept, grant, resource, team_grant = (str(uuid4()) for _ in range(6))
    start = "2021-01-01T00:00:00Z"

    def sql(script):
        return subprocess.check_output(docker + ["psql", "-XqAt", "-v", "ON_ERROR_STOP=1", *args],
                                       input=script, text=True).strip()

    folder = root / "database/schema"
    subprocess.run(docker + ["createdb", *args], check=True)
    try:
        sql("\n".join(p.read_text() for p in sorted(folder.glob("*.sql")) if p.name[:3] < "073"))
        sql(f"""
        insert into teams(id,name) values('{team}','Finance');
        insert into departments(id,name) values('{dept}','Old department');
        insert into members(id,email,password_hash,display_name,role,department_id)
        values('{actor}','finance@example.invalid','!','Finance','admin','{dept}');
        insert into enterprise(id,name,default_team) values(true,'Synthetic','{team}');
        insert into management_grants(id,member_id,enterprise_id,include_costs,allow_export,allow_details,detail_costs,reason,granted_by,expires_at)
        values('{grant}','{actor}',true,true,true,true,true,'Original permission','{actor}',now()+interval '1 hour');
        insert into management_exports(grant_id,request_key,request_hash,include_costs,expires_at,payload)
        values('{grant}',gen_random_uuid(),'x',true,now()+interval '1 hour','{{"version":1}}');
        """)
        sql((folder / "073_financial_exports.sql").read_text())
        assert sql("select g.financial_export,e.cost_lines,e.payload->>'version' from management_grants g join management_exports e on e.grant_id=g.id") == "f|f|1"
        sql(f"""
        update management_grants set financial_export=true where id='{grant}';
        insert into management_grants(id,member_id,team_id,include_costs,financial_export,reason,granted_by,expires_at)
        values('{team_grant}','{actor}','{team}',true,true,'Finance','{actor}',now()+interval '1 hour');
        insert into model_resources(id,name,team_id,created_by) values('{resource}','Probe','{team}','{actor}');
        insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key) values('{resource}',1,'https://example.invalid','fixture','x');
        insert into model_probes(team_id,resource_id,resource_revision,actor,request_key,created_at)
        select '{team}','{resource}',1,'{actor}',n::text,'{start}'::timestamptz+n*interval '1 millisecond' from generate_series(0,10000) n;
        insert into spend(op_key,team_id,actor,kind,amount_cny,reserved_cny,unknown_authorized,resource_budget,source_revision,source_name)
        select 'connection/'||id,'{team}','{actor}','connection',0.000001,0.120002,false,(select id from resource_budgets where model_id='{resource}'),1,'Probe' from model_probes where request_key='0';
        """)
        query = (root / "monitor/financial_export.sql").read_text()

        def sample(scope, seconds):
            return json.loads(sql(f"""
            create function pg_temp.snapshot(uuid,uuid,timestamptz,timestamptz,text,text) returns jsonb language sql as $$ {query} $$;
            with snapshot as materialized (select pg_temp.snapshot('{scope}','{actor}','{start}','{start}'::timestamptz+interval '{seconds} seconds','test','Finance') as f)
            select jsonb_build_object('lineCount',jsonb_array_length(f->'calls'),'totals',f->'totals','first',f#>'{{calls,0}}','last',f#>'{{calls,-1}}','overLimit',f is null) from snapshot;
            """))

        exact = sample(grant, 10)
        assert exact["lineCount"] == 10000 and exact["totals"]["unknown"] == 9999
        assert exact["first"]["recordedCny"] == "0.000001" and exact["first"]["remainingEstimateCny"] == "0.120001"
        assert exact["last"]["recordedCny"] is None and exact["last"]["payerTeamId"] is None
        assert sample(grant, 11)["overLimit"]
        payer = sample(team_grant, 11)
        assert payer["lineCount"] == 1 and payer["totals"]["unknown"] == 0
        return dict(priorGrantIncludesFinancialExport=False, historicalSummaryPreserved=True,
                    exactBoundary=exact["lineCount"], overLimitFileGenerated=False,
                    unallocatedEnterpriseCalls=9999, callsAssignedToPayer=payer["lineCount"], knownTotals=payer["totals"])
    finally:
        subprocess.run(docker + ["dropdb", *args], check=True)


if __name__ == "__main__":
    print(json.dumps(verify(), indent=2))

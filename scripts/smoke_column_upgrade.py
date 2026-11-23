"""Apply the column correction migration over populated 085 call history."""

import json
from pathlib import Path
import subprocess
from uuid import uuid4


def verify_column_upgrade(pg):
    schema = Path(__file__).parent / "schema"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
    pg = [*pg[:-1], "-i", pg[-1]]
    db = "column_upgrade_" + uuid4().hex
    member, team, project, failed, uncertain, control = (str(uuid4()) for _ in range(6))
    check = f"""
    select jsonb_build_object('knownRejectionClosable',run_failure_confirmable('{failed}'),
        'unresolvedEarlierCallClosable',run_failure_confirmable('{uncertain}'),
        'attempts',(select jsonb_agg(status order by attempt_no) from call_attempts where run_id='{uncertain}'),
        'review',(select decision from call_reviews where control_id='{control}'));
    """
    sql = "\n".join(
        f.read_text() for f in sorted(schema.glob("*.sql")) if f.name[:3] <= "085"
    )
    sql += f"""
    insert into members(id,display_name,email,password_hash,role) values('{member}','升级核验','schema@sample.invalid','','admin');
    insert into teams(id,name) values('{team}','升级团队');
    insert into team_members select '{team}','{member}';
    insert into projects(id,owner,final_reviewer,team_id,title) values('{project}','{member}','{member}','{team}','原有核算');
    insert into project_members(project_id,member_id) values('{project}','{member}');
    insert into plans(project_id,version,brief) values('{project}',1,'原方案');
    insert into runs(id,project_id,plan_version,actor,charge_team,status,request_key,request_hash,max_calls)
        select r,'{project}',1,'{member}','{team}','reconcile',r::text,decode('42','hex'),1 from unnest(array['{failed}'::uuid,'{uncertain}'::uuid]) r;
    insert into invocations(run_id,ordinal,attempt_no,status,ended_at)
        values('{failed}',1,1,'input_rejected',now()),('{uncertain}',1,2,'input_rejected',now());
    insert into controls(id,run_id,actor,request_key,signature,action,reason,revision)
        values('{control}','{uncertain}','{member}','old-review',decode('43','hex'),'call.redo','历史回执未确认',2);
    insert into call_reviews(control_id,run_id,ordinal,attempt_no,decision) values('{control}','{uncertain}',1,1,'redo');
    insert into superseded_calls(run_id,ordinal,attempt_no,started_at,ended_at,review_control)
        values('{uncertain}',1,1,now(),now(),'{control}');
    """
    sql += check + (schema / "086_csv_field_corrections.sql").read_text() + check
    sql += f"select input_issue is null from invocations where run_id='{failed}';"
    sql += "\n".join(
        f.read_text() for f in sorted(schema.glob("*.sql")) if "087" <= f.name[:3] <= "103"
    )
    source = str(uuid4())
    issue = json.dumps({"source": source, "version": 1, "name": "旧表.csv", "missing": ["amount"], "choices": ["net"]}, ensure_ascii=False)
    mapping = json.dumps({"source": source, "version": 1, "columns": {"amount": "net"}})
    sql += f"""
    update invocations set input_issue='{issue}'::jsonb where run_id='{failed}';
    update call_reviews set input_mapping='{mapping}'::jsonb where control_id='{control}';
    """
    sql += (schema / "104_csv_batch_corrections.sql").read_text()
    sql += f"""
    select jsonb_build_object(
      'legacyReceiptPreserved',i.input_issue='{issue}'::jsonb and (select count(*) from column_correction_items(i.input_issue))=1,
      'legacyMappingPreserved',v.input_mapping='{mapping}'::jsonb and
         (select item->'columns'->>'amount' from column_correction_items(v.input_mapping) item)='net')
    from invocations i, call_reviews v where i.run_id='{failed}' and v.control_id='{control}';
    """
    sql += "create temporary table previous_column_costs as select * from management_call_costs;"
    sql += (schema / "105_column_suggestions.sql").read_text()
    sql += """
    select jsonb_build_object(
      'previousCostsPreserved', (select count(*) from previous_column_costs)>0
        and not exists(select * from previous_column_costs except all select * from management_call_costs)
        and not exists(select * from management_call_costs except all select * from previous_column_costs),
      'noGeneratedAdvice', not exists(select from column_suggestions));
    """
    app, service, existing_project, existing_run = (str(uuid4()) for _ in range(4))
    sql += f"""
    insert into members(id,display_name,email,password_hash,role,kind)
      values('{app}','旧应用','old-app@sample.invalid','','member','application');
    insert into team_members values('{team}','{app}');
    insert into services(id,team_id,name,owner) values('{service}','{team}','旧服务','{member}');
    insert into service_versions(service_id,version,instructions,reviewer) values('{service}',1,'已发布原服务','{member}');
    insert into applications(id,service_id,token_hash,expires_at) values('{app}','{service}',decode('67','hex'),now()+interval '1 day');
    insert into projects(id,owner,final_reviewer,team_id,title,stage,plan_version)
      values('{existing_project}','{app}','{member}','{team}','旧应用项目','reconcile',1);
    insert into project_memberships values('{existing_project}','{app}',false),('{existing_project}','{member}',true);
    insert into plans(project_id,version,brief) values('{existing_project}',1,'旧应用方案');
    insert into runs(id,project_id,plan_version,actor,charge_team,status,request_key,request_hash,max_calls,app_id,service_id,service_version)
      values('{existing_run}','{existing_project}',1,'{app}','{team}','reconcile','old-app-execution',decode('44','hex'),1,'{app}','{service}',1);
    """
    sql += (schema / "106_service_column_recovery.sql").read_text()
    sql += f"""
    select jsonb_build_object(
      'reviewerMayConfirmColumns', exists(select from column_correction_authority where run_id='{existing_run}' and member_id='{member}'),
      'reviewerMayRetryUnknownCalls', exists(select from call_review_authority where run_id='{existing_run}' and member_id='{member}'),
      'applicationMayConfirmColumns', exists(select from column_correction_authority where run_id='{existing_run}' and member_id='{app}'));
    update project_memberships set can_review=false where project_id='{existing_project}' and member_id='{member}';
    select not exists(select from column_correction_authority where run_id='{existing_run}' and member_id='{member}');
    """
    sql += (schema / "107_column_reconfirmation.sql").read_text()
    sql += f"""
    select jsonb_build_object(
      'activeLegacyMappingPreserved', exists(select from active_call_redos where control_id='{control}'
        and input_mapping='{mapping}'::jsonb and superseded_by is null),
      'noReplacementInvented', not exists(select from call_reviews where superseded_by is not null),
      'previousCostsPreserved', not exists(select * from previous_column_costs except all select * from management_call_costs)
        and not exists(select * from management_call_costs except all select * from previous_column_costs));
    insert into invocations(run_id,ordinal,attempt_no,status,ended_at,input_issue)
      values('{existing_run}',1,1,'input_rejected',now(),'{issue}'::jsonb);
    with saved as (
      insert into controls(run_id,actor,request_key,signature,action,reason,revision)
        values('{existing_run}','{member}','old-autonomous-confirmation',decode('68','hex'),'call.redo','历史字段确认',2)
        returning id
    ) insert into call_reviews(control_id,run_id,ordinal,attempt_no,decision,input_mapping)
      select id,'{existing_run}',1,1,'redo','{mapping}'::jsonb from saved;
    select exists(select from pending_column_reconfirmations where run_id='{existing_run}');
    update invocations set input_issue=null where run_id='{existing_run}';
    select not exists(select from pending_column_reconfirmations where run_id='{existing_run}');
    """
    subprocess.run(pg + ["createdb", "-U", "alliance", db], check=True)
    try:
        process = subprocess.run(
            pg + ["psql", "-XAtq", "-v", "ON_ERROR_STOP=1", "-U", "alliance", "-d", db],
            input=sql,
            text=True,
            capture_output=True,
        )
        if process.returncode:
            raise RuntimeError(process.stderr[-1500:])
        before, after, no_issue, batch, advice, service_authority, reviewer_revoked, reconfirmation, needs_fields, erased = process.stdout.strip().splitlines()[-10:]
        expected = {
            "knownRejectionClosable": True,
            "unresolvedEarlierCallClosable": False,
            "attempts": ["unknown", "input_rejected"],
            "review": "redo",
        }
        assert json.loads(before) == json.loads(after) == expected and no_issue == "t"
        batch_result = json.loads(batch)
        assert batch_result == {"legacyReceiptPreserved": True, "legacyMappingPreserved": True}
        advice_result = json.loads(advice)
        assert advice_result == {"previousCostsPreserved": True, "noGeneratedAdvice": True}
        service_result = json.loads(service_authority)
        assert service_result == {
            "reviewerMayConfirmColumns": True, "reviewerMayRetryUnknownCalls": False, "applicationMayConfirmColumns": False,
        }
        assert reviewer_revoked == "t"
        service_result["reviewerRevocationEffective"] = True
        reconfirmation_result = json.loads(reconfirmation)
        assert reconfirmation_result == {
            "activeLegacyMappingPreserved": True, "noReplacementInvented": True, "previousCostsPreserved": True,
        }
        assert needs_fields == erased == "t"
        reconfirmation_result["removedReceiptNoLongerNeedsReconfirmation"] = True
        return {
            "before": json.loads(before),
            "after": json.loads(after),
            "legacyMappingsInvented": False,
            "batchUpgrade": batch_result,
            "adviceUpgrade": advice_result,
            "serviceUpgrade": service_result,
            "reconfirmationUpgrade": reconfirmation_result,
        }
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", db], check=True)

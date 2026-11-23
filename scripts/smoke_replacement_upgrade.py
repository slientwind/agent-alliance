"""Upgrade existing model-backed publications, service pins and open drafts from schema 077."""

import json
import subprocess
from pathlib import Path
from uuid import uuid4


def verify_replacement_upgrade(pg):
    database = "model_upgrade_" + uuid4().hex
    schema = Path(__file__).parent / "schema"
    if not schema.is_dir():
        schema = Path("backend/internal/database/schema")
    pg = [*pg[:-1], "-i", pg[-1]]
    sql = "\n".join(
        f.read_text() for f in sorted(schema.glob("*.sql")) if f.name[:3] <= "077"
    )
    # Two historical publications share the resource. One also has an unreviewed edit.
    sql += """
    create temp table fixture(actor uuid,team uuid,model uuid,untouched uuid,customized uuid,service uuid);
    insert into fixture select gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),gen_random_uuid();
    insert into members(id,display_name,email,password_hash,role) select actor,'Operator','upgrade@sample.invalid','','admin' from fixture;
    insert into teams(id,name) select team,'Upgrade' from fixture;
    insert into team_members select team,actor from fixture;
    insert into model_resources(id,name,team_id,created_by) select model,'Existing model',team,actor from fixture;
    insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key)
      select model,1,'https://example.invalid/v1','existing',decode('0123','hex') from fixture;
    insert into agents(id,owner,team_id) select untouched,actor,team from fixture union all select customized,actor,team from fixture;
    insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
      select a.id,1,'Original','Keep existing rules',f.model,1,f.actor from fixture f cross join agents a;
    insert into validations(agent_id,revision,actor,status,ended_at)
      select a.id,1,f.actor,'passed',now() from fixture f cross join agents a;
    update agents set published_revision=1;
    insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
      select customized,2,'Draft','Unpublished changes',model,1,actor from fixture;
    insert into services(id,team_id,name,owner) select service,team,'Frozen service',actor from fixture;
    insert into service_versions(service_id,version,instructions,reviewer) select service,1,'Summarize',actor from fixture;
    insert into service_nodes(service_id,version,ordinal,agent_id,agent_revision) select service,1,1,untouched,1 from fixture;
    """
    sql += (schema / "078_model_replacements.sql").read_text()
    sql += """
    select json_build_object(
      'eligibleOriginal',model_replacement_base_clear(f.untouched,1),
      'protectedCustomDraft',not model_replacement_base_clear(f.customized,1),
      'priorProofs',(select count(*) from agents a where agent_verified(a.id,1)),
      'modelKeyPreserved',(select encrypted_key=decode('0123','hex') from model_revisions),
      'currentPublications',(select count(*) from agents where published_revision=1 and publication_generation=0),
      'serviceRevision',(select agent_revision from service_nodes where service_id=f.service),
      'draftText',(select instructions from agent_revisions where(agent_id,revision)=(f.customized,2)),
      'inventedBatches',(select count(*) from model_replacements)) from fixture f;
    update agents set published_revision=1;
    select sum(publication_generation) from agents;
    """
    subprocess.run(pg + ["createdb", "-U", "alliance", database], check=True)
    try:
        result = subprocess.run(
            pg
            + [
                "psql",
                "-XAtq",
                "-v",
                "ON_ERROR_STOP=1",
                "-U",
                "alliance",
                "-d",
                database,
            ],
            input=sql,
            text=True,
            capture_output=True,
        )
        if result.returncode:
            raise RuntimeError(result.stderr[-1200:])
        lines = result.stdout.strip().splitlines()
        observed = json.loads(lines[-2])
        assert observed == dict(
            eligibleOriginal=True,
            protectedCustomDraft=True,
            priorProofs=2,
            modelKeyPreserved=True,
            currentPublications=2,
            serviceRevision=1,
            draftText="Unpublished changes",
            inventedBatches=0,
        ), observed
        assert lines[-1] == "0", lines[-1]
        return observed
    finally:
        subprocess.run(pg + ["dropdb", "-U", "alliance", database], check=True)

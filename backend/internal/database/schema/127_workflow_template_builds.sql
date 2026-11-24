ALTER TABLE workflow_template_versions ADD COLUMN manifest_digest text
 CHECK (manifest_digest ~ '^[0-9a-f]{64}$');

CREATE TABLE workflow_template_builds (
 template_id uuid NOT NULL,
 version integer NOT NULL,
 agent_id uuid NOT NULL,
 agent_revision integer NOT NULL,
 build_digest text NOT NULL,
 PRIMARY KEY(template_id,version,agent_id,agent_revision),
 FOREIGN KEY(template_id,version) REFERENCES workflow_template_versions(template_id,version),
 FOREIGN KEY(agent_id,agent_revision,build_digest) REFERENCES agent_build_manifests(agent_id,revision,digest)
);

CREATE TABLE plan_workflow_templates (
 project_id uuid NOT NULL,
 plan_version integer NOT NULL,
 template_id uuid NOT NULL,
 template_version integer NOT NULL,
 PRIMARY KEY(project_id,plan_version),
 FOREIGN KEY(project_id,plan_version) REFERENCES plans(project_id,version),
 FOREIGN KEY(project_id,template_id) REFERENCES workflow_template_uses(project_id,template_id),
 FOREIGN KEY(template_id,template_version) REFERENCES workflow_template_versions(template_id,version)
);

-- A replacement chosen in the new plan is governed by ordinary Agent
-- admission. A retained template revision must still match its frozen build.
CREATE FUNCTION workflow_build_usable(template_key uuid, template_version integer,
 agent_key uuid, version integer) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT FROM workflow_template_builds b
   WHERE (b.template_id,b.version,b.agent_id,b.agent_revision)=($1,$2,$3,$4)
     AND b.build_digest IS DISTINCT FROM agent_build_digest(agent_build_definition($3,$4)))
$$;

CREATE FUNCTION plan_workflow_build_usable(project_key uuid, plan_version integer,
 agent_key uuid, version integer) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT FROM plan_workflow_templates p
   WHERE (p.project_id,p.plan_version)=($1,$2)
     AND NOT workflow_build_usable(p.template_id,p.template_version,$3,$4))
$$;

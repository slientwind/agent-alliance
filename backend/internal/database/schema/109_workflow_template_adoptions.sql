ALTER TABLE workflow_template_uses ADD UNIQUE(project_id,template_id);

CREATE TABLE workflow_template_adoptions (
 project_id uuid NOT NULL,
 template_id uuid NOT NULL,
 from_version integer NOT NULL,
 to_version integer NOT NULL CHECK(to_version>from_version),
 previous_plan_version integer NOT NULL CHECK(previous_plan_version>=0),
 plan_version integer NOT NULL CHECK(plan_version>previous_plan_version),
 project_revision integer NOT NULL,
 actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,
 request_hash bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,project_revision),
 UNIQUE(actor,request_key),
 FOREIGN KEY(project_id,template_id) REFERENCES workflow_template_uses(project_id,template_id),
 FOREIGN KEY(project_id,plan_version) REFERENCES plans(project_id,version),
 FOREIGN KEY(template_id,from_version) REFERENCES workflow_template_versions(template_id,version),
 FOREIGN KEY(template_id,to_version) REFERENCES workflow_template_versions(template_id,version)
);

CREATE VIEW project_workflow_template_binding AS
 SELECT u.project_id,u.template_id,coalesce(a.to_version,u.version) AS version
 FROM workflow_template_uses u LEFT JOIN LATERAL (
   SELECT to_version FROM workflow_template_adoptions WHERE project_id=u.project_id ORDER BY project_revision DESC LIMIT 1
 ) a ON true;

CREATE FUNCTION offered_workflow_template_version(template uuid, actor uuid) RETURNS integer LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN t.owner=actor THEN t.current_version ELSE t.published_version END
 FROM workflow_templates t JOIN team_members tm ON (tm.team_id,tm.member_id)=(t.team_id,actor)
 JOIN members m ON m.id=tm.member_id AND m.enabled
 WHERE t.id=template AND t.archived_at IS NULL
$$;

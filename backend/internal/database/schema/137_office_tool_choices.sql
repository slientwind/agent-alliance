CREATE FUNCTION tool_choice_profile(tool jsonb) RETURNS text LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT CASE
  WHEN tool->>'operation' IN ('aggregate','docx','xlsx','pptx') AND coalesce(tool->>'contract','')=''
   THEN tool->>'operation'
  WHEN tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}' THEN 'pptx-outline-v1'
 END
$$;

CREATE TABLE run_tool_choice_waits (
 run_id uuid PRIMARY KEY,
 ordinal integer NOT NULL,
 control_revision integer NOT NULL,
 FOREIGN KEY(run_id,ordinal) REFERENCES run_tool_choices ON DELETE CASCADE
);

CREATE VIEW active_tool_choice_waits AS
 SELECT r.id AS run_id,c.ordinal
 FROM runs r
 JOIN run_tool_choices c ON c.run_id=r.id AND c.choice=0 AND c.manual_choice IS NULL
 JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,c.ordinal)
 JOIN agent_revisions a ON (a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
 LEFT JOIN run_tool_choice_waits w ON (w.run_id,w.ordinal,w.control_revision)=(r.id,c.ordinal,r.control_revision)
 WHERE r.status='blocked' AND (a.tool->>'operation'='aggregate' OR w.run_id IS NOT NULL);

CREATE FUNCTION service_requires_csv_choice(service uuid, rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM service_tool_choices c
  JOIN service_nodes n ON (n.service_id,n.version,n.ordinal)=(c.service_id,c.version,c.ordinal)
  JOIN agent_revisions a ON (a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
  WHERE (c.service_id,c.version)=(service,rev) AND a.tool->>'operation'='aggregate'
 )
$$;

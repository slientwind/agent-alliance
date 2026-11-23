ALTER TABLE planning_requests ADD COLUMN required_output text NOT NULL DEFAULT ''
  CHECK (required_output IN ('','docx','xlsx','pptx','png'));

CREATE OR REPLACE VIEW current_planning_requests AS
 SELECT q.id,q.project_id,q.actor,q.request_key,q.project_revision,q.resource_id,
        q.resource_revision,q.status,q.result,q.tokens,q.created_at,q.rule_pins,
        CASE WHEN q.status='ready' AND
          (q.rule_pins IS DISTINCT FROM planning_rule_pins(q.project_id,q.actor)
           OR NOT planning_sources_readable(q.project_id,q.input_sources))
        THEN 'stale' ELSE q.status END AS current_status,q.input_sources,q.required_output
 FROM planning_requests q;

CREATE FUNCTION agent_file_format(a agent_revisions) RETURNS text LANGUAGE sql STABLE AS $$
 SELECT CASE agent_output_operation(a)
   WHEN 'aggregate' THEN 'xlsx' WHEN 'extract' THEN 'text' ELSE agent_output_operation(a) END
$$;

CREATE FUNCTION agent_accepts_planning_inputs(a agent_revisions, pins jsonb) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT agent_output_operation(a) <> 'aggregate' OR NOT EXISTS (
   SELECT FROM jsonb_to_recordset(pins) p(id uuid,version integer)
   JOIN source_versions v ON (v.source_id,v.version)=(p.id,p.version)
   WHERE lower(right(v.filename,4)) <> '.csv')
$$;

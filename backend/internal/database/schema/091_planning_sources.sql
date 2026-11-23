-- NULL means the caller used the older planning contract, without selecting files.
ALTER TABLE planning_requests ADD COLUMN input_sources jsonb;

CREATE FUNCTION planning_sources_readable(project_key uuid, pins jsonb) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS (
   SELECT FROM jsonb_to_recordset(pins) x(id uuid, version integer)
   WHERE NOT EXISTS (
     SELECT FROM sources s JOIN source_versions v ON v.source_id=s.id
     WHERE s.project_id=project_key AND s.id=x.id AND v.version=x.version
       AND s.revoked_at IS NULL AND v.extracted IS NOT NULL
   )
 )
$$;

CREATE OR REPLACE VIEW current_planning_requests AS
 SELECT q.id,q.project_id,q.actor,q.request_key,q.project_revision,q.resource_id,
        q.resource_revision,q.status,q.result,q.tokens,q.created_at,q.rule_pins,
        CASE WHEN q.status='ready' AND
          (q.rule_pins IS DISTINCT FROM planning_rule_pins(q.project_id,q.actor)
           OR NOT planning_sources_readable(q.project_id,q.input_sources))
        THEN 'stale' ELSE q.status END AS current_status,q.input_sources
 FROM planning_requests q;

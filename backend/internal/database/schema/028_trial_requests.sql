ALTER TABLE validations ADD COLUMN request_key text;
ALTER TABLE validations ADD UNIQUE (actor, request_key);
CREATE VIEW trial_results AS
 SELECT v.*, CASE WHEN v.status='submitted' AND v.created_at +
     COALESCE((r.remote_policy->>'executionSeconds')::integer,
              CASE WHEN r.tool IS NOT NULL THEN 45 ELSE 90 END) * interval '1 second' <= now()
     THEN 'unknown' ELSE v.status END AS current_status
 FROM validations v JOIN agent_revisions r ON (r.agent_id, r.revision)=(v.agent_id, v.revision);

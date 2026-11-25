CREATE TABLE event_trigger_notices (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 batch_id uuid NOT NULL REFERENCES event_trigger_batches(id) ON DELETE CASCADE,
 member_id uuid NOT NULL REFERENCES members(id),
 channel text NOT NULL CHECK(channel IN ('feishu','dingtalk')),
 phase text NOT NULL CHECK(phase IN ('started','completed')),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','sending','sent','failed','unknown','revoked')),
 next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 attempts int NOT NULL DEFAULT 0,
 receipt text,
 UNIQUE(batch_id,member_id,channel,phase)
);
CREATE INDEX event_trigger_notices_queue ON event_trigger_notices(channel,next_at,id) WHERE state IN ('pending','sending');

ALTER TABLE business_mutation_notices ADD COLUMN expected_revision integer NOT NULL DEFAULT 1;

CREATE FUNCTION event_run_current(target uuid) RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT NOT EXISTS(
  SELECT FROM event_trigger_batches b JOIN event_triggers t ON t.id=b.trigger_id
   JOIN runs r ON r.id=b.run_id JOIN projects p ON p.id=r.project_id
  WHERE b.run_id=$1 AND (NOT t.enabled OR (t.revision,t.space_id,t.maintainer) IS DISTINCT FROM (b.trigger_revision,p.space_id,r.actor)
   OR coalesce(space_role(p.space_id,r.actor) NOT IN ('member','space_admin'),true)
   OR (t.connector_id IS NOT NULL AND NOT EXISTS(SELECT FROM business_connectors c WHERE c.id=t.connector_id AND c.enabled)))
 );
$$;

CREATE FUNCTION event_run_write_allowed(target uuid, connector uuid DEFAULT NULL) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT FROM event_trigger_batches b JOIN event_triggers t ON t.id=b.trigger_id
   WHERE b.run_id=$1 AND (NOT t.allow_write_preview OR t.connector_id IS NULL OR ($2 IS NOT NULL AND t.connector_id IS DISTINCT FROM $2)))
 AND event_run_current($1);
$$;

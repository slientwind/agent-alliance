ALTER TABLE planning_requests ADD COLUMN context_through bigint NOT NULL DEFAULT 0;
ALTER TABLE planning_requests ADD COLUMN context_compacted boolean NOT NULL DEFAULT false;

CREATE INDEX messages_project_user ON messages(project_id,seq) WHERE kind='user';

CREATE TABLE planning_context_messages (
 request_id uuid NOT NULL REFERENCES planning_requests ON DELETE CASCADE,
 message_seq bigint NOT NULL REFERENCES messages(seq),
 body_bytes integer NOT NULL CHECK (body_bytes >= 0),
 PRIMARY KEY(request_id,message_seq)
);

-- The ledger links only to earlier messages in the same project. UNION also
-- deduplicates ancestors reached through more than one conversation turn.
CREATE FUNCTION planning_context_requests(request_key uuid) RETURNS SETOF uuid
LANGUAGE sql STABLE AS $$
 WITH RECURSIVE history(id) AS (
   SELECT $1
   UNION
   SELECT m.planning_request FROM history h
   JOIN planning_context_messages c ON c.request_id=h.id
   JOIN messages m ON m.seq=c.message_seq
   WHERE m.planning_request IS NOT NULL
 )
 SELECT id FROM history
$$;

CREATE FUNCTION planning_context_readable(request_key uuid, viewer uuid,
 allowed_sources jsonb DEFAULT NULL, current_rules boolean DEFAULT true) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM planning_requests root JOIN project_members pm ON pm.project_id=root.project_id
               JOIN members m ON m.id=pm.member_id
               WHERE root.id=$1 AND m.id=$2 AND m.enabled)
 AND NOT EXISTS (
   SELECT FROM planning_context_requests($1) ancestor(id)
   JOIN planning_requests q ON q.id=ancestor.id
   WHERE (q.id<>$1 AND q.status<>'ready')
      OR ($4 AND q.rule_pins IS DISTINCT FROM planning_rule_pins(q.project_id,q.actor))
      OR NOT planning_rules_readable(q.rule_pins,$2)
      OR NOT planning_sources_readable(q.project_id,q.input_sources)
      OR ($3 IS NOT NULL AND NOT (coalesce(q.input_sources,'[]') <@ $3))
 )
$$;

CREATE OR REPLACE VIEW current_planning_requests AS
 SELECT q.id,q.project_id,q.actor,q.request_key,q.project_revision,q.resource_id,
        q.resource_revision,q.status,q.result,q.tokens,q.created_at,q.rule_pins,
        CASE WHEN q.status='ready' AND NOT planning_context_readable(q.id,q.actor)
        THEN 'stale' ELSE q.status END AS current_status,q.input_sources,q.required_output,
        q.context_through,q.context_compacted
 FROM planning_requests q;

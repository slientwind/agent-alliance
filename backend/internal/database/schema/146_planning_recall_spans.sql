ALTER TABLE planning_context_messages
    ADD COLUMN body_start integer NOT NULL DEFAULT 0 CHECK (body_start >= 0),
    ADD COLUMN body_hash bytea CHECK (octet_length(body_hash) = 32),
    ADD COLUMN recalled boolean NOT NULL DEFAULT false,
    ADD CHECK (NOT recalled OR (body_hash IS NOT NULL AND body_bytes > 0));

CREATE OR REPLACE FUNCTION planning_context_readable(request_key uuid, viewer uuid,
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
      OR EXISTS (
        SELECT FROM planning_context_messages c JOIN messages m ON m.seq=c.message_seq
        WHERE c.request_id=q.id AND
          (m.project_id<>q.project_id OR (c.recalled AND (m.kind<>'user' OR m.planning_request IS NOT NULL))
           OR (c.body_hash IS NOT NULL AND c.body_hash IS DISTINCT FROM
             digest(substring(convert_to(m.body,'UTF8') FROM c.body_start+1 FOR c.body_bytes),'sha256')))
      )
 )
$$;

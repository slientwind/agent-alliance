-- These rows keep their attempt and ordering keys after their payload is retired.
CREATE INDEX pending_text_reply_payloads ON text_reply_checkpoints(run_id,ordinal,attempt_no) WHERE state='pending';
CREATE INDEX open_lookup_question_payloads ON lookup_question_handoffs(run_id,parent,parent_attempt) WHERE question IS NOT NULL;

-- Legacy lookup rows without a working set remain on their original lifecycle.
CREATE FUNCTION retire_invalid_working_payloads(batch_size integer) RETURNS bigint
LANGUAGE sql AS $$
 WITH payload_attempts AS (
   SELECT run_id,ordinal,attempt_no FROM text_reply_checkpoints WHERE state='pending'
   UNION
   SELECT run_id,parent,parent_attempt FROM lookup_question_handoffs WHERE question IS NOT NULL
   UNION
   SELECT run_id,ordinal,attempt_no FROM run_draft_fragments
   UNION
   SELECT run_id,ordinal,attempt_no FROM lookup_turns
   WHERE decision IS NOT NULL OR refinement_evidence IS NOT NULL OR initial_evidence IS NOT NULL
   UNION
   SELECT i.run_id,i.ordinal,i.attempt_no FROM invocations i
   WHERE i.status='completed' AND i.result IS NOT NULL
     AND NOT EXISTS(SELECT FROM artifacts a WHERE (a.run_id,a.ordinal)=(i.run_id,i.ordinal))
     AND NOT EXISTS(SELECT FROM change_batches c WHERE c.run_id=i.run_id AND c.applied_version IS NOT NULL)
 ), attempts AS MATERIALIZED (
   SELECT s.run_id,s.ordinal,s.attempt_no FROM payload_attempts p
   JOIN run_working_sets s USING(run_id,ordinal,attempt_no)
   JOIN runs r ON r.id=s.run_id
   WHERE NOT run_working_set_usable(s.run_id,s.ordinal,s.attempt_no)
   ORDER BY s.run_id,s.ordinal,s.attempt_no
   LIMIT batch_size FOR NO KEY UPDATE OF r SKIP LOCKED
 ), drafts AS (
   DELETE FROM run_draft_fragments f USING attempts a
   WHERE (f.run_id,f.ordinal,f.attempt_no)=(a.run_id,a.ordinal,a.attempt_no)
 ), replies AS (
   UPDATE text_reply_checkpoints c SET state='discarded',payload=NULL FROM attempts a
   WHERE (c.run_id,c.ordinal,c.attempt_no)=(a.run_id,a.ordinal,a.attempt_no) AND c.state='pending'
 ), questions AS (
   UPDATE lookup_question_handoffs q SET question=NULL FROM attempts a
   WHERE (q.run_id,q.parent,q.parent_attempt)=(a.run_id,a.ordinal,a.attempt_no) AND q.question IS NOT NULL
 ), turns AS (
   -- Ending the decision also discards its pending local observations.
   UPDATE lookup_turns t SET decision=NULL,refinement_evidence=NULL,initial_evidence=NULL,
     outcome=CASE WHEN t.decision IS NOT NULL THEN 'invalidated' ELSE t.outcome END
   FROM attempts a WHERE (t.run_id,t.ordinal,t.attempt_no)=(a.run_id,a.ordinal,a.attempt_no)
     AND (t.decision IS NOT NULL OR t.refinement_evidence IS NOT NULL OR t.initial_evidence IS NOT NULL)
 ), results AS (
   UPDATE invocations i SET result=NULL FROM attempts a
   WHERE (i.run_id,i.ordinal,i.attempt_no)=(a.run_id,a.ordinal,a.attempt_no)
     AND i.status='completed' AND i.result IS NOT NULL
     AND NOT EXISTS(SELECT FROM artifacts a WHERE (a.run_id,a.ordinal)=(i.run_id,i.ordinal))
     AND NOT EXISTS(SELECT FROM change_batches c WHERE c.run_id=i.run_id AND c.applied_version IS NOT NULL)
 )
 SELECT count(*) FROM attempts
$$;

-- Changes found by a sweep may have no accompanying business-row update.
CREATE CONSTRAINT TRIGGER project_session_reply_discarded AFTER UPDATE ON text_reply_checkpoints
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN (OLD.state='pending' AND NEW.state='discarded') EXECUTE FUNCTION publish_project_session_event();

CREATE CONSTRAINT TRIGGER project_session_question_erased AFTER UPDATE ON lookup_question_handoffs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN (OLD.question IS NOT NULL AND NEW.question IS NULL) EXECUTE FUNCTION publish_project_session_event();

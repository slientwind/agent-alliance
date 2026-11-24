ALTER TABLE nodes DROP CONSTRAINT nodes_lookup_phase_check,
    ADD CHECK (lookup_phase IN ('', 'search', 'answer', 'answer_calc', 'synthesize'));
ALTER TABLE service_nodes DROP CONSTRAINT service_nodes_lookup_phase_check,
    ADD CHECK (lookup_phase IN ('', 'search', 'answer', 'answer_calc', 'synthesize'));

ALTER TABLE lookup_turns ADD COLUMN refinement_evidence jsonb;

CREATE FUNCTION evidence_follow_up_skipped(run_key uuid, step_no integer) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT exists(select from trace_events where run_id=run_key and ordinal=step_no and code='lookup_synthesis_skipped')
$$;

CREATE OR REPLACE FUNCTION clear_invalidated_local_action() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns
    SET decision=NULL,outcome=case when decision is not null then 'invalidated' else outcome end,refinement_evidence=NULL
    WHERE (run_id,ordinal,attempt_no)=(NEW.run_id,NEW.ordinal,NEW.attempt_no)
      AND (decision IS NOT NULL OR refinement_evidence IS NOT NULL);
    RETURN NULL;
END $$;

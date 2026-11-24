CREATE TABLE text_reply_checkpoints (
    run_id uuid NOT NULL,
    ordinal integer NOT NULL,
    attempt_no integer NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','consumed','discarded','incomplete')),
    payload bytea,
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (run_id,ordinal,attempt_no),
    FOREIGN KEY (run_id,ordinal,attempt_no) REFERENCES run_working_sets ON DELETE CASCADE,
    CHECK ((state='pending') = (payload IS NOT NULL))
);

CREATE FUNCTION discard_run_text_replies() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE text_reply_checkpoints SET state='discarded',payload=NULL
 WHERE run_id=NEW.id AND state='pending';
 RETURN NULL;
END $$;
CREATE TRIGGER discard_text_replies_on_run_end AFTER UPDATE OF status ON runs
 FOR EACH ROW WHEN (NEW.status IN ('completed','cancelled','failed') AND OLD.status IS DISTINCT FROM NEW.status)
 EXECUTE FUNCTION discard_run_text_replies();

CREATE FUNCTION discard_invalidated_text_reply() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE text_reply_checkpoints SET state='discarded',payload=NULL
 WHERE (run_id,ordinal,attempt_no)=(NEW.run_id,NEW.ordinal,NEW.attempt_no) AND state='pending';
 RETURN NULL;
END $$;
CREATE TRIGGER discard_text_reply_on_input_invalidation AFTER UPDATE OF invalidated_at ON run_working_sets
 FOR EACH ROW WHEN (OLD.invalidated_at IS NULL AND NEW.invalidated_at IS NOT NULL)
 EXECUTE FUNCTION discard_invalidated_text_reply();

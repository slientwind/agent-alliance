CREATE TABLE lookup_question_handoffs (
    run_id uuid NOT NULL REFERENCES runs ON DELETE CASCADE,
    target integer NOT NULL CHECK (target > 0),
    parent integer NOT NULL CHECK (parent > 0 AND parent < target),
    parent_attempt integer NOT NULL,
    plan_version integer NOT NULL,
    question text CHECK (char_length(question) BETWEEN 1 AND 320),
    citations jsonb NOT NULL,
    PRIMARY KEY (run_id,target),
    FOREIGN KEY (run_id,parent,parent_attempt) REFERENCES run_working_sets ON DELETE CASCADE
);

CREATE VIEW lookup_question_availability AS
SELECT q.*,q.plan_version=r.plan_version AND i.status='completed' AND i.attempt_no=q.parent_attempt
    AND run_working_set_usable(q.run_id,q.parent,q.parent_attempt) AS usable
FROM lookup_question_handoffs q JOIN runs r ON r.id=q.run_id
JOIN invocations i ON (i.run_id,i.ordinal)=(q.run_id,q.parent);

CREATE FUNCTION erase_lookup_question_on_invalidation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_question_handoffs SET question=NULL
    WHERE (run_id,parent,parent_attempt)=(NEW.run_id,NEW.ordinal,NEW.attempt_no)
      AND question IS NOT NULL;
    RETURN NULL;
END $$;

CREATE TRIGGER lookup_question_parent_invalidated AFTER UPDATE OF invalidated_at ON run_working_sets
FOR EACH ROW WHEN (OLD.invalidated_at IS NULL AND NEW.invalidated_at IS NOT NULL)
EXECUTE FUNCTION erase_lookup_question_on_invalidation();

CREATE FUNCTION erase_lookup_questions_on_run_end() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_question_handoffs SET question=NULL WHERE run_id=NEW.id AND question IS NOT NULL;
    RETURN NULL;
END $$;

CREATE TRIGGER lookup_question_run_ended AFTER UPDATE OF status ON runs
FOR EACH ROW WHEN (NEW.status IS DISTINCT FROM OLD.status AND NEW.status IN ('completed','cancelled','failed'))
EXECUTE FUNCTION erase_lookup_questions_on_run_end();

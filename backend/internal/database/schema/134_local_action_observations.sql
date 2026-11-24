-- Pending observations are separate from publishable invocation results.
-- Old decisions continue to commit their result in the same activity.
ALTER TABLE lookup_turns ADD COLUMN observe_before_commit boolean NOT NULL DEFAULT false;

CREATE TABLE local_action_observations (
    run_id uuid NOT NULL,
    ordinal integer NOT NULL,
    attempt_no integer NOT NULL,
    control_revision integer NOT NULL CHECK (control_revision>0),
    payload bytea,
    outcome text NOT NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    consumed_at timestamptz,
    discarded_at timestamptz,
    PRIMARY KEY (run_id,ordinal,attempt_no),
    FOREIGN KEY (run_id,ordinal,attempt_no) REFERENCES lookup_turns ON DELETE CASCADE,
    CHECK (num_nonnulls(payload,consumed_at,discarded_at)=1)
);

CREATE FUNCTION discard_pending_local_observation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE local_action_observations SET discarded_at=now(),payload=NULL
    WHERE (run_id,ordinal,attempt_no)=(NEW.run_id,NEW.ordinal,NEW.attempt_no) AND payload IS NOT NULL;
    RETURN NULL;
END $$;

CREATE TRIGGER local_observation_decision_ended AFTER UPDATE OF decision ON lookup_turns
FOR EACH ROW WHEN (OLD.decision IS NOT NULL AND NEW.decision IS NULL)
EXECUTE FUNCTION discard_pending_local_observation();

CREATE CONSTRAINT TRIGGER project_session_local_observation AFTER INSERT ON local_action_observations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();

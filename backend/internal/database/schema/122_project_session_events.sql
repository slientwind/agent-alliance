CREATE TABLE project_session_heads (
    project_id uuid PRIMARY KEY REFERENCES projects ON DELETE CASCADE,
    seq bigint NOT NULL,
    last_change_tx bigint
);

CREATE TABLE project_session_events (
    project_id uuid NOT NULL REFERENCES project_session_heads ON DELETE CASCADE,
    seq bigint NOT NULL,
    run_id uuid,
    draft_seq bigint,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((run_id IS NULL) = (draft_seq IS NULL)),
    PRIMARY KEY (project_id, seq)
);
CREATE INDEX project_session_event_expiry ON project_session_events(created_at);

-- Deferred writers take the sequence lock at the end of the business transaction.
-- A state invalidation is sufficient once per project and transaction. Draft text
-- stays in its original table and is joined only for currently authorized attempts.
CREATE FUNCTION publish_project_session_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    pid uuid;
    rid uuid;
    fragment bigint;
    next_seq bigint;
    change_tx bigint;
BEGIN
    CASE TG_TABLE_NAME
        WHEN 'projects' THEN pid := NEW.id;
        WHEN 'messages' THEN pid := NEW.project_id;
        WHEN 'runs' THEN pid := NEW.project_id;
        ELSE
            SELECT project_id INTO pid FROM runs WHERE id=NEW.run_id;
            IF TG_TABLE_NAME='run_draft_fragments' THEN
                rid := NEW.run_id;
                fragment := NEW.seq;
            END IF;
    END CASE;
    IF pid IS NULL THEN RETURN NULL; END IF;
    IF fragment IS NULL THEN change_tx := txid_current(); END IF;
    INSERT INTO project_session_heads AS h(project_id,seq,last_change_tx) VALUES(pid,1,change_tx)
    ON CONFLICT(project_id) DO UPDATE
        SET seq=h.seq+1,last_change_tx=coalesce(change_tx,h.last_change_tx)
        WHERE change_tx IS NULL OR h.last_change_tx IS DISTINCT FROM change_tx
    RETURNING seq INTO next_seq;
    IF next_seq IS NOT NULL THEN
        INSERT INTO project_session_events(project_id,seq,run_id,draft_seq) VALUES(pid,next_seq,rid,fragment);
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER project_session_project AFTER UPDATE ON projects
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN ((NEW.revision,NEW.stage,NEW.owner,NEW.final_reviewer) IS DISTINCT FROM (OLD.revision,OLD.stage,OLD.owner,OLD.final_reviewer))
EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_message AFTER INSERT ON messages
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_run_created AFTER INSERT ON runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_run_changed AFTER UPDATE ON runs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN ((NEW.status,NEW.control_revision) IS DISTINCT FROM (OLD.status,OLD.control_revision))
EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_attempt_created AFTER INSERT ON invocations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_attempt_changed AFTER UPDATE ON invocations
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN ((NEW.status,NEW.attempt_no,NEW.remote_status,(NEW.result IS NULL)) IS DISTINCT FROM
      (OLD.status,OLD.attempt_no,OLD.remote_status,(OLD.result IS NULL)))
EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_remote_job AFTER UPDATE ON remote_jobs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN ((NEW.phase,NEW.detail,NEW.input_revision) IS DISTINCT FROM (OLD.phase,OLD.detail,OLD.input_revision))
EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_remote_input AFTER UPDATE ON remote_task_inputs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN (NEW.state IS DISTINCT FROM OLD.state)
EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_lookup AFTER INSERT OR UPDATE ON lookup_turns
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_invalidation AFTER UPDATE ON run_working_sets
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
WHEN (NEW.invalidated_at IS DISTINCT FROM OLD.invalidated_at)
EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_draft AFTER INSERT ON run_draft_fragments
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();

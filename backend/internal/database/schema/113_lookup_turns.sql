CREATE TABLE lookup_turns (
    run_id uuid NOT NULL,
    ordinal integer NOT NULL,
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    plan_version integer NOT NULL,
    decision bytea,
    outcome text,
    PRIMARY KEY (run_id, ordinal, attempt_no),
    FOREIGN KEY (run_id, ordinal) REFERENCES invocations ON DELETE CASCADE,
    CHECK ((decision IS NULL) <> (outcome IS NULL))
);
CREATE INDEX pending_lookup_turns ON lookup_turns(run_id,plan_version) WHERE decision IS NOT NULL;

CREATE FUNCTION clear_ended_lookup_turns() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns SET decision=NULL,outcome='run_ended'
    WHERE run_id=NEW.id AND decision IS NOT NULL;
    RETURN NULL;
END $$;

CREATE TRIGGER ended_lookup_turns AFTER UPDATE OF status ON runs FOR EACH ROW
WHEN (NEW.status IS DISTINCT FROM OLD.status AND NEW.status IN ('completed','failed','cancelled'))
EXECUTE FUNCTION clear_ended_lookup_turns();

CREATE FUNCTION clear_revoked_lookup_turns() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE lookup_turns t SET decision=NULL,outcome='invalidated'
    FROM runs r, plan_sources p
    WHERE r.id=t.run_id AND p.project_id=r.project_id AND p.plan_version=t.plan_version
      AND p.source_id=NEW.id AND t.decision IS NOT NULL;
    RETURN NULL;
END $$;

CREATE TRIGGER revoked_lookup_turns AFTER UPDATE OF revoked_at ON sources FOR EACH ROW
WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL)
EXECUTE FUNCTION clear_revoked_lookup_turns();

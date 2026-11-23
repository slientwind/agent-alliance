ALTER TABLE runs ADD COLUMN finished_at timestamptz;
UPDATE runs r SET finished_at=greatest(r.created_at,
 (SELECT max(ended_at) FROM invocations WHERE run_id=r.id),
 (SELECT max(created_at) FROM controls WHERE run_id=r.id))
 WHERE r.status IN ('completed','cancelled');
CREATE INDEX runs_finished ON runs(finished_at) WHERE finished_at IS NOT NULL;

CREATE TABLE trace_grants (
 run_id uuid REFERENCES runs, member_id uuid REFERENCES members,
 scope text CHECK(scope IN ('structure','content','export')), expires_at timestamptz NOT NULL,
 PRIMARY KEY(run_id,member_id,scope)
);
CREATE TABLE trace_events (
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 run_id uuid NOT NULL REFERENCES runs, ordinal integer, kind text NOT NULL, code text NOT NULL,
 occurred_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX trace_events_run ON trace_events(run_id,seq);

CREATE FUNCTION trace_run_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' OR OLD.status IS DISTINCT FROM NEW.status THEN
   IF NEW.status IN ('completed','cancelled') THEN NEW.finished_at=clock_timestamp(); END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER trace_finish BEFORE INSERT OR UPDATE OF status ON runs FOR EACH ROW EXECUTE FUNCTION trace_run_transition();

CREATE FUNCTION trace_transition_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.status IS NOT DISTINCT FROM OLD.status THEN RETURN NULL; END IF;
 IF TG_TABLE_NAME='runs' THEN
   INSERT INTO trace_events(run_id,kind,code) VALUES(NEW.id,'run',NEW.status);
 ELSE
   INSERT INTO trace_events(run_id,ordinal,kind,code) VALUES(NEW.run_id,NEW.ordinal,'call',NEW.status);
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER trace_runs AFTER INSERT OR UPDATE OF status ON runs FOR EACH ROW EXECUTE FUNCTION trace_transition_event();
CREATE TRIGGER trace_calls AFTER INSERT OR UPDATE OF status ON invocations FOR EACH ROW EXECUTE FUNCTION trace_transition_event();

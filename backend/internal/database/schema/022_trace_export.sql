CREATE TABLE telemetry_outbox (
 run_id uuid REFERENCES runs, ordinal integer NOT NULL, attempts integer NOT NULL DEFAULT 0,
 next_at timestamptz NOT NULL DEFAULT now(), lease uuid, outcome text NOT NULL DEFAULT 'pending',
 PRIMARY KEY(run_id,ordinal)
);
CREATE INDEX telemetry_due ON telemetry_outbox(next_at) WHERE outcome='pending';
CREATE FUNCTION enqueue_trace_span() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='runs' THEN
   IF NEW.finished_at IS NOT NULL THEN
     INSERT INTO telemetry_outbox(run_id,ordinal) VALUES(NEW.id,0) ON CONFLICT DO NOTHING;
   END IF;
 ELSIF NEW.ended_at IS NOT NULL THEN
   INSERT INTO telemetry_outbox(run_id,ordinal) VALUES(NEW.run_id,NEW.ordinal) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER telemetry_run AFTER UPDATE OF status ON runs FOR EACH ROW EXECUTE FUNCTION enqueue_trace_span();
CREATE TRIGGER telemetry_call AFTER UPDATE OF ended_at ON invocations FOR EACH ROW EXECUTE FUNCTION enqueue_trace_span();

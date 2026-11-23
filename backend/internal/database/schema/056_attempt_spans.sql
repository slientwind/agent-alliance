ALTER TABLE telemetry_outbox ADD COLUMN call_attempt integer NOT NULL DEFAULT 1;
ALTER TABLE telemetry_outbox DROP CONSTRAINT telemetry_outbox_pkey,
 ADD PRIMARY KEY(run_id,ordinal,call_attempt);
CREATE OR REPLACE FUNCTION enqueue_trace_span() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='runs' THEN
   IF NEW.finished_at IS NOT NULL THEN
     INSERT INTO telemetry_outbox(run_id,ordinal) VALUES(NEW.id,0) ON CONFLICT DO NOTHING;
   END IF;
 ELSIF NEW.ended_at IS NOT NULL THEN
   INSERT INTO telemetry_outbox(run_id,ordinal,call_attempt) VALUES(NEW.run_id,NEW.ordinal,NEW.attempt_no) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NULL;
END $$;

CREATE FUNCTION call_span_id(uuid,integer,integer) RETURNS bytea LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT substr(digest($1::text||'/'||$2::text||CASE WHEN $3=1 THEN '' ELSE '/attempt/'||$3::text END,'sha256'),1,8)
$$;
ALTER TABLE trace_events ADD COLUMN call_attempt integer;
CREATE FUNCTION assign_event_attempt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.ordinal IS NOT NULL THEN
    SELECT attempt_no INTO NEW.call_attempt FROM invocations WHERE run_id=NEW.run_id AND ordinal=NEW.ordinal;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER trace_event_attempt BEFORE INSERT ON trace_events FOR EACH ROW EXECUTE FUNCTION assign_event_attempt();

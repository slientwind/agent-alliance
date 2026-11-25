ALTER TABLE model_gateway_attempts ADD COLUMN first_chunk_at timestamptz,
 ADD COLUMN streamed boolean NOT NULL DEFAULT false,
 ADD CHECK(first_chunk_at IS NULL OR (streamed AND first_chunk_at BETWEEN started_at AND finished_at));

CREATE TABLE genai_telemetry_outbox (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL REFERENCES runs ON DELETE CASCADE,
 operation_key text UNIQUE REFERENCES model_gateway_attempts ON DELETE CASCADE,
 evaluation_score smallint CHECK(evaluation_score IN (0,1)),
 occurred_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 next_at timestamptz NOT NULL DEFAULT now(),
 lease uuid,
 outcome text NOT NULL DEFAULT 'pending',
 CHECK((operation_key IS NULL) <> (evaluation_score IS NULL))
);
CREATE INDEX genai_telemetry_due ON genai_telemetry_outbox(next_at,id) WHERE outcome='pending';
CREATE INDEX genai_telemetry_run ON genai_telemetry_outbox(run_id);

CREATE FUNCTION enqueue_genai_telemetry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT FROM runs r WHERE r.id=NEW.run_id AND (r.trace_expires_at IS NULL OR r.trace_expires_at>clock_timestamp())) THEN
  IF TG_TABLE_NAME='model_gateway_attempts' THEN
   INSERT INTO genai_telemetry_outbox(run_id,operation_key,occurred_at) VALUES(NEW.run_id,NEW.operation_key,NEW.finished_at);
  ELSE
   INSERT INTO genai_telemetry_outbox(run_id,evaluation_score,occurred_at)
    VALUES(NEW.run_id,(NEW.kind='feedback_thumbs_up')::int,NEW.recorded_at);
  END IF;
 END IF;
 RETURN NULL;
END
$$;
CREATE TRIGGER genai_model_span AFTER INSERT ON model_gateway_attempts
 FOR EACH ROW WHEN (NEW.run_id IS NOT NULL) EXECUTE FUNCTION enqueue_genai_telemetry();
CREATE TRIGGER genai_feedback_score AFTER INSERT ON run_quality_signals
 FOR EACH ROW WHEN (NEW.kind IN ('feedback_thumbs_up','feedback_thumbs_down')) EXECUTE FUNCTION enqueue_genai_telemetry();

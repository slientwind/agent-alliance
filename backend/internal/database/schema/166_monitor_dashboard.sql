CREATE TABLE monitor_run_hourly_stats (
 bucket_hour timestamptz NOT NULL,
 team_id uuid NOT NULL,
 status text NOT NULL,
 quality_track text NOT NULL,
 total bigint NOT NULL,
 output_passed bigint NOT NULL,
 accepted bigint NOT NULL,
 PRIMARY KEY (bucket_hour,team_id,status,quality_track)
);

ALTER TABLE monitor_agent_hourly_stats ADD COLUMN latency_histogram jsonb NOT NULL DEFAULT '{}';
ALTER TABLE model_gateway_attempts ADD COLUMN turn integer CHECK(turn>0);
CREATE INDEX monitor_active_queue ON runs(created_at,id) WHERE status IN ('queued','running','blocked','reconcile','paused');

CREATE FUNCTION monitor_latency_bucket(latency numeric) RETURNS integer LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT CASE WHEN latency<1000 THEN ceil(latency/10)*10 WHEN latency<10000 THEN ceil(latency/100)*100
  ELSE least(2147483000,ceil(latency/1000)*1000) END::integer
$$;

CREATE OR REPLACE FUNCTION monitor_mark_run_hours(target uuid) RETURNS void LANGUAGE sql AS $$
 INSERT INTO monitor_rollup_dirty(bucket_hour)
 SELECT date_trunc('hour',started,'UTC') FROM (
  SELECT created_at started FROM runs WHERE id=target
  UNION ALL SELECT started_at FROM invocations WHERE run_id=target
  UNION ALL SELECT started_at FROM superseded_calls WHERE run_id=target
  UNION ALL SELECT started_at FROM model_gateway_attempts WHERE run_id=target
 ) calls GROUP BY 1
$$;

CREATE TRIGGER monitor_run_inserted AFTER INSERT ON runs FOR EACH ROW EXECUTE FUNCTION monitor_run_facts_changed();

CREATE TRIGGER monitor_reviews_changed AFTER INSERT OR DELETE ON reviews FOR EACH ROW EXECUTE FUNCTION monitor_delivery_facts_changed();

INSERT INTO monitor_rollup_dirty(bucket_hour)
 SELECT DISTINCT hour FROM (SELECT date_trunc('hour',created_at,'UTC') as hour FROM runs
 UNION ALL SELECT bucket_hour FROM monitor_agent_hourly_stats) backfill;

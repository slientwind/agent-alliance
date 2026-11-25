ALTER TABLE model_gateway_attempts ADD COLUMN run_id uuid REFERENCES runs,
 ADD COLUMN agent_id uuid REFERENCES agents;

CREATE INDEX invocation_monitor_time ON invocations(started_at);
CREATE INDEX superseded_monitor_time ON superseded_calls(started_at);
CREATE INDEX model_gateway_attempts_run ON model_gateway_attempts(run_id,started_at) WHERE run_id IS NOT NULL;

CREATE TABLE monitor_agent_hourly_stats (
 bucket_hour timestamptz NOT NULL,
 team_id uuid NOT NULL,
 agent_id uuid NOT NULL,
 source_type text NOT NULL,
 model_id uuid,
 tier text,
 runs_total bigint NOT NULL,
 runs_succeeded bigint NOT NULL,
 calls_total bigint NOT NULL,
 calls_succeeded bigint NOT NULL,
 lightweight_total bigint NOT NULL,
 lightweight_effective bigint NOT NULL,
 collaborative_total bigint NOT NULL,
 collaborative_approved bigint NOT NULL,
 p50_ms bigint,
 p95_ms bigint,
 prompt_tokens bigint NOT NULL,
 completion_tokens bigint NOT NULL,
 cached_tokens bigint NOT NULL,
 tokens_total bigint NOT NULL,
 cost_micro_cny numeric NOT NULL,
 undisclosed_calls bigint NOT NULL,
 UNIQUE NULLS NOT DISTINCT (bucket_hour,team_id,agent_id,source_type,model_id,tier)
);

CREATE TABLE monitor_model_hourly_stats (
 bucket_hour timestamptz NOT NULL,
 team_id uuid NOT NULL,
 model_id uuid NOT NULL,
 provider_id uuid,
 tier text,
 calls_total bigint NOT NULL,
 calls_succeeded bigint NOT NULL,
 calls_failed bigint NOT NULL,
 fallback_count bigint NOT NULL,
 p50_ms bigint,
 p95_ms bigint,
 prompt_tokens bigint NOT NULL,
 completion_tokens bigint NOT NULL,
 cached_tokens bigint NOT NULL,
 tokens_total bigint NOT NULL,
 cost_micro_cny numeric NOT NULL,
 undisclosed_calls bigint NOT NULL,
 UNIQUE NULLS NOT DISTINCT (bucket_hour,team_id,model_id,provider_id,tier)
);

CREATE TABLE monitor_rollup_dirty (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 bucket_hour timestamptz NOT NULL
);

CREATE FUNCTION monitor_mark_call_hour() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'DELETE' THEN
  INSERT INTO monitor_rollup_dirty(bucket_hour) VALUES(date_trunc('hour',NEW.started_at,'UTC'));
 END IF;
 IF TG_OP='DELETE' OR (TG_OP='UPDATE' AND OLD.started_at IS DISTINCT FROM NEW.started_at) THEN
  INSERT INTO monitor_rollup_dirty(bucket_hour) VALUES(date_trunc('hour',OLD.started_at,'UTC'));
 END IF;
 RETURN NULL;
END $$;

CREATE TRIGGER monitor_invocations_changed AFTER INSERT OR DELETE OR UPDATE OF tokens,status,started_at,ended_at ON invocations
 FOR EACH ROW EXECUTE FUNCTION monitor_mark_call_hour();
CREATE TRIGGER monitor_superseded_changed AFTER INSERT OR DELETE ON superseded_calls
 FOR EACH ROW EXECUTE FUNCTION monitor_mark_call_hour();
CREATE TRIGGER monitor_gateway_attempts_changed AFTER INSERT OR DELETE ON model_gateway_attempts
 FOR EACH ROW EXECUTE FUNCTION monitor_mark_call_hour();

CREATE FUNCTION monitor_mark_run_hours(target uuid) RETURNS void LANGUAGE sql AS $$
 INSERT INTO monitor_rollup_dirty(bucket_hour)
 SELECT date_trunc('hour',started,'UTC') FROM (
  SELECT started_at started FROM invocations WHERE run_id=target
  UNION ALL SELECT started_at FROM superseded_calls WHERE run_id=target
  UNION ALL SELECT started_at FROM model_gateway_attempts WHERE run_id=target
 ) calls GROUP BY 1
$$;

CREATE FUNCTION monitor_run_facts_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='runs' THEN
  PERFORM monitor_mark_run_hours(NEW.id);
 ELSIF TG_OP='DELETE' THEN
  PERFORM monitor_mark_run_hours(OLD.run_id);
 ELSE
  PERFORM monitor_mark_run_hours(NEW.run_id);
 END IF;
 RETURN NULL;
END $$;

CREATE TRIGGER monitor_run_updated AFTER UPDATE OF status,quality_track ON runs
 FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status OR OLD.quality_track IS DISTINCT FROM NEW.quality_track)
 EXECUTE FUNCTION monitor_run_facts_changed();
CREATE TRIGGER monitor_run_signals_changed AFTER INSERT OR DELETE ON run_quality_signals
 FOR EACH ROW EXECUTE FUNCTION monitor_run_facts_changed();
CREATE TRIGGER monitor_run_receipts_changed AFTER INSERT OR UPDATE ON late_call_receipts
 FOR EACH ROW EXECUTE FUNCTION monitor_run_facts_changed();
CREATE TRIGGER monitor_run_charges_changed AFTER INSERT OR DELETE OR UPDATE OF amount_cny,settled ON spend
 FOR EACH ROW EXECUTE FUNCTION monitor_run_facts_changed();

CREATE FUNCTION monitor_delivery_facts_changed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM monitor_mark_run_hours(run_id) FROM artifacts WHERE id = CASE WHEN TG_OP='DELETE' THEN OLD.artifact_id ELSE NEW.artifact_id END;
 RETURN NULL;
END $$;
CREATE TRIGGER monitor_delivery_items_changed AFTER INSERT OR DELETE ON delivery_items
 FOR EACH ROW EXECUTE FUNCTION monitor_delivery_facts_changed();

CREATE FUNCTION monitor_gateway_billed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO monitor_rollup_dirty(bucket_hour) SELECT date_trunc('hour',started_at,'UTC') FROM model_gateway_attempts
 WHERE operation_key=NEW.operation_key;
 RETURN NULL;
END $$;
CREATE TRIGGER monitor_gateway_charges_changed AFTER UPDATE OF state ON model_gateway_charges
 FOR EACH ROW WHEN (OLD.state IS DISTINCT FROM NEW.state) EXECUTE FUNCTION monitor_gateway_billed();

INSERT INTO monitor_rollup_dirty(bucket_hour)
 SELECT DISTINCT date_trunc('hour',started_at,'UTC') FROM (
  SELECT started_at FROM invocations UNION ALL SELECT started_at FROM superseded_calls
  UNION ALL SELECT started_at FROM model_gateway_attempts
 ) existing;

CREATE VIEW monitor_call_facts AS
 SELECT c.started_at,c.ended_at,coalesce(s.team_id,r.charge_team) AS team_id,c.run_id,n.agent_id,
  CASE WHEN ar.tool IS NOT NULL THEN 'tool' WHEN ar.card IS NOT NULL THEN 'external' ELSE 'model' END AS source_type,
  ar.resource_id AS model_id,NULL::uuid AS provider_id,NULL::text AS tier,
  c.status='completed' AS success,c.status IN ('unknown','input_rejected','remote_closed','failed') AS failure,
  false AS fallback,r.status='completed' AS run_succeeded,
  q.quality_track,q.lightweight_effective,q.collaborative_approved,
  NULL::bigint AS prompt_tokens,NULL::bigint AS completion_tokens,NULL::bigint AS cached_tokens,c.tokens::bigint AS tokens_total,
  coalesce(s.amount_cny*1000000,0) AS cost_micro_cny,ar.card IS NOT NULL AND (s.id IS NULL OR NOT s.settled) AS undisclosed
 FROM call_attempts c JOIN runs r ON r.id=c.run_id
 JOIN effective_run_nodes n ON (n.run_id,n.ordinal)=(c.run_id,c.ordinal)
 JOIN agent_revisions ar ON (ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision)
 LEFT JOIN spend s ON s.op_key=c.op_key LEFT JOIN run_quality_outcomes q ON q.run_id=c.run_id
 UNION ALL
 SELECT a.started_at,a.finished_at,c.team_id,a.run_id,a.agent_id,'model',m.id,m.provider_id,c.tier,
  a.status='succeeded',a.status IN ('failed','rejected'),a.fallback,coalesce(r.status='completed',false),
  q.quality_track,q.lightweight_effective,q.collaborative_approved,c.prompt_tokens,c.completion_tokens,c.cached_tokens,
  c.prompt_tokens+c.completion_tokens,coalesce(c.cost_micro_cny,0),false
 FROM model_gateway_attempts a JOIN model_gateway_charges c USING(operation_key)
 JOIN model_catalog m ON m.id=a.model_id LEFT JOIN runs r ON r.id=a.run_id
 LEFT JOIN run_quality_outcomes q ON q.run_id=a.run_id;

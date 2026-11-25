CREATE TABLE monitor_member_outcome_hourly (
 bucket_hour timestamptz NOT NULL,
 actor uuid NOT NULL,
 team_id uuid NOT NULL,
 kind text NOT NULL,
 state text NOT NULL,
 n bigint NOT NULL,
 PRIMARY KEY(bucket_hour,actor,team_id,kind,state)
);

CREATE TABLE monitor_member_cost_hourly (
 bucket_hour timestamptz NOT NULL,
 actor uuid NOT NULL,
 team_id uuid,
 attempts bigint NOT NULL,
 tokens bigint NOT NULL,
 unknown bigint NOT NULL,
 pending bigint NOT NULL,
 reserved numeric NOT NULL,
 amount numeric NOT NULL,
 UNIQUE NULLS NOT DISTINCT(bucket_hour,actor,team_id)
);

CREATE TABLE monitor_member_quality_hourly (
 bucket_hour timestamptz NOT NULL,
 actor uuid NOT NULL,
 team_id uuid NOT NULL,
 quality_track text NOT NULL,
 total bigint NOT NULL,
 met bigint NOT NULL,
 PRIMARY KEY(bucket_hour,actor,team_id,quality_track)
);

CREATE TABLE monitor_member_rollup_done (bucket_hour timestamptz PRIMARY KEY);
CREATE INDEX monitor_rollup_dirty_bucket ON monitor_rollup_dirty(bucket_hour);
CREATE INDEX monitor_reviews_time ON reviews(created_at);
CREATE INDEX monitor_deliveries_time ON deliveries(created_at);
CREATE INDEX monitor_adoptions_time ON answer_adoptions(at);
CREATE INDEX monitor_validation_end_time ON validations(ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX monitor_probe_start_time ON model_probes(created_at);
CREATE INDEX monitor_planning_start_time ON planning_requests(created_at);
CREATE INDEX monitor_validation_start_time ON validations(created_at);
CREATE INDEX monitor_column_start_time ON column_suggestions(created_at);

CREATE FUNCTION monitor_usage_raw_ranges(from_at timestamptz,to_at timestamptz)
RETURNS TABLE(lo timestamptz,hi timestamptz) LANGUAGE sql STABLE AS $$
 WITH bounds AS (
  SELECT date_trunc('hour',from_at,'UTC') + CASE WHEN from_at=date_trunc('hour',from_at,'UTC') THEN interval '0' ELSE interval '1 hour' END AS first,
         date_trunc('hour',to_at,'UTC') AS last
 ), windows AS (
  SELECT from_at lo,least(to_at,first) hi FROM bounds
  UNION ALL SELECT greatest(first,last),to_at FROM bounds
  UNION ALL SELECT DISTINCT d.bucket_hour,d.bucket_hour+interval '1 hour'
   FROM bounds b JOIN monitor_rollup_dirty d ON d.bucket_hour>=b.first AND d.bucket_hour<b.last
   LEFT JOIN monitor_member_rollup_done done USING(bucket_hour) WHERE done.bucket_hour IS NULL
 ) SELECT lo,hi FROM windows WHERE lo<hi
$$;

CREATE FUNCTION monitor_member_event_hour() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 moment timestamptz;
BEGIN
 IF TG_OP <> 'DELETE' THEN
  IF TG_NARGS>0 THEN moment := NEW.at; ELSE moment := NEW.created_at; END IF;
  IF moment IS NOT NULL THEN
   INSERT INTO monitor_rollup_dirty(bucket_hour) VALUES(date_trunc('hour',moment,'UTC'));
  END IF;
 END IF;
 IF TG_OP <> 'INSERT' THEN
  IF TG_NARGS>0 THEN moment := OLD.at; ELSE moment := OLD.created_at; END IF;
  IF moment IS NOT NULL THEN
   INSERT INTO monitor_rollup_dirty(bucket_hour) VALUES(date_trunc('hour',moment,'UTC'));
  END IF;
 END IF;
 RETURN NULL;
END $$;

CREATE TRIGGER monitor_member_reviews AFTER INSERT OR DELETE ON reviews
 FOR EACH ROW EXECUTE FUNCTION monitor_member_event_hour();
CREATE TRIGGER monitor_member_adoptions AFTER INSERT OR DELETE ON answer_adoptions
 FOR EACH ROW EXECUTE FUNCTION monitor_member_event_hour('at');
CREATE TRIGGER monitor_member_deliveries AFTER INSERT OR DELETE ON deliveries
 FOR EACH ROW EXECUTE FUNCTION monitor_member_event_hour();
CREATE TRIGGER monitor_member_connection AFTER INSERT OR DELETE OR UPDATE OF tokens ON model_probes
 FOR EACH ROW EXECUTE FUNCTION monitor_member_event_hour();
CREATE TRIGGER monitor_member_planning AFTER INSERT OR DELETE OR UPDATE OF tokens ON planning_requests
 FOR EACH ROW EXECUTE FUNCTION monitor_member_event_hour();
CREATE TRIGGER monitor_member_column_suggestion AFTER INSERT OR DELETE OR UPDATE OF tokens ON column_suggestions
 FOR EACH ROW EXECUTE FUNCTION monitor_member_event_hour();
CREATE TRIGGER monitor_member_validation_cost AFTER INSERT OR DELETE OR UPDATE OF tokens ON validations
 FOR EACH ROW EXECUTE FUNCTION monitor_member_event_hour();

CREATE FUNCTION monitor_member_validation_outcome() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP <> 'DELETE' AND NEW.ended_at IS NOT NULL THEN
  INSERT INTO monitor_rollup_dirty(bucket_hour) VALUES(date_trunc('hour',NEW.ended_at,'UTC'));
 END IF;
 IF TG_OP <> 'INSERT' AND OLD.ended_at IS NOT NULL THEN
  INSERT INTO monitor_rollup_dirty(bucket_hour) VALUES(date_trunc('hour',OLD.ended_at,'UTC'));
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER monitor_member_validation_result AFTER INSERT OR DELETE OR UPDATE OF status,ended_at,check_id ON validations
 FOR EACH ROW EXECUTE FUNCTION monitor_member_validation_outcome();

CREATE FUNCTION monitor_member_nonrun_charge() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 entry spend;
 key uuid;
 moment timestamptz;
BEGIN
 entry := CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
 IF entry.run_id IS NOT NULL OR split_part(entry.op_key,'/',2) !~* '^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$' THEN
  RETURN NULL;
 END IF;
 key := split_part(entry.op_key,'/',2)::uuid;
 CASE split_part(entry.op_key,'/',1)
  WHEN 'connection' THEN SELECT created_at INTO moment FROM model_probes WHERE id=key;
  WHEN 'planning' THEN SELECT created_at INTO moment FROM planning_requests WHERE id=key;
  WHEN 'validation' THEN SELECT created_at INTO moment FROM validations WHERE id=key;
  WHEN 'column-suggestion' THEN SELECT created_at INTO moment FROM column_suggestions WHERE id=key;
  ELSE RETURN NULL;
 END CASE;
 IF moment IS NOT NULL THEN
  INSERT INTO monitor_rollup_dirty(bucket_hour) VALUES(date_trunc('hour',moment,'UTC'));
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER monitor_member_charge AFTER INSERT OR DELETE OR UPDATE OF amount_cny,settled ON spend
 FOR EACH ROW EXECUTE FUNCTION monitor_member_nonrun_charge();

INSERT INTO monitor_rollup_dirty(bucket_hour)
 SELECT DISTINCT date_trunc('hour',at,'UTC') FROM (
  SELECT at FROM management_outcome_events UNION ALL SELECT at FROM management_call_costs
 ) existing WHERE at IS NOT NULL;

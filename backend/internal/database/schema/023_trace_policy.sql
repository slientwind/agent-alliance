ALTER TABLE teams ADD COLUMN trace_days integer NOT NULL DEFAULT 30 CHECK(trace_days BETWEEN 1 AND 365),
 ADD COLUMN trace_manager uuid REFERENCES members, ADD COLUMN trace_policy_revision integer NOT NULL DEFAULT 1;
ALTER TABLE runs ADD COLUMN trace_expires_at timestamptz;
UPDATE runs SET trace_expires_at=finished_at+interval '30 days' WHERE finished_at IS NOT NULL;
CREATE INDEX runs_trace_expiry ON runs(trace_expires_at) WHERE trace_expires_at IS NOT NULL;

CREATE OR REPLACE FUNCTION trace_run_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' OR OLD.status IS DISTINCT FROM NEW.status THEN
   IF NEW.status IN ('completed','cancelled') AND NEW.finished_at IS NULL THEN
     NEW.finished_at=clock_timestamp();
     SELECT NEW.finished_at+t.trace_days*interval '1 day' INTO NEW.trace_expires_at
       FROM projects p JOIN teams t ON t.id=p.team_id WHERE p.id=NEW.project_id;
   END IF;
 END IF;
 RETURN NEW;
END $$;

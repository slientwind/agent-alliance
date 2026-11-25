CREATE TABLE external_agent_receipts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 request_key text NOT NULL UNIQUE,
 run_id uuid REFERENCES runs ON DELETE CASCADE,
 trial_id uuid REFERENCES validations ON DELETE CASCADE,
 turn integer CHECK(turn>0),
 attempt integer CHECK(attempt>0),
 operation text NOT NULL CHECK(operation IN ('submit','poll','cancel','supplement')),
 cost_disclosure text NOT NULL CHECK(cost_disclosure IN ('reported','undisclosed')),
 usage jsonb,
 trace_url text,
 received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((run_id IS NULL)<>(trial_id IS NULL)),
 CHECK((run_id IS NULL)=(turn IS NULL) AND (run_id IS NULL)=(attempt IS NULL)),
 CHECK((cost_disclosure='reported')=(usage IS NOT NULL))
);
CREATE INDEX external_agent_receipts_run ON external_agent_receipts(run_id,turn,attempt,received_at);
CREATE INDEX external_agent_receipts_trial ON external_agent_receipts(trial_id,received_at);

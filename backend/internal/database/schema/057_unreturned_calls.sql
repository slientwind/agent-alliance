-- Older entries have no recorded transport deadline. Use the historical
-- activity timeout and an additional reconciliation window for those entries.
ALTER TABLE invocations
 ADD COLUMN execution_seconds integer NOT NULL DEFAULT 120 CHECK (execution_seconds>0),
 ADD COLUMN receipt_seconds integer NOT NULL DEFAULT 900 CHECK (receipt_seconds>0);
CREATE INDEX pending_invocation_start ON invocations(started_at) WHERE status='submitted';
CREATE TABLE receipt_wakes(run_id uuid PRIMARY KEY REFERENCES runs);
CREATE TABLE late_call_receipts(
 run_id uuid NOT NULL,ordinal integer NOT NULL,attempt_no integer NOT NULL,received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 outcome text NOT NULL CHECK(outcome IN ('completed','remote','unknown')),tokens integer,
 body_sha256 text,file_sha256 text,remote_task text,remote_context text,remote_state text,
 PRIMARY KEY(run_id,ordinal,attempt_no),FOREIGN KEY(run_id,ordinal) REFERENCES invocations
);
ALTER TABLE call_reviews ADD COLUMN submission_timed_out boolean NOT NULL DEFAULT false,
 ADD COLUMN execution_due timestamptz, ADD COLUMN receipt_due timestamptz;
CREATE OR REPLACE VIEW call_attempts AS
 SELECT i.run_id,i.ordinal,i.attempt_no,i.started_at,i.ended_at,i.status,coalesce(i.tokens,l.tokens) tokens,call_operation(i.run_id,i.ordinal,i.attempt_no) op_key
 FROM invocations i LEFT JOIN late_call_receipts l USING(run_id,ordinal,attempt_no)
 UNION ALL SELECT s.run_id,s.ordinal,s.attempt_no,s.started_at,s.ended_at,'unknown',l.tokens,call_operation(s.run_id,s.ordinal,s.attempt_no)
 FROM superseded_calls s LEFT JOIN late_call_receipts l USING(run_id,ordinal,attempt_no);
CREATE VIEW submission_windows AS
 SELECT run_id,ordinal,started_at+make_interval(secs=>execution_seconds) execution_due,
 started_at+make_interval(secs=>execution_seconds+receipt_seconds) receipt_due
 FROM invocations WHERE status='submitted' AND remote_task IS NULL;

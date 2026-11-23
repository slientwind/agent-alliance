CREATE TABLE remote_jobs(
 run_id uuid, ordinal integer, phase text NOT NULL CHECK(phase IN ('checking','manual','finished')),
 check_window_seconds integer NOT NULL DEFAULT 900, deadline timestamptz NOT NULL, next_at timestamptz NOT NULL DEFAULT now(),
 checks integer NOT NULL DEFAULT 0, consecutive_errors integer NOT NULL DEFAULT 0,
 cancel_attempted boolean NOT NULL DEFAULT false, wake boolean NOT NULL DEFAULT false,
 lease uuid, lease_until timestamptz, detail text NOT NULL DEFAULT 'remote_pending',
 PRIMARY KEY(run_id,ordinal), FOREIGN KEY(run_id,ordinal) REFERENCES invocations
);
CREATE INDEX remote_due ON remote_jobs(next_at) WHERE phase='checking';
CREATE INDEX remote_wake ON remote_jobs(run_id) WHERE wake;
INSERT INTO remote_jobs(run_id,ordinal,phase,deadline,detail)
 SELECT run_id,ordinal,'manual',now(),'remote_legacy' FROM invocations WHERE status='remote' AND remote_task IS NOT NULL;

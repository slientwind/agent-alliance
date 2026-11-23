ALTER TABLE invocations ADD COLUMN attempt_no integer NOT NULL DEFAULT 1 CHECK(attempt_no>0);
CREATE TABLE call_reviews(
 control_id uuid PRIMARY KEY REFERENCES controls, run_id uuid NOT NULL, ordinal integer NOT NULL, attempt_no integer NOT NULL,
 decision text NOT NULL CHECK(decision IN ('hold','redo')), FOREIGN KEY(run_id,ordinal) REFERENCES invocations
);
CREATE UNIQUE INDEX single_redo_per_attempt ON call_reviews(run_id,ordinal,attempt_no) WHERE decision='redo';
CREATE INDEX call_review_step ON call_reviews(run_id,ordinal);
-- Only superseded, unresolved attempts enter this history. A new reservation
-- and the snapshot commit together, so a budget block never invents an attempt.
CREATE TABLE superseded_calls(
 run_id uuid NOT NULL,ordinal integer NOT NULL,attempt_no integer NOT NULL,
 started_at timestamptz NOT NULL,ended_at timestamptz NOT NULL,key_generation integer,
 review_control uuid NOT NULL UNIQUE REFERENCES call_reviews,
 PRIMARY KEY(run_id,ordinal,attempt_no),FOREIGN KEY(run_id,ordinal) REFERENCES invocations
);
CREATE FUNCTION call_operation(uuid,integer,integer) RETURNS text LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT $1::text||'-'||$2::text||CASE WHEN $3=1 THEN '' ELSE '/attempt/'||$3::text END
$$;
CREATE VIEW call_attempts AS
 SELECT run_id,ordinal,attempt_no,started_at,ended_at,status,tokens,call_operation(run_id,ordinal,attempt_no) op_key FROM invocations
 UNION ALL SELECT run_id,ordinal,attempt_no,started_at,ended_at,'unknown',NULL::integer,call_operation(run_id,ordinal,attempt_no) FROM superseded_calls;

CREATE VIEW call_review_authority AS
 SELECT r.id run_id,m.id member_id FROM runs r JOIN projects p ON p.id=r.project_id JOIN project_members pm ON pm.project_id=p.id
 JOIN members m ON m.id=pm.member_id AND m.enabled
 WHERE m.id=p.owner OR (pm.can_review AND m.id=r.actor AND EXISTS(SELECT FROM change_batches cb WHERE cb.run_id=r.id));

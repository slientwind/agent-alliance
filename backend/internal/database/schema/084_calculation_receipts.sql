ALTER TABLE late_call_receipts DROP CONSTRAINT late_call_receipts_outcome_check,
 ADD CHECK(outcome IN ('completed','remote','unknown','input_rejected'));

-- A negative receipt belongs to a single operation. Close the run only on
-- an explicit control decision after every dispatched operation is accounted for.
CREATE OR REPLACE FUNCTION run_failure_confirmable(target uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT FROM runs WHERE id=target AND status IN ('running','blocked','reconcile','paused'))
       AND (EXISTS (SELECT FROM invocations WHERE run_id=target AND status='input_rejected')
            OR EXISTS (SELECT FROM invocations i JOIN remote_jobs j USING(run_id,ordinal)
            WHERE i.run_id=target AND i.status='remote_closed' AND i.remote_status IN ('failed','rejected')
              AND j.phase='finished' AND j.detail IN ('remote_failed','remote_rejected')))
       AND NOT EXISTS (SELECT FROM invocations WHERE run_id=target AND status NOT IN ('completed','remote_closed','input_rejected'))
       AND NOT EXISTS (SELECT FROM remote_jobs WHERE run_id=target AND phase<>'finished')
       AND NOT EXISTS (
            SELECT FROM superseded_calls s WHERE s.run_id=target AND NOT EXISTS (
                SELECT FROM late_call_receipts l
                WHERE (l.run_id,l.ordinal,l.attempt_no)=(s.run_id,s.ordinal,s.attempt_no)
                  AND (l.outcome IN ('completed','input_rejected') OR (l.outcome='remote'
                       AND l.remote_state IN ('completed','failed','canceled','rejected')))
            )
       )
$$;

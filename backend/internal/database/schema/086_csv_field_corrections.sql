-- An accepted correction belongs to one attempted operation and one pinned file.
ALTER TABLE invocations ADD COLUMN input_issue jsonb;
ALTER TABLE call_reviews ADD COLUMN input_mapping jsonb CHECK(input_mapping IS NULL OR decision='redo');
CREATE OR REPLACE VIEW call_attempts AS
 SELECT i.run_id,i.ordinal,i.attempt_no,i.started_at,i.ended_at,i.status,coalesce(i.tokens,l.tokens) tokens,call_operation(i.run_id,i.ordinal,i.attempt_no) op_key
 FROM invocations i LEFT JOIN late_call_receipts l USING(run_id,ordinal,attempt_no)
 UNION ALL SELECT s.run_id,s.ordinal,s.attempt_no,s.started_at,s.ended_at,CASE WHEN v.input_mapping IS NULL THEN 'unknown' ELSE 'input_rejected' END,l.tokens,call_operation(s.run_id,s.ordinal,s.attempt_no)
 FROM superseded_calls s JOIN call_reviews v ON v.control_id=s.review_control LEFT JOIN late_call_receipts l ON (l.run_id,l.ordinal,l.attempt_no)=(s.run_id,s.ordinal,s.attempt_no);

CREATE OR REPLACE FUNCTION run_failure_confirmable(target uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT FROM runs WHERE id=target AND status IN ('running','blocked','reconcile','paused'))
       AND (EXISTS (SELECT FROM invocations WHERE run_id=target AND status='input_rejected')
            OR EXISTS (SELECT FROM invocations i JOIN remote_jobs j USING(run_id,ordinal)
            WHERE i.run_id=target AND i.status='remote_closed' AND i.remote_status IN ('failed','rejected')
              AND j.phase='finished' AND j.detail IN ('remote_failed','remote_rejected')))
       AND NOT EXISTS (SELECT FROM invocations WHERE run_id=target AND status NOT IN ('completed','remote_closed','input_rejected'))
       AND NOT EXISTS (SELECT FROM remote_jobs WHERE run_id=target AND phase<>'finished')
       AND NOT EXISTS (
            SELECT FROM superseded_calls s JOIN call_reviews v ON v.control_id=s.review_control WHERE s.run_id=target AND v.input_mapping IS NULL AND NOT EXISTS (
                SELECT FROM late_call_receipts l
                WHERE (l.run_id,l.ordinal,l.attempt_no)=(s.run_id,s.ordinal,s.attempt_no)
                  AND (l.outcome IN ('completed','input_rejected') OR (l.outcome='remote'
                       AND l.remote_state IN ('completed','failed','canceled','rejected')))
            )
       )
$$;

-- Keep the decision, attempt and version for audit. Remove copied headers when
-- the enterprise erases the underlying source from retained storage.
CREATE INDEX correction_issue_source ON invocations ((input_issue->>'source')) WHERE input_issue IS NOT NULL;
CREATE INDEX correction_mapping_source ON call_reviews ((input_mapping->>'source')) WHERE input_mapping IS NOT NULL;
CREATE FUNCTION purge_correction_headers() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE invocations SET input_issue=NULL WHERE input_issue->>'source'=NEW.id::text;
 UPDATE call_reviews SET input_mapping=input_mapping-'columns' WHERE input_mapping->>'source'=NEW.id::text;
 RETURN NULL;
END $$;
CREATE TRIGGER purge_csv_header_copies AFTER UPDATE OF purged_at ON sources
 FOR EACH ROW WHEN (OLD.purged_at IS NULL AND NEW.purged_at IS NOT NULL)
 EXECUTE FUNCTION purge_correction_headers();

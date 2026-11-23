-- A field approval may be replaced only while its rejected attempt is still
-- current. Keep the earlier decision and point it to the new approval.
ALTER TABLE call_reviews ADD COLUMN superseded_by uuid
 REFERENCES call_reviews(control_id) DEFERRABLE INITIALLY DEFERRED
 CHECK(superseded_by IS NULL OR (input_mapping IS NOT NULL AND superseded_by<>control_id));
DROP INDEX single_redo_per_attempt;
CREATE UNIQUE INDEX single_redo_per_attempt ON call_reviews(run_id,ordinal,attempt_no)
 WHERE decision='redo' AND superseded_by IS NULL;

CREATE VIEW active_call_redos AS
 SELECT v.*, CASE WHEN v.input_mapping IS NOT NULL
   THEN EXISTS(SELECT FROM column_correction_authority a WHERE (a.run_id,a.member_id)=(v.run_id,c.actor))
   ELSE EXISTS(SELECT FROM call_review_authority a WHERE (a.run_id,a.member_id)=(v.run_id,c.actor))
   END AS authority_current
 FROM call_reviews v JOIN controls c ON c.id=v.control_id
 WHERE v.decision='redo' AND v.superseded_by IS NULL;

CREATE VIEW pending_column_reconfirmations AS
 SELECT v.run_id,v.ordinal,v.attempt_no,v.control_id,v.input_mapping
 FROM active_call_redos v
 JOIN invocations i ON (i.run_id,i.ordinal,i.attempt_no)=(v.run_id,v.ordinal,v.attempt_no)
 JOIN runs r ON r.id=v.run_id
 WHERE v.input_mapping IS NOT NULL AND NOT v.authority_current
 AND i.status='input_rejected' AND i.input_issue IS NOT NULL
 AND r.status IN ('running','reconcile','blocked','paused');

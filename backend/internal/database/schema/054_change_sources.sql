ALTER TABLE change_batches ADD COLUMN source_assessment uuid REFERENCES source_assessments;
-- An assessment fixes the exact source, observed revision and feedback to which consent applies.
CREATE VIEW run_source_inputs AS
 SELECT r.id run_id, s.id source_id, ps.source_version plan_pin, v.version,
        coalesce(v.filename,'历史资料') filename,
        CASE WHEN s.revoked_at IS NULL AND (c.id IS NULL OR s.current_version=c.observed_version) THEN v.extracted END extracted
 FROM runs r JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN sources s ON (s.id,s.project_id)=(ps.source_id,r.project_id)
 LEFT JOIN change_batches b ON b.run_id=r.id
 LEFT JOIN source_assessments c ON (c.id,c.source_id)=(b.source_assessment,s.id)
 JOIN source_versions v ON (v.source_id,v.version)=(s.id,coalesce(c.observed_version,ps.source_version));

CREATE VIEW change_source_receipts AS
 SELECT b.run_id,b.artifact_id,b.applied_version,c.source_id,
 jsonb_build_object('run',r.id,'artifact',b.artifact_id,'version',b.applied_version,'source',c.source_id,
 'name',v.filename,'planPin',c.pinned_version,'selected',c.observed_version,'authorizedBy',r.actor,
 'assessment',c.id,'feedback',b.feedback_id) receipt
 FROM change_batches b JOIN source_assessments c ON c.id=b.source_assessment JOIN runs r ON r.id=b.run_id
 JOIN source_versions v ON (v.source_id,v.version)=(c.source_id,c.observed_version);
ALTER TABLE delivery_source_evidence ADD COLUMN revision_inputs jsonb NOT NULL DEFAULT '[]';

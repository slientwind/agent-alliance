ALTER TABLE delivery_item_assessments ADD COLUMN file_evidence jsonb,
  ADD CHECK (file_evidence IS NULL OR (artifact_id IS NOT NULL AND jsonb_typeof(file_evidence)='object'));

-- The review view adds file text only for authorized desktop reviewers.
-- Calls without a viewer still produce the original sealed coverage shape.
CREATE OR REPLACE FUNCTION project_delivery_coverage(target uuid, version integer, viewer uuid DEFAULT NULL) RETURNS jsonb
LANGUAGE sql STABLE AS $$
  SELECT jsonb_build_object('planVersion',c.plan_version,'allowPartial',c.allow_partial,
    'items',ARRAY(
      SELECT jsonb_strip_nulls(jsonb_build_object('item',q.item,'step',q.ordinal,'label',q.label,
        'run',run.id,'artifact',a.id,'version',a.current_version,'state',coalesce(v.state,'unchecked'),
        'reason',case when access.allowed then v.reason end,'responsible',v.responsible,'responsibleName',owner.display_name,
        'responsibleActive',owner.id IS NOT NULL AND EXISTS(
           SELECT FROM project_members pm WHERE (pm.project_id,pm.member_id)=(q.project_id,v.responsible)
             AND pm.recovery_assignment IS NULL),
        'continueWhen',case when access.allowed then v.continue_when end,'reviewer',v.reviewer,'reviewedAt',v.reviewed_at,
        'fileEvidenceAvailable',case when reviewer.allowed and access.allowed and
          f.filename ~* '\.(docx|xlsx|pptx|pdf)$' then true end,
        'fileEvidence',case when reviewer.allowed and access.allowed and
          (v.file_evidence->>'hash',v.file_evidence->>'name')=(f.object_hash,f.filename)
          then v.file_evidence-'hash' end))
      FROM plan_delivery_requirements q
      LEFT JOIN LATERAL (
        SELECT r.id FROM runs r WHERE (r.project_id,r.plan_version)=(q.project_id,q.plan_version)
          AND NOT EXISTS(SELECT FROM change_batches b WHERE b.run_id=r.id)
        ORDER BY r.created_at DESC,r.id DESC LIMIT 1
      ) run ON true
      LEFT JOIN run_candidate_artifacts a ON (a.run_id,a.ordinal)=(run.id,q.ordinal)
      LEFT JOIN artifact_files f ON (f.artifact_id,f.version)=(a.id,a.current_version)
      CROSS JOIN LATERAL (SELECT viewer IS NULL OR a.id IS NULL OR distribution_readable(a.id,a.current_version,viewer) AS allowed) access
      LEFT JOIN LATERAL (
        SELECT v.* FROM delivery_item_assessments v
        WHERE (v.project_id,v.plan_version,v.item)=(q.project_id,q.plan_version,q.item)
        ORDER BY v.id DESC LIMIT 1
      ) v ON ((v.run_id=run.id OR EXISTS (
            SELECT FROM rule_branch_steps b JOIN branch_rebuild_ancestors history ON history.run_id=b.run_id
            WHERE (b.run_id,b.ordinal,b.mode,b.artifact_id,b.result_version)=(run.id,q.ordinal,'reuse',a.id,a.current_version)
            AND history.source_run=v.run_id))
        AND (v.artifact_id,v.artifact_version) IS NOT DISTINCT FROM (a.id,a.current_version)
        AND EXISTS(SELECT FROM project_members pm JOIN members rm ON rm.id=pm.member_id AND rm.enabled
          WHERE (pm.project_id,pm.member_id)=(q.project_id,v.reviewer) AND pm.can_review))
      LEFT JOIN members owner ON owner.id=v.responsible AND owner.enabled AND owner.kind='person'
      WHERE (q.project_id,q.plan_version)=(c.project_id,c.plan_version)
      ORDER BY q.item))
  FROM plan_delivery_contracts c
  CROSS JOIN LATERAL (SELECT viewer IS NOT NULL AND EXISTS(
    SELECT FROM project_members pm JOIN members u ON u.id=pm.member_id AND u.enabled AND u.departed_at IS NULL
    WHERE (pm.project_id,pm.member_id)=(c.project_id,viewer) AND pm.can_review) AS allowed) reviewer
  WHERE (c.project_id,c.plan_version)=(target,version)
$$;

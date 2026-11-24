CREATE TABLE artifact_upstream_context (
  artifact_id uuid NOT NULL,
  version integer NOT NULL,
  run_id uuid NOT NULL,
  ordinal integer NOT NULL,
  attempt_no integer NOT NULL,
  body_digest text NOT NULL CHECK (body_digest ~ '^[0-9a-f]{64}$'),
  PRIMARY KEY(artifact_id,version),
  FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions ON DELETE CASCADE,
  FOREIGN KEY(run_id,ordinal,attempt_no) REFERENCES run_working_sets ON DELETE CASCADE
);
CREATE INDEX artifact_upstream_context_by_call ON artifact_upstream_context(run_id,ordinal,attempt_no);

ALTER TABLE delivery_item_assessments ADD COLUMN upstream_evidence_step integer
  CHECK (upstream_evidence_step IS NULL OR (upstream_evidence_step>0 AND artifact_id IS NOT NULL));

CREATE FUNCTION bind_artifact_upstream_context(target uuid, edition integer, origin uuid, step_no integer)
RETURNS void LANGUAGE sql AS $$
 INSERT INTO artifact_upstream_context(artifact_id,version,run_id,ordinal,attempt_no,body_digest)
 SELECT v.artifact_id,v.version,i.run_id,i.ordinal,i.attempt_no,encode(digest(convert_to(v.body,'UTF8'),'sha256'),'hex')
 FROM invocations i JOIN run_working_sets w USING(run_id,ordinal,attempt_no)
 JOIN runs r ON r.id=i.run_id
 JOIN artifacts a ON (a.id,a.project_id,a.ordinal)=(target,r.project_id,i.ordinal)
 JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,edition)
 WHERE (i.run_id,i.ordinal)=(origin,step_no) AND i.status='completed' AND v.body=i.result
   AND jsonb_array_length(w.upstream_context)>0
   AND NOT EXISTS(SELECT FROM jsonb_array_elements(w.upstream_context) input WHERE input->>'digest' IS NULL)
 ON CONFLICT (artifact_id,version) DO NOTHING
$$;

-- A copied candidate has no new execution. Carry the original provenance via
-- the explicit copied_from chain, which survives approval of an edition.
CREATE FUNCTION artifact_upstream_origin(target uuid, edition integer)
RETURNS SETOF artifact_upstream_context LANGUAGE sql STABLE ROWS 1 AS $$
 WITH RECURSIVE lineage(version) AS (
   SELECT edition
   UNION ALL SELECT v.copied_from FROM lineage l JOIN artifact_versions v
     ON (v.artifact_id,v.version)=(target,l.version) WHERE v.copied_from IS NOT NULL
 )
 SELECT c.* FROM lineage l JOIN artifact_upstream_context c
   ON (c.artifact_id,c.version)=(target,l.version) ORDER BY c.version DESC LIMIT 1
$$;

CREATE FUNCTION artifact_upstream_review_entries(target_project uuid, target uuid, edition integer, viewer uuid, source_step integer)
RETURNS TABLE(origin_version integer, step integer, attempt integer, name text, source_version integer,
              current_version integer, total_bytes integer, head_bytes integer, tail_from integer,
              available boolean, body text)
LANGUAGE sql STABLE ROWS 12 SET jit TO off AS $$
 WITH work AS MATERIALIZED (
   SELECT c.version,w.upstream_context,run_working_set_usable(c.run_id,c.ordinal,c.attempt_no)
     AND encode(digest(convert_to(v.body,'UTF8'),'sha256'),'hex')=c.body_digest AS usable
   FROM artifacts a JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,edition)
   JOIN project_members pm ON (pm.project_id,pm.member_id)=(a.project_id,viewer) AND pm.can_review
   JOIN members m ON m.id=pm.member_id AND m.enabled AND m.departed_at IS NULL
   JOIN LATERAL artifact_upstream_origin(a.id,v.version) c ON true
   JOIN run_working_sets w ON (w.run_id,w.ordinal,w.attempt_no)=(c.run_id,c.ordinal,c.attempt_no)
   WHERE (a.project_id,a.id)=(target_project,target) AND v.version=a.current_version
     AND distribution_readable(a.id,v.version,viewer)
 )
 SELECT work.version,clip.step,
   CASE WHEN valid.allowed THEN clip."originAttempt" END,
   CASE WHEN valid.allowed THEN a.name END,
   CASE WHEN valid.allowed THEN v.version END,
   CASE WHEN valid.allowed THEN a.current_version END,
   CASE WHEN valid.allowed THEN clip."totalBytes" END,
   CASE WHEN valid.allowed THEN clip."headBytes" END,
   CASE WHEN valid.allowed THEN clip."tailFrom" END,
   valid.allowed, CASE WHEN source_step>0 AND valid.allowed THEN original.body END
 FROM work CROSS JOIN LATERAL jsonb_to_recordset(work.upstream_context) AS clip(
   step int,"originRun" uuid,"originAttempt" int,artifact uuid,version int,
   "totalBytes" int,"headBytes" int,"tailFrom" int,digest text)
 LEFT JOIN artifacts a ON (a.id,a.project_id)=(clip.artifact,target_project)
 LEFT JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,clip.version)
 LEFT JOIN runs r ON (r.id,r.project_id)=(clip."originRun",target_project)
 LEFT JOIN invocations i ON (i.run_id,i.ordinal,i.attempt_no)=(r.id,clip.step,clip."originAttempt") AND i.status='completed'
 CROSS JOIN LATERAL (SELECT CASE WHEN clip.artifact IS NULL THEN i.result ELSE v.body END AS body) original
 CROSS JOIN LATERAL (SELECT coalesce(work.usable AND original.body IS NOT NULL
   AND (clip.artifact IS NULL OR distribution_readable(a.id,v.version,viewer))
   AND (clip."originRun" IS NULL OR (r.id IS NOT NULL AND run_working_set_usable(r.id,clip.step,clip."originAttempt")))
   AND octet_length(original.body)=clip."totalBytes"
   AND encode(digest(convert_to(original.body,'UTF8'),'sha256'),'hex')=clip.digest
   AND 0<=clip."headBytes" AND clip."headBytes"<=clip."tailFrom" AND clip."tailFrom"<=clip."totalBytes",
   false) AS allowed) valid
 WHERE source_step=0 OR clip.step=source_step
 ORDER BY clip.step
$$;

CREATE FUNCTION project_upstream_review_scope(target uuid, viewer uuid)
RETURNS text LANGUAGE sql STABLE SET jit TO off AS $$
 SELECT string_agg(a.id::text||'/'||a.current_version||'/'||
   run_working_set_usable(c.run_id,c.ordinal,c.attempt_no)::text,',' ORDER BY a.id)
 FROM project_members pm JOIN artifacts a ON a.project_id=pm.project_id
 JOIN LATERAL artifact_upstream_origin(a.id,a.current_version) c ON true
 WHERE (pm.project_id,pm.member_id)=(target,viewer) AND pm.can_review
   AND distribution_readable(a.id,a.current_version,viewer)
$$;

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
          then v.file_evidence-'hash' end,
        'remoteEvidence',remote.receipt,
        'remoteEvidenceRecorded',case when remote.receipt is not null and v.remote_receipt_version=remote.version then true end,
        'upstreamEvidenceReady',case when reviewer.allowed and access.allowed and upstream.run_id is not null
          then run_working_set_usable(upstream.run_id,upstream.ordinal,upstream.attempt_no) end,
        'upstreamEvidenceStep',case when reviewer.allowed and access.allowed and upstream.run_id is not null
          and run_working_set_usable(upstream.run_id,upstream.ordinal,upstream.attempt_no)
          then v.upstream_evidence_step end))
      FROM plan_delivery_requirements q
      LEFT JOIN LATERAL (
        SELECT r.id FROM runs r WHERE (r.project_id,r.plan_version)=(q.project_id,q.plan_version)
          AND NOT EXISTS(SELECT FROM change_batches b WHERE b.run_id=r.id)
        ORDER BY r.created_at DESC,r.id DESC LIMIT 1
      ) run ON true
      LEFT JOIN run_candidate_artifacts a ON (a.run_id,a.ordinal)=(run.id,q.ordinal)
      LEFT JOIN visible_artifact_remote_receipts(c.project_id,viewer) remote
        ON (remote.artifact_id,remote.version)=(a.id,a.current_version)
      LEFT JOIN LATERAL artifact_upstream_origin(a.id,a.current_version) upstream ON reviewer.allowed
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

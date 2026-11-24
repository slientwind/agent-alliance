CREATE TABLE remote_completion_evidence (
  run_id uuid NOT NULL,
  ordinal integer NOT NULL,
  attempt_no integer NOT NULL,
  agent_id uuid NOT NULL,
  agent_revision integer NOT NULL,
  task text NOT NULL CHECK (octet_length(task) BETWEEN 1 AND 512),
  way text NOT NULL CHECK (way IN ('submit','query','supplement')),
  input_id uuid REFERENCES remote_task_inputs,
  body_digest text NOT NULL CHECK (body_digest ~ '^[0-9a-f]{64}$'),
  observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY(run_id,ordinal,attempt_no),
  FOREIGN KEY(run_id,ordinal,attempt_no) REFERENCES run_working_sets ON DELETE CASCADE,
  FOREIGN KEY(agent_id,agent_revision) REFERENCES agent_revisions,
  CHECK ((way='supplement')=(input_id IS NOT NULL))
);

CREATE TABLE artifact_remote_receipts (
  artifact_id uuid NOT NULL,
  version integer NOT NULL,
  run_id uuid NOT NULL,
  ordinal integer NOT NULL,
  attempt_no integer NOT NULL,
  PRIMARY KEY(artifact_id,version),
  FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions ON DELETE CASCADE,
  FOREIGN KEY(run_id,ordinal,attempt_no) REFERENCES remote_completion_evidence ON DELETE CASCADE
);
CREATE INDEX artifact_remote_receipts_by_call ON artifact_remote_receipts(run_id,ordinal,attempt_no);

ALTER TABLE delivery_item_assessments ADD COLUMN remote_receipt_version integer,
  ADD CHECK (remote_receipt_version IS NULL OR (artifact_id IS NOT NULL AND remote_receipt_version=artifact_version)),
  ADD FOREIGN KEY(artifact_id,remote_receipt_version) REFERENCES artifact_remote_receipts ON DELETE SET NULL (remote_receipt_version);

-- Bind only the exact current invocation and the body that became the new
-- artifact version. This also works when a different worker adopts the result.
CREATE FUNCTION bind_artifact_remote_receipt(target uuid, edition integer, origin uuid, step_no integer)
RETURNS void LANGUAGE sql AS $$
 INSERT INTO artifact_remote_receipts(artifact_id,version,run_id,ordinal,attempt_no)
 SELECT v.artifact_id,v.version,i.run_id,i.ordinal,i.attempt_no
 FROM invocations i JOIN remote_completion_evidence e USING(run_id,ordinal,attempt_no)
 JOIN runs r ON r.id=i.run_id
 JOIN artifacts a ON (a.id,a.project_id,a.ordinal)=(target,r.project_id,i.ordinal)
 JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,edition)
 WHERE (i.run_id,i.ordinal)=(origin,step_no) AND i.status='completed'
   AND i.remote_status='completed' AND i.remote_task=e.task AND v.body=i.result
   AND e.body_digest=encode(digest(v.body,'sha256'),'hex')
 ON CONFLICT (artifact_id,version) DO NOTHING
$$;

-- Keep remote task IDs on authorized review surfaces. The protocol context and
-- supplement request bodies are unnecessary for this review.
CREATE FUNCTION visible_artifact_remote_receipts(target uuid, viewer uuid)
RETURNS TABLE(artifact_id uuid, version integer, receipt jsonb)
LANGUAGE sql STABLE ROWS 32 SET jit TO off AS $$
 SELECT link.artifact_id,link.version,jsonb_build_object(
   'task',e.task,'way',e.way,'agent',ar.name,'agentRevision',e.agent_revision,
   'step',e.ordinal,'attempt',e.attempt_no,'completedAt',e.observed_at)
 FROM project_members pm JOIN members m ON m.id=pm.member_id AND m.enabled AND m.departed_at IS NULL
 JOIN artifacts a ON a.project_id=pm.project_id
 JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,a.current_version)
 JOIN artifact_remote_receipts link ON (link.artifact_id,link.version)=(v.artifact_id,v.version)
 JOIN remote_completion_evidence e ON (e.run_id,e.ordinal,e.attempt_no)=(link.run_id,link.ordinal,link.attempt_no)
 JOIN agent_revisions ar ON (ar.agent_id,ar.revision)=(e.agent_id,e.agent_revision)
 WHERE (pm.project_id,pm.member_id)=(target,viewer) AND pm.can_review
   AND distribution_readable(a.id,v.version,viewer)
   AND run_working_set_usable(e.run_id,e.ordinal,e.attempt_no)
   AND e.body_digest=encode(digest(v.body,'sha256'),'hex')
$$;

-- Both review evidence types stay out of the signed delivery coverage shape.
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
        'remoteEvidenceRecorded',case when remote.receipt is not null and v.remote_receipt_version=remote.version then true end))
      FROM plan_delivery_requirements q
      LEFT JOIN LATERAL (
        SELECT r.id FROM runs r WHERE (r.project_id,r.plan_version)=(q.project_id,q.plan_version)
          AND NOT EXISTS(SELECT FROM change_batches b WHERE b.run_id=r.id)
        ORDER BY r.created_at DESC,r.id DESC LIMIT 1
      ) run ON true
      LEFT JOIN run_candidate_artifacts a ON (a.run_id,a.ordinal)=(run.id,q.ordinal)
      LEFT JOIN visible_artifact_remote_receipts(c.project_id,viewer) remote
        ON (remote.artifact_id,remote.version)=(a.id,a.current_version)
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

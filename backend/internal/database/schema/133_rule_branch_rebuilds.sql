CREATE TABLE rule_branch_rebuilds (
    run_id uuid PRIMARY KEY REFERENCES runs,
    source_run uuid NOT NULL UNIQUE REFERENCES runs CHECK (source_run<>run_id),
    authorized_calls integer NOT NULL CHECK (authorized_calls>0),
    preview jsonb NOT NULL
);

CREATE TABLE rule_branch_steps (
    run_id uuid NOT NULL REFERENCES rule_branch_rebuilds,
    ordinal integer NOT NULL CHECK (ordinal>0),
    mode text NOT NULL CHECK (mode IN ('reuse','rebuild','continue')),
    origin_run uuid REFERENCES runs,
    origin_attempt integer,
    artifact_id uuid REFERENCES artifacts,
    base_version integer,
    result_version integer,
    finished boolean NOT NULL,
    PRIMARY KEY (run_id,ordinal),
    FOREIGN KEY (artifact_id,base_version) REFERENCES artifact_versions,
    FOREIGN KEY (artifact_id,result_version) REFERENCES artifact_versions,
    CHECK (origin_attempt IS NULL OR origin_run IS NOT NULL),
    CHECK (mode<>'reuse' OR finished)
);
CREATE UNIQUE INDEX rule_branch_artifact_editions ON rule_branch_steps(artifact_id,result_version)
    WHERE mode<>'reuse' AND result_version IS NOT NULL;

-- Reused steps point to their real attempts. They never create invocation
-- rows, tokens, or spend records in the new run.
CREATE VIEW run_observations AS
 SELECT i.run_id,i.ordinal,i.run_id AS origin_run,i.attempt_no
 FROM invocations i WHERE i.status='completed'
 UNION ALL
 SELECT b.run_id,b.ordinal,b.origin_run,b.origin_attempt FROM rule_branch_steps b
 WHERE b.mode='reuse' AND b.origin_attempt IS NOT NULL;

CREATE VIEW run_artifact_slots AS
 SELECT a.run_id,a.ordinal,a.id AS artifact_id FROM artifacts a
 WHERE NOT EXISTS(SELECT FROM rule_branch_steps b WHERE (b.run_id,b.ordinal)=(a.run_id,a.ordinal))
 UNION ALL
 SELECT b.run_id,b.ordinal,b.artifact_id FROM rule_branch_steps b WHERE b.artifact_id IS NOT NULL;

CREATE VIEW run_candidate_artifacts AS
 SELECT a.id,slot.run_id,slot.ordinal,coalesce(frozen.base_version,a.current_version) AS current_version
 FROM (
   SELECT a.id AS artifact_id,a.run_id,a.ordinal FROM artifacts a
   WHERE NOT EXISTS(SELECT FROM rule_branch_steps b WHERE (b.run_id,b.ordinal)=(a.run_id,a.ordinal))
   UNION ALL
   SELECT b.artifact_id,b.run_id,b.ordinal FROM rule_branch_steps b WHERE b.finished AND b.result_version IS NOT NULL
 ) slot JOIN artifacts a ON a.id=slot.artifact_id
 LEFT JOIN rule_branch_rebuilds next ON next.source_run=slot.run_id
 LEFT JOIN rule_branch_steps frozen ON (frozen.run_id,frozen.ordinal,frozen.artifact_id)=(next.run_id,slot.ordinal,a.id);

CREATE FUNCTION artifact_execution_run(target uuid, edition integer) RETURNS uuid
LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT b.run_id FROM rule_branch_steps b WHERE b.artifact_id=a.id
       AND b.mode<>'reuse' AND b.result_version<=edition ORDER BY b.result_version DESC LIMIT 1),a.run_id)
 FROM artifacts a WHERE a.id=target
$$;

CREATE VIEW branch_rebuild_ancestors AS
 WITH RECURSIVE parents(run_id,source_run) AS (
   SELECT run_id,source_run FROM rule_branch_rebuilds
   UNION
   SELECT p.run_id,b.source_run FROM parents p JOIN rule_branch_rebuilds b ON b.run_id=p.source_run
 ) SELECT * FROM parents;

-- A new batch inherits the existing, authorized excerpts by reference.
-- request_revision still identifies the one original receipt.
CREATE VIEW run_source_receipt_pins AS
 SELECT p.* FROM run_source_receipts p
 UNION ALL
 SELECT a.run_id,p.ordinal,p.request_revision,p.source_id,p.source_version,p.kind,p.excerpt_start,p.excerpt_end
 FROM branch_rebuild_ancestors a JOIN run_source_receipts p ON p.run_id=a.source_run;

CREATE OR REPLACE VIEW step_source_receipt_inputs AS
 SELECT p.*,p.ordinal AS reader FROM run_source_receipt_pins p
 UNION ALL
 SELECT p.*,e.ordinal AS reader FROM run_source_receipt_pins p
 JOIN runs r ON r.id=p.run_id
 JOIN node_edges e ON (e.project_id,e.plan_version,e.depends_on)=(r.project_id,r.plan_version,p.ordinal)
 JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(e.project_id,e.plan_version,e.ordinal)
 JOIN run_observations i ON (i.run_id,i.ordinal)=(p.run_id,p.ordinal)
 JOIN lookup_turns t ON (t.run_id,t.ordinal,t.attempt_no)=(i.origin_run,i.ordinal,i.attempt_no)
 WHERE n.lookup_phase='synthesize' AND t.outcome='lookup_refined' AND EXISTS (
   SELECT FROM jsonb_to_recordset(t.refinement_evidence) AS c("Source" uuid,"Version" integer)
   WHERE (c."Source",c."Version")=(p.source_id,p.source_version)
 );

CREATE OR REPLACE FUNCTION working_artifact_origin(artifact_key uuid, edition integer)
RETURNS TABLE(run_id uuid,ordinal integer,attempt_no integer) LANGUAGE sql STABLE AS $$
 SELECT i.run_id,i.ordinal,i.attempt_no
 FROM invocations i JOIN (
   SELECT a.run_id,a.ordinal FROM artifacts a WHERE a.id=artifact_key AND edition=1
   UNION
   SELECT c.run_id,a.ordinal FROM change_batches c JOIN artifacts a ON a.id=c.artifact_id
   WHERE a.id=artifact_key AND c.applied_version=edition
   UNION
   SELECT b.run_id,b.ordinal FROM rule_branch_steps b
   WHERE b.artifact_id=artifact_key AND b.result_version=edition AND b.mode<>'reuse'
 ) origin USING(run_id,ordinal)
 WHERE NOT EXISTS(SELECT FROM reviews v WHERE v.artifact_id=artifact_key AND v.version=edition)
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
        'continueWhen',case when access.allowed then v.continue_when end,'reviewer',v.reviewer,'reviewedAt',v.reviewed_at))
      FROM plan_delivery_requirements q
      LEFT JOIN LATERAL (
        SELECT r.id FROM runs r WHERE (r.project_id,r.plan_version)=(q.project_id,q.plan_version)
          AND NOT EXISTS(SELECT FROM change_batches b WHERE b.run_id=r.id)
        ORDER BY r.created_at DESC,r.id DESC LIMIT 1
      ) run ON true
      LEFT JOIN run_candidate_artifacts a ON (a.run_id,a.ordinal)=(run.id,q.ordinal)
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
  FROM plan_delivery_contracts c WHERE (c.project_id,c.plan_version)=(target,version)
$$;

CREATE OR REPLACE FUNCTION verified_document_format(target uuid, ver integer) RETURNS text
LANGUAGE sql STABLE AS $$
 SELECT agent.tool->>'operation' FROM artifacts a
 JOIN artifact_versions v ON (v.artifact_id,v.version)=(a.id,ver)
 JOIN artifact_files f ON (f.artifact_id,f.version)=(v.artifact_id,v.version)
 JOIN runs r ON r.id=a.run_id
 JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(a.project_id,r.plan_version,a.ordinal)
 JOIN agent_revisions agent ON (agent.agent_id,agent.revision)=(n.agent_id,n.agent_revision)
 WHERE a.id=target AND (
   (agent.tool->>'operation'='docx' AND f.media_type='application/vnd.openxmlformats-officedocument.wordprocessingml.document')
   OR (agent.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb
       AND f.media_type='application/vnd.openxmlformats-officedocument.presentationml.presentation')
 )
 AND EXISTS (
   SELECT FROM invocation_files ff JOIN invocations i USING(run_id,ordinal)
   WHERE ff.object_hash=f.object_hash AND i.ordinal=a.ordinal AND i.result=v.body AND
   (i.run_id=a.run_id OR EXISTS(SELECT FROM change_batches cb WHERE cb.run_id=i.run_id AND cb.artifact_id=a.id)
     OR EXISTS(SELECT FROM rule_branch_steps b WHERE b.run_id=i.run_id AND b.artifact_id=a.id AND b.result_version=ver AND b.mode<>'reuse'))
 );
$$;

CREATE OR REPLACE FUNCTION artifact_step_receipts(target uuid, edition integer) RETURNS SETOF run_source_receipts
LANGUAGE plpgsql STABLE ROWS 8 AS $$
BEGIN
 RETURN QUERY
 WITH RECURSIVE origin(run_id,ordinal) AS (
   SELECT a.run_id,a.ordinal FROM artifacts a WHERE a.id=target AND edition=1
   UNION
   SELECT b.run_id,a.ordinal FROM change_batches b JOIN artifacts a ON a.id=b.artifact_id
   WHERE a.id=target AND b.applied_version=edition
   UNION
   SELECT b.run_id,b.ordinal FROM rule_branch_steps b
   WHERE b.artifact_id=target AND b.result_version=edition AND b.mode<>'reuse'
 ), lineage(run_id,ordinal,attempt_no) AS (
   SELECT i.run_id,i.ordinal,i.attempt_no FROM origin o JOIN invocations i USING (run_id,ordinal)
   UNION
   SELECT p.parent_run,p.parent_ordinal,p.parent_attempt FROM lineage l
   JOIN run_working_parents p USING (run_id,ordinal,attempt_no)
 )
 SELECT DISTINCT r.* FROM lineage l
 JOIN run_working_inputs d USING (run_id,ordinal,attempt_no)
 JOIN step_source_receipt_inputs f ON (f.run_id,f.reader,f.source_id,f.source_version)=(l.run_id,l.ordinal,d.ref,d.revision)
 JOIN run_source_receipts r USING (request_revision)
 WHERE d.kind='source'
 UNION
 SELECT r.* FROM change_batches b JOIN source_assessments c ON c.id=b.source_assessment
 CROSS JOIN LATERAL artifact_step_receipts(target,b.base_version) r
 WHERE b.artifact_id=target AND b.applied_version=edition AND b.base_version<edition
   AND (r.source_id,r.source_version)=(c.source_id,c.pinned_version);
END
$$;

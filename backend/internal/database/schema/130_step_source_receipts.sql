ALTER TABLE runs ADD COLUMN step_source_receipt boolean NOT NULL DEFAULT false;
CREATE SEQUENCE source_receipt_request_no AS integer;
ALTER TABLE calculation_requests ADD COLUMN request_revision integer CHECK (request_revision > 0);
ALTER TABLE controls ADD COLUMN wake_ordinal integer CHECK (wake_ordinal > 0);
ALTER TABLE channel_proposals ADD COLUMN receipt_question_revision integer CHECK (receipt_question_revision > 0);
ALTER TABLE channel_attachment_jobs ADD COLUMN receipt_question_revision integer CHECK (receipt_question_revision > 0);

CREATE TABLE run_source_receipts (
 run_id uuid NOT NULL REFERENCES runs,
 ordinal integer NOT NULL CHECK (ordinal > 0),
 request_revision integer NOT NULL UNIQUE CHECK (request_revision > 0),
 source_id uuid NOT NULL,
 source_version integer NOT NULL,
 kind text NOT NULL CHECK (kind IN ('lookup','calculation')),
 excerpt_start integer NOT NULL CHECK (excerpt_start >= 0),
 excerpt_end integer NOT NULL CHECK (excerpt_end > excerpt_start AND excerpt_end-excerpt_start <= 320),
 PRIMARY KEY (run_id,ordinal,source_id),
 FOREIGN KEY (source_id,source_version) REFERENCES source_versions
);
CREATE INDEX run_source_receipts_by_source ON run_source_receipts(source_id);

-- A synthesis may reopen the exact source of an accepted refinement
-- observation. Other dependents consume the parent's result, not its input.
CREATE VIEW step_source_receipt_inputs AS
 SELECT p.*,p.ordinal AS reader FROM run_source_receipts p
 UNION ALL
 SELECT p.*,e.ordinal AS reader FROM run_source_receipts p
 JOIN runs r ON r.id=p.run_id
 JOIN node_edges e ON (e.project_id,e.plan_version,e.depends_on)=(r.project_id,r.plan_version,p.ordinal)
 JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(e.project_id,e.plan_version,e.ordinal)
 JOIN invocations i ON (i.run_id,i.ordinal)=(p.run_id,p.ordinal)
 JOIN lookup_turns t ON (t.run_id,t.ordinal,t.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)
 WHERE n.lookup_phase='synthesize' AND t.outcome='lookup_refined' AND EXISTS (
   SELECT FROM jsonb_to_recordset(t.refinement_evidence) AS c("Source" uuid,"Version" integer)
   WHERE (c."Source",c."Version")=(p.source_id,p.source_version)
 );

CREATE FUNCTION source_receipt_open(state text, request_revision integer) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN request_revision IS NULL THEN state='blocked' ELSE state='running' END
$$;

CREATE VIEW current_source_receipt_requests AS
 SELECT q.* FROM calculation_requests q JOIN runs r ON r.id=q.run_id
 WHERE source_receipt_open(r.status,q.request_revision);

-- Follow the attempts a specific edition used, including captured parents.
-- Project membership alone does not make every receipt a dependency.
CREATE FUNCTION artifact_step_receipts(target uuid, edition integer) RETURNS SETOF run_source_receipts
LANGUAGE plpgsql STABLE ROWS 8 AS $$
BEGIN
 RETURN QUERY
 WITH RECURSIVE origin(run_id,ordinal) AS (
   SELECT a.run_id,a.ordinal FROM artifacts a WHERE a.id=target AND edition=1
   UNION
   SELECT b.run_id,a.ordinal FROM change_batches b JOIN artifacts a ON a.id=b.artifact_id
   WHERE a.id=target AND b.applied_version=edition
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

ALTER TABLE delivery_source_evidence ADD COLUMN step_receipts jsonb NOT NULL DEFAULT '[]'::jsonb;

CREATE OR REPLACE FUNCTION run_working_set_usable(run_key uuid, step_no integer, attempt integer) RETURNS boolean
LANGUAGE sql STABLE AS $$
 WITH RECURSIVE lineage(run_id,ordinal,attempt_no,invalidated_at) AS (
   SELECT s.run_id,s.ordinal,s.attempt_no,s.invalidated_at FROM run_working_sets s
   WHERE (s.run_id,s.ordinal,s.attempt_no)=(run_key,step_no,attempt)
   UNION
   SELECT origin.run_id,origin.ordinal,origin.attempt_no,origin.invalidated_at FROM lineage l
   JOIN run_working_parents p ON(p.run_id,p.ordinal,p.attempt_no)=(l.run_id,l.ordinal,l.attempt_no)
   JOIN run_working_sets origin ON(origin.run_id,origin.ordinal,origin.attempt_no)=(p.parent_run,p.parent_ordinal,p.parent_attempt)
 )
 SELECT NOT EXISTS(
   SELECT FROM lineage l JOIN runs r ON r.id=l.run_id
   JOIN run_working_sets s USING(run_id,ordinal,attempt_no)
   WHERE l.invalidated_at IS NOT NULL OR (s.build_digest IS NOT NULL AND s.build_digest IS DISTINCT FROM
     agent_build_digest(agent_build_definition(s.build_agent,s.build_revision))) OR NOT EXISTS(
     SELECT FROM project_members pm JOIN members u ON u.id=pm.member_id AND u.enabled
     WHERE (pm.project_id,pm.member_id)=(r.project_id,r.actor))
   OR EXISTS(
     SELECT FROM run_working_inputs d WHERE(d.run_id,d.ordinal,d.attempt_no)=(l.run_id,l.ordinal,l.attempt_no)
     AND CASE d.kind
       WHEN 'source' THEN NOT EXISTS(
         SELECT FROM run_source_inputs f WHERE f.run_id=l.run_id AND f.source_id=d.ref AND f.version=d.revision
           AND (f.extracted IS NOT NULL OR f.picture_hash IS NOT NULL)
         UNION ALL
         SELECT FROM step_source_receipt_inputs f
         JOIN sources source ON (source.id,source.project_id)=(f.source_id,r.project_id)
         JOIN source_versions v ON (v.source_id,v.version)=(f.source_id,f.source_version)
         WHERE (f.run_id,f.reader,f.source_id,f.source_version)=(l.run_id,l.ordinal,d.ref,d.revision)
           AND source.revoked_at IS NULL AND v.extracted IS NOT NULL)
       WHEN 'rule' THEN NOT rule_usable(d.ref,d.revision,r.actor) OR NOT EXISTS(
         SELECT FROM plan_rules p JOIN team_rules tr ON tr.id=p.rule_id JOIN projects project ON project.id=p.project_id
         WHERE(p.project_id,p.plan_version,p.rule_id,p.rule_revision)=(r.project_id,r.plan_version,d.ref,d.revision)
           AND tr.team_id=project.team_id)
       WHEN 'preference' THEN NOT run_memory_can_use_personal(l.run_id) OR NOT EXISTS(
         SELECT FROM plan_preferences p JOIN preferences pref ON pref.id=p.preference_id AND pref.revision=p.revision
         WHERE(p.project_id,p.plan_version,p.preference_id,p.revision)=(r.project_id,r.plan_version,d.ref,d.revision)
           AND pref.member_id=r.actor AND pref.enabled AND pref.deleted_at IS NULL AND NOT pref.departure_guard
           AND pref.body IS NOT NULL AND pref.expires_at>statement_timestamp())
     END
   )
 )
$$;

CREATE OR REPLACE VIEW changed_artifact_sources AS
 SELECT a.project_id,a.id AS artifact_id,a.current_version AS artifact_version,
 s.id AS source_id,ps.source_version AS pinned,s.current_version AS observed,s.revoked_at IS NOT NULL AS revoked,
 v.filename AS original_name,s.name AS current_name,c.id AS assessment,c.decision,c.actor,c.reason,c.feedback_id
 FROM artifacts a JOIN runs r ON r.id=a.run_id
 JOIN LATERAL (
   SELECT source_id,source_version FROM plan_sources
   WHERE (project_id,plan_version)=(r.project_id,r.plan_version)
   UNION
   SELECT source_id,source_version FROM artifact_step_receipts(a.id,a.current_version)
 ) ps ON true
 JOIN sources s ON s.id=ps.source_id
 JOIN source_versions v ON (v.source_id,v.version)=(s.id,ps.source_version)
 JOIN source_versions latest ON (latest.source_id,latest.version)=(s.id,s.current_version)
 LEFT JOIN source_assessments c ON(c.artifact_id,c.artifact_version,c.source_id,c.observed_version,c.revoked)=(a.id,a.current_version,s.id,s.current_version,s.revoked_at IS NOT NULL)
 WHERE s.revoked_at IS NOT NULL OR v.object_hash<>latest.object_hash OR v.media_type<>latest.media_type OR v.extracted IS DISTINCT FROM latest.extracted;

CREATE OR REPLACE VIEW distribution_cases AS
 WITH scope AS (
 SELECT e.source_id,i.artifact_id,i.version FROM delivery_distribution_inputs e
 JOIN delivery_items i USING(delivery_id) JOIN withdrawn_delivery_sources s ON s.id=e.source_id
 WHERE EXISTS (SELECT FROM delivery_source_evidence d WHERE (d.delivery_id,d.source_id)=(e.delivery_id,e.source_id) AND d.step_receipts='[]')
    OR NOT EXISTS (SELECT FROM delivery_source_evidence d WHERE (d.delivery_id,d.source_id)=(e.delivery_id,e.source_id))
    OR EXISTS (SELECT FROM artifact_step_receipts(i.artifact_id,i.version) p WHERE p.source_id=e.source_id)
 UNION
 SELECT ps.source_id,a.id,a.current_version FROM artifacts a JOIN projects p ON p.id=a.project_id AND p.stage='review'
 JOIN runs r ON r.id=a.run_id JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN withdrawn_delivery_sources s ON s.id=ps.source_id
 UNION
 SELECT ps.source_id,a.id,a.current_version FROM artifacts a JOIN projects p ON p.id=a.project_id
 CROSS JOIN LATERAL artifact_step_receipts(a.id,a.current_version) ps
 JOIN sources s ON s.id=ps.source_id AND s.revoked_at IS NOT NULL
 WHERE p.stage IN ('running','paused','blocked','reconcile','review'))
 SELECT x.*,s.project_id,s.created_by,s.name AS source_name,coalesce(v.revision,0) AS revision,
 coalesce(v.decision,'pending') AS state,v.actor,v.reason,v.created_at
 FROM scope x JOIN sources s ON s.id=x.source_id LEFT JOIN LATERAL
 (SELECT * FROM distribution_reviews r WHERE (r.source_id,r.artifact_id,r.version)=(x.source_id,x.artifact_id,x.version)
 ORDER BY r.revision DESC LIMIT 1) v ON true;


CREATE OR REPLACE VIEW run_source_inputs AS
 SELECT r.id run_id, s.id source_id, ps.source_version plan_pin, v.version,
        coalesce(v.filename,'历史资料') filename,
        CASE WHEN s.revoked_at IS NULL AND (c.id IS NULL OR s.current_version=c.observed_version) THEN v.extracted END extracted,
        CASE WHEN source_is_picture(v.media_type) THEN v.media_type END picture_media,
        CASE WHEN source_is_picture(v.media_type) AND s.revoked_at IS NULL
               AND (c.id IS NULL OR s.current_version=c.observed_version) THEN v.object_hash END picture_hash,
        ps.picture_order
 FROM runs r JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN sources s ON (s.id,s.project_id)=(ps.source_id,r.project_id)
 LEFT JOIN change_batches b ON b.run_id=r.id
 LEFT JOIN source_assessments c ON (c.id,c.source_id)=(b.source_assessment,s.id)
 JOIN source_versions v ON (v.source_id,v.version)=(s.id,coalesce(c.observed_version,ps.source_version))
 UNION ALL
 -- An explicit change assessment can bind the replacement for a scoped pin.
 -- It fixes the observed revision using the same contract as a plan source.
 SELECT r.id,s.id,c.pinned_version,v.version,coalesce(v.filename,'历史资料'),
        CASE WHEN s.revoked_at IS NULL AND s.current_version=c.observed_version THEN v.extracted END,
        NULL,NULL,NULL
 FROM runs r JOIN change_batches b ON b.run_id=r.id
 JOIN source_assessments c ON (c.id,c.artifact_id,c.artifact_version)=(b.source_assessment,b.artifact_id,b.base_version)
 JOIN sources s ON (s.id,s.project_id)=(c.source_id,r.project_id)
 JOIN source_versions v ON (v.source_id,v.version)=(s.id,c.observed_version)
 WHERE NOT EXISTS(SELECT FROM plan_sources p WHERE (p.project_id,p.plan_version,p.source_id)=(r.project_id,r.plan_version,s.id))
   AND EXISTS(SELECT FROM artifact_step_receipts(b.artifact_id,b.base_version) receipt
     WHERE (receipt.source_id,receipt.source_version)=(s.id,c.pinned_version));

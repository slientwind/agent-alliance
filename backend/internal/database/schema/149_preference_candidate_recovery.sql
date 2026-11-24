ALTER TABLE runs ADD COLUMN skip_unavailable_preferences boolean NOT NULL DEFAULT false;

-- An automatic downstream revision copies content, so it still has the
-- original execution dependencies until a reviewer accepts it.
ALTER TABLE artifact_versions ADD COLUMN copied_from integer,
 ADD CHECK (copied_from=version-1),
 ADD FOREIGN KEY (artifact_id,copied_from) REFERENCES artifact_versions(artifact_id,version);

UPDATE artifact_versions v SET copied_from=b.version
FROM artifact_versions b
WHERE (b.artifact_id,b.version,b.body)=(v.artifact_id,v.version-1,v.body)
 AND v.reason='上游内容变化，需要核对引用';

-- Also recognize copies written by a previous API during a rolling upgrade.
CREATE FUNCTION bind_artifact_copy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.copied_from IS NULL AND NEW.reason='上游内容变化，需要核对引用' AND EXISTS(
   SELECT FROM artifact_versions b WHERE (b.artifact_id,b.version,b.body)=(NEW.artifact_id,NEW.version-1,NEW.body)
 ) THEN
   NEW.copied_from := NEW.version-1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER bind_artifact_copy BEFORE INSERT ON artifact_versions FOR EACH ROW EXECUTE FUNCTION bind_artifact_copy();

CREATE OR REPLACE FUNCTION working_artifact_origin(artifact_key uuid, edition integer)
RETURNS TABLE(run_id uuid,ordinal integer,attempt_no integer) LANGUAGE sql STABLE AS $$
 WITH RECURSIVE undelivered(version) AS (
   SELECT edition WHERE NOT EXISTS(SELECT FROM reviews v WHERE (v.artifact_id,v.version)=(artifact_key,edition))
   UNION ALL
   SELECT v.copied_from FROM undelivered e
   JOIN artifact_versions v ON (v.artifact_id,v.version)=(artifact_key,e.version)
   WHERE v.copied_from IS NOT NULL AND NOT EXISTS(
     SELECT FROM reviews approved WHERE (approved.artifact_id,approved.version)=(artifact_key,v.copied_from))
 )
 SELECT i.run_id,i.ordinal,i.attempt_no FROM invocations i JOIN (
   SELECT a.run_id,a.ordinal FROM artifacts a WHERE a.id=artifact_key AND EXISTS(SELECT FROM undelivered WHERE version=1)
   UNION
   SELECT c.run_id,a.ordinal FROM change_batches c JOIN artifacts a ON a.id=c.artifact_id
   WHERE a.id=artifact_key AND c.applied_version IN (SELECT version FROM undelivered)
   UNION
   SELECT b.run_id,b.ordinal FROM rule_branch_steps b
   WHERE b.artifact_id=artifact_key AND b.result_version IN (SELECT version FROM undelivered) AND b.mode<>'reuse'
 ) origin USING(run_id,ordinal)
$$;

CREATE FUNCTION run_preference_pin_usable(run_key uuid, build uuid, ref uuid, ref_revision integer)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT run_memory_can_use_personal(run_key) AND EXISTS(
   SELECT FROM runs r JOIN plan_bound_preferences p ON (p.project_id,p.plan_version)=(r.project_id,r.plan_version)
   JOIN preferences f ON (f.id,f.revision)=(p.preference_id,p.revision)
   WHERE (r.id,p.preference_id,p.revision)=(run_key,ref,ref_revision)
     AND p.agent_scope IS NOT DISTINCT FROM f.agent_scope AND (p.agent_scope IS NULL OR p.agent_scope=build)
     AND f.member_id=r.actor AND f.enabled AND f.deleted_at IS NULL AND NOT f.departure_guard
     AND f.body IS NOT NULL AND f.expires_at>statement_timestamp())
$$;

CREATE FUNCTION working_preference_inputs(run_key uuid, step_no integer, attempt integer)
RETURNS TABLE(run_id uuid,ref uuid,revision integer,usable boolean) LANGUAGE sql STABLE AS $$
 WITH RECURSIVE lineage(run_id,ordinal,attempt_no) AS (
   SELECT run_key,step_no,attempt
   UNION
   SELECT p.parent_run,p.parent_ordinal,p.parent_attempt FROM lineage l
   JOIN run_working_parents p USING(run_id,ordinal,attempt_no)
 )
 SELECT d.run_id,d.ref,d.revision,
   run_preference_pin_usable(d.run_id,s.build_agent,d.ref,d.revision)
     AND NOT coalesce(s.invalidated_kind='preference' AND s.invalidated_ref=d.ref,false)
 FROM lineage l JOIN run_working_sets s USING(run_id,ordinal,attempt_no)
 JOIN run_working_inputs d USING(run_id,ordinal,attempt_no) WHERE d.kind='preference'
$$;

CREATE FUNCTION artifact_draft_block_reason(target uuid, edition integer) RETURNS text
LANGUAGE sql STABLE AS $$
 WITH origin AS MATERIALIZED (SELECT * FROM working_artifact_origin(target,edition))
 SELECT CASE WHEN EXISTS(SELECT FROM origin o
    CROSS JOIN LATERAL working_preference_inputs(o.run_id,o.ordinal,o.attempt_no) f WHERE NOT f.usable) THEN 'preference'
  WHEN EXISTS(SELECT FROM origin o
    CROSS JOIN LATERAL invalid_working_step_rules(o.run_id,o.ordinal,o.attempt_no) r WHERE r.scoped) THEN 'rule' END
$$;

-- Legacy readers, including distribution_readable, use the expanded gate.
CREATE OR REPLACE FUNCTION artifact_rule_draft_readable(target uuid, edition int) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT artifact_draft_block_reason(target,edition) IS NULL
$$;

CREATE FUNCTION lock_artifact_preference_inputs(target uuid, edition integer) RETURNS void
LANGUAGE sql AS $$
 SELECT FROM preferences f WHERE EXISTS(
   SELECT FROM working_artifact_origin(target,edition) o
   CROSS JOIN LATERAL working_preference_inputs(o.run_id,o.ordinal,o.attempt_no) pin WHERE pin.ref=f.id)
 ORDER BY f.id FOR SHARE OF f
$$;

-- Review receipts make the edition independent. Serialize the boundary with
-- edits and withdrawals even for an API still using the previous write path.
CREATE FUNCTION require_review_preference_inputs() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM lock_artifact_preference_inputs(NEW.artifact_id,NEW.version);
 IF artifact_draft_block_reason(NEW.artifact_id,NEW.version)='preference' THEN
   RAISE EXCEPTION 'review_candidate_preference_unavailable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER require_review_preference_inputs BEFORE INSERT ON reviews
 FOR EACH ROW EXECUTE FUNCTION require_review_preference_inputs();

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
       WHEN 'rule' THEN NOT EXISTS(
         SELECT FROM effective_run_rules x
         WHERE (x.run_id,x.ordinal,x.rule_id,x.rule_revision)=(l.run_id,l.ordinal,d.ref,d.revision) AND x.usable)
       WHEN 'skill' THEN NOT EXISTS(
         SELECT FROM skill_revisions v JOIN skill_packs p ON p.id=v.skill_id
         JOIN team_members m ON (m.team_id,m.member_id)=(p.team_id,r.actor)
         WHERE (v.skill_id,v.revision)=(d.ref,d.revision) AND v.revoked_at IS NULL
       )
       WHEN 'preference' THEN NOT run_preference_pin_usable(l.run_id,s.build_agent,d.ref,d.revision)
     END
   )
 )
$$;

CREATE OR REPLACE VIEW review_actions AS
 WITH review_projects AS NOT MATERIALIZED (
   SELECT id,owner,team_id,final_reviewer,revision,plan_version,coalesce(review_started_at,updated_at) AS started
   FROM projects WHERE stage='review'
 ), coverage AS NOT MATERIALIZED (
   SELECT p.id,projection.coverage,project_delivery_mode(projection.coverage) AS mode
   FROM review_projects p CROSS JOIN LATERAL (SELECT project_delivery_coverage(p.id,p.plan_version) AS coverage) projection
 ), items AS NOT MATERIALIZED (
   SELECT a.id,a.project_id,a.name,a.ordinal,a.current_version,v.created_at,r.created_at AS reviewed,
     r.reviewer IS NOT NULL AND NOT source_check_needed(a.id) AS approved,
     takeover_review_needed(a.project_id,a.id,a.current_version) AS takeover,
     EXISTS(SELECT FROM feedback f WHERE f.artifact_id=a.id AND f.state IN('required','investigate','verification')) AS blocked
   FROM review_projects p JOIN artifacts a ON a.project_id=p.id
   JOIN artifact_versions v ON(v.artifact_id,v.version)=(a.id,a.current_version)
   LEFT JOIN reviews r ON(r.artifact_id,r.version)=(a.id,a.current_version)
 ), actions AS (
   SELECT i.project_id,'review:'||i.id||':'||i.current_version AS action_key,'review' AS kind,
     i.id AS subject,i.name,i.current_version AS version,0 AS revision,i.ordinal,'' AS position,
     greatest(i.created_at,p.started) AS opened,CASE WHEN i.approved AND i.takeover THEN p.final_reviewer END AS assigned
   FROM items i JOIN review_projects p ON p.id=i.project_id
   WHERE (NOT i.approved OR i.takeover) AND NOT i.blocked
     AND artifact_rule_draft_readable(i.id,i.current_version)
   UNION ALL
   SELECT i.project_id,'feedback:'||f.id||':'||coalesce(e.revision,1)||':'||i.current_version,f.state,f.id,i.name,i.current_version,
     f.revision,i.ordinal,left(f.position,80),greatest(p.started,i.created_at,coalesce(e.created_at,f.created_at)),f.reviewer
   FROM items i JOIN feedback f ON f.artifact_id=i.id JOIN review_projects p ON p.id=i.project_id
   LEFT JOIN LATERAL (SELECT revision,created_at FROM feedback_events
     WHERE feedback_id=f.id AND action<>'transfer' ORDER BY revision DESC LIMIT 1) e ON true
   WHERE f.state IN('suggested','required','investigate','verification')
   UNION ALL
   SELECT p.id,'coverage:'||p.id||':'||p.plan_version,'coverage',p.id,'核对必交项和缺项安排',0,p.revision,0,'',p.started,NULL::uuid
   FROM review_projects p JOIN coverage c USING(id) WHERE c.mode='coverage'
   UNION ALL
   SELECT p.id,'deliver:'||p.id||':'||md5(string_agg(i.id||':'||i.current_version,',' ORDER BY i.id)),
     'deliver',p.id,'',0,p.revision,0,'',greatest(p.started,max(i.reviewed)),p.final_reviewer
   FROM review_projects p JOIN items i ON i.project_id=p.id JOIN coverage c ON c.id=p.id
   WHERE c.mode='full' OR c.mode='partial' AND c.coverage @> jsonb_build_object('items',
     jsonb_build_array(jsonb_build_object('artifact',i.id,'state','covered')))
   GROUP BY p.id,p.revision,p.started,p.final_reviewer
   HAVING bool_and(i.approved AND NOT i.takeover AND NOT i.blocked) AND NOT candidate_distribution_held(p.id)
      AND NOT EXISTS(SELECT FROM changed_artifact_sources s WHERE s.project_id=p.id AND s.decision IS DISTINCT FROM 'retain')
 )
 SELECT t.*,p.team_id,p.owner,
   ARRAY(SELECT pm.member_id FROM project_members pm JOIN members m ON m.id=pm.member_id
     WHERE pm.project_id=p.id AND m.enabled AND m.kind='person'
     AND (pm.can_review OR t.kind='deliver') AND (t.assigned IS NULL OR t.assigned=pm.member_id)
     ORDER BY pm.member_id) AS recipients
 FROM actions t JOIN review_projects p ON p.id=t.project_id;

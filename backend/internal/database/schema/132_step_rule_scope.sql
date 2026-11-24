-- An empty scope retains the team-wide rule contract. A published revision's
-- targets are immutable, just like its text and expiry.
ALTER TABLE rule_versions ADD COLUMN agent_scope uuid[] NOT NULL DEFAULT '{}'
    CHECK (array_position(agent_scope,NULL::uuid) IS NULL);

CREATE SEQUENCE step_rule_wait_no AS integer;
CREATE TABLE step_rule_waits (
    run_id uuid NOT NULL REFERENCES runs,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    rule_id uuid NOT NULL,
    rule_revision integer NOT NULL,
    wait_revision integer NOT NULL DEFAULT nextval('step_rule_wait_no') CHECK (wait_revision > 0),
    rebuild_required boolean NOT NULL,
    PRIMARY KEY (run_id,ordinal),
    FOREIGN KEY (rule_id,rule_revision) REFERENCES rule_versions
);
CREATE TABLE step_rule_replacements (
    run_id uuid NOT NULL REFERENCES runs,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    rule_id uuid NOT NULL,
    rule_revision integer NOT NULL,
    PRIMARY KEY (run_id,ordinal,rule_id),
    FOREIGN KEY (rule_id,rule_revision) REFERENCES rule_versions
);

CREATE VIEW effective_run_rules AS
 SELECT n.run_id,n.ordinal,p.rule_id,v.revision AS rule_revision,
        cardinality(original.agent_scope)>0 AS scoped,
        tr.team_id=project.team_id AND rule_usable(p.rule_id,v.revision,r.actor) AS usable
 FROM effective_run_nodes n JOIN runs r ON r.id=n.run_id
 JOIN nodes primary_node ON (primary_node.project_id,primary_node.plan_version,primary_node.ordinal)=(n.project_id,n.plan_version,n.ordinal)
 JOIN projects project ON project.id=r.project_id
 JOIN plan_rules p ON (p.project_id,p.plan_version)=(n.project_id,n.plan_version)
 JOIN team_rules tr ON tr.id=p.rule_id
 JOIN rule_versions original ON (original.rule_id,original.revision)=(p.rule_id,p.rule_revision)
 LEFT JOIN step_rule_replacements replacement ON (replacement.run_id,replacement.ordinal,replacement.rule_id)=(n.run_id,n.ordinal,p.rule_id)
 JOIN rule_versions v ON (v.rule_id,v.revision)=(p.rule_id,coalesce(replacement.rule_revision,p.rule_revision))
 WHERE cardinality(original.agent_scope)=0 OR n.agent_id=ANY(original.agent_scope) OR primary_node.agent_id=ANY(original.agent_scope);

CREATE VIEW step_rule_recovery_choices AS
 SELECT w.*,CASE WHEN w.rebuild_required OR EXISTS(SELECT FROM invocations i WHERE (i.run_id,i.ordinal)=(w.run_id,w.ordinal)) THEN NULL
          WHEN current_pin.usable THEN w.rule_revision
          WHEN rule_usable(w.rule_id,v.revision,r.actor) AND (n.agent_id=ANY(v.agent_scope) OR primary_node.agent_id=ANY(v.agent_scope))
          THEN v.revision END AS available_revision
 FROM step_rule_waits w JOIN runs r ON r.id=w.run_id
 JOIN projects p ON p.id=r.project_id
 JOIN team_rules tr ON tr.id=w.rule_id AND tr.team_id=p.team_id
 JOIN effective_run_rules current_pin ON (current_pin.run_id,current_pin.ordinal,current_pin.rule_id,current_pin.rule_revision)
       =(w.run_id,w.ordinal,w.rule_id,w.rule_revision) AND current_pin.scoped
 JOIN effective_run_nodes n ON (n.run_id,n.ordinal)=(w.run_id,w.ordinal)
 JOIN nodes primary_node ON (primary_node.project_id,primary_node.plan_version,primary_node.ordinal)=(n.project_id,n.plan_version,n.ordinal)
 LEFT JOIN rule_versions v ON (v.rule_id,v.revision)=(tr.id,tr.published_revision);

-- Planning precedes Agent selection. Only team-wide rules can instruct the
-- planner. Scoped rules enter the selected steps at execution time.
CREATE OR REPLACE FUNCTION planning_rule_pins(project_key uuid, who uuid) RETURNS jsonb
LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN coalesce(bool_and(rule_usable(r.id,r.published_revision,who)),true)
 THEN coalesce(jsonb_agg(jsonb_build_object('id',r.id,'rev',r.published_revision) ORDER BY r.id),'[]')
 END FROM projects p JOIN team_rules r ON r.team_id=p.team_id
 JOIN rule_versions v ON (v.rule_id,v.revision)=(r.id,r.published_revision)
 WHERE p.id=project_key AND NOT r.disabled AND cardinality(v.agent_scope)=0
$$;

ALTER TABLE run_working_sets ADD COLUMN invalidated_kind text, ADD COLUMN invalidated_ref uuid;
CREATE OR REPLACE FUNCTION invalidate_run_working_input() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE run_working_sets s SET invalidated_at=clock_timestamp(),invalidated_kind=TG_ARGV[0],invalidated_ref=NEW.id
    FROM run_working_inputs d
    WHERE (s.run_id,s.ordinal,s.attempt_no)=(d.run_id,d.ordinal,d.attempt_no)
      AND d.kind=TG_ARGV[0] AND d.ref=NEW.id AND s.invalidated_at IS NULL;
    RETURN NULL;
END $$;

-- Follow only the observations captured by the attempt, including ancestors.
-- A restored publication cannot resurrect a draft that already lost scope.
CREATE FUNCTION invalid_working_step_rules(run_key uuid, step_no int, attempt int, viewer uuid DEFAULT NULL)
RETURNS TABLE(rule_id uuid, rule_revision integer, scoped boolean) LANGUAGE sql STABLE AS $$
 WITH RECURSIVE lineage(run_id,ordinal,attempt_no) AS (
   SELECT run_key,step_no,attempt
   UNION
   SELECT p.parent_run,p.parent_ordinal,p.parent_attempt FROM lineage l
   JOIN run_working_parents p USING(run_id,ordinal,attempt_no)
 )
 SELECT DISTINCT d.ref,d.revision,cardinality(v.agent_scope)>0 FROM lineage l
 JOIN run_working_sets s USING(run_id,ordinal,attempt_no)
 JOIN run_working_inputs d USING(run_id,ordinal,attempt_no)
 JOIN runs r ON r.id=l.run_id
 JOIN rule_versions v ON (v.rule_id,v.revision)=(d.ref,d.revision)
 WHERE d.kind='rule' AND (s.invalidated_kind='rule' AND s.invalidated_ref=d.ref
   OR viewer IS NOT NULL AND NOT rule_usable(d.ref,d.revision,viewer)
   OR NOT EXISTS(SELECT FROM effective_run_rules x
        WHERE (x.run_id,x.ordinal,x.rule_id,x.rule_revision)=(l.run_id,l.ordinal,d.ref,d.revision) AND x.usable))
$$;

CREATE FUNCTION artifact_rule_draft_readable(target uuid, edition int) RETURNS boolean
LANGUAGE plpgsql STABLE AS $$
BEGIN
 RETURN NOT EXISTS (
   SELECT FROM working_artifact_origin(target,edition) o
   CROSS JOIN LATERAL invalid_working_step_rules(o.run_id,o.ordinal,o.attempt_no) revoked
   WHERE revoked.scoped
 );
END
$$;

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
       WHEN 'preference' THEN NOT run_memory_can_use_personal(l.run_id) OR NOT EXISTS(
         SELECT FROM plan_preferences p JOIN preferences pref ON pref.id=p.preference_id AND pref.revision=p.revision
         WHERE(p.project_id,p.plan_version,p.preference_id,p.revision)=(r.project_id,r.plan_version,d.ref,d.revision)
           AND pref.member_id=r.actor AND pref.enabled AND pref.deleted_at IS NULL AND NOT pref.departure_guard
           AND pref.body IS NOT NULL AND pref.expires_at>statement_timestamp())
     END
   )
 )
$$;

CREATE OR REPLACE FUNCTION distribution_readable(target uuid, ver integer, member uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT artifact_rule_draft_readable(target,ver) AND NOT EXISTS(
 SELECT FROM artifacts a JOIN runs r ON r.id=a.run_id JOIN projects p ON p.id=a.project_id
 JOIN project_members m ON m.project_id=p.id AND m.member_id=member
 JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN withdrawn_delivery_sources s ON s.id=ps.source_id
 LEFT JOIN LATERAL(SELECT decision FROM distribution_reviews v
 WHERE(v.source_id,v.artifact_id,v.version)=(s.id,a.id,ver) ORDER BY revision DESC LIMIT 1) latest ON true
 WHERE a.id=target AND NOT m.can_review AND member NOT IN(p.owner,s.created_by) AND latest.decision IS DISTINCT FROM 'reference')
$$;

ALTER TABLE skill_revisions ADD COLUMN activation_terms text[] NOT NULL DEFAULT '{}'
 CHECK (cardinality(activation_terms)<=8);

CREATE FUNCTION keep_skill_activation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.activation_terms IS DISTINCT FROM NEW.activation_terms THEN
   RAISE EXCEPTION 'Skill activation terms belong to an immutable revision';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER skill_activation_immutable BEFORE UPDATE OF activation_terms ON skill_revisions
 FOR EACH ROW EXECUTE FUNCTION keep_skill_activation();

CREATE TABLE validation_skill_inputs (
 trial_id uuid NOT NULL REFERENCES validations(id) ON DELETE CASCADE,
 skill_id uuid NOT NULL,
 skill_revision integer NOT NULL,
 PRIMARY KEY (trial_id,skill_id),
 FOREIGN KEY (skill_id,skill_revision) REFERENCES skill_revisions
);
CREATE INDEX validation_skill_coverage ON validation_skill_inputs(skill_id,skill_revision,trial_id);

CREATE FUNCTION agent_skill_activation_coverage(target uuid,rev integer,build text)
RETURNS TABLE(skill_id uuid,skill_revision integer,proof uuid) LANGUAGE sql STABLE AS $$
 SELECT b.skill_id,b.skill_revision,trial.id
 FROM agent_skill_bindings b
 JOIN skill_revisions s ON (s.skill_id,s.revision)=(b.skill_id,b.skill_revision)
 LEFT JOIN LATERAL (
   SELECT v.id FROM validation_skill_inputs used JOIN validations v ON v.id=used.trial_id
   WHERE (used.skill_id,used.skill_revision)=(b.skill_id,b.skill_revision)
     AND (v.agent_id,v.revision,v.build_digest)=(target,rev,build) AND v.status='passed'
     AND NOT EXISTS(SELECT FROM validation_withdrawals w WHERE w.trial_id=v.id)
   ORDER BY v.created_at DESC,v.id DESC LIMIT 1
 ) trial ON true
 WHERE (b.agent_id,b.revision)=(target,rev) AND b.root AND cardinality(s.activation_terms)>0
$$;

CREATE OR REPLACE FUNCTION agent_verified(target uuid, rev integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(
  (SELECT bool_and(coalesce(c.passed,false)) FROM check_outcomes c WHERE (c.agent_id,c.revision)=(target,rev)),
  EXISTS(SELECT FROM validations v WHERE (v.agent_id,v.revision)=(target,rev) AND v.status='passed'
   AND NOT EXISTS (SELECT FROM validation_withdrawals w WHERE w.trial_id=v.id))
 ) AND CASE WHEN EXISTS(
   SELECT FROM agent_skill_bindings b JOIN skill_revisions s ON (s.skill_id,s.revision)=(b.skill_id,b.skill_revision)
   WHERE (b.agent_id,b.revision)=(target,rev) AND b.root AND cardinality(s.activation_terms)>0
 ) THEN NOT EXISTS (
   SELECT FROM agent_skill_activation_coverage(target,rev,agent_build_digest(agent_build_definition(target,rev)))
   WHERE proof IS NULL
 ) ELSE true END
$$;

ALTER TABLE run_working_inputs DROP CONSTRAINT run_working_inputs_kind_check;
ALTER TABLE run_working_inputs ADD CHECK (kind IN ('source','preference','rule','skill'));

CREATE FUNCTION invalidate_revoked_skill_working_sets() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE run_working_sets s SET invalidated_at=clock_timestamp()
 FROM run_working_inputs d
 WHERE (s.run_id,s.ordinal,s.attempt_no)=(d.run_id,d.ordinal,d.attempt_no)
   AND d.kind='skill' AND (d.ref,d.revision)=(NEW.skill_id,NEW.revision) AND s.invalidated_at IS NULL;
 RETURN NULL;
END $$;
CREATE TRIGGER skill_working_sets_revoked AFTER UPDATE OF revoked_at ON skill_revisions
 FOR EACH ROW WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL)
 EXECUTE FUNCTION invalidate_revoked_skill_working_sets();

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
       WHEN 'preference' THEN NOT run_memory_can_use_personal(l.run_id) OR NOT EXISTS(
         SELECT FROM plan_preferences p JOIN preferences pref ON pref.id=p.preference_id AND pref.revision=p.revision
         WHERE(p.project_id,p.plan_version,p.preference_id,p.revision)=(r.project_id,r.plan_version,d.ref,d.revision)
           AND pref.member_id=r.actor AND pref.enabled AND pref.deleted_at IS NULL AND NOT pref.departure_guard
           AND pref.body IS NOT NULL AND pref.expires_at>statement_timestamp())
     END
   )
 )
$$;

CREATE FUNCTION readable_run_skills(run_key uuid, step_no integer, attempt integer)
RETURNS jsonb LANGUAGE sql STABLE STRICT AS $$
 SELECT CASE WHEN run_working_set_usable(run_key,step_no,attempt) THEN (
    SELECT jsonb_agg(jsonb_build_object('name',p.name,'revision',d.revision) ORDER BY p.name,d.ref)
    FROM run_working_inputs d JOIN skill_packs p ON p.id=d.ref
    WHERE (d.run_id,d.ordinal,d.attempt_no)=(run_key,step_no,attempt) AND d.kind='skill'
 ) END
$$;

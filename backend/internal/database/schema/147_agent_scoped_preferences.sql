ALTER TABLE preferences ADD COLUMN agent_scope uuid REFERENCES agents;

-- Keep scoped pins out of the legacy input table. An older Worker must not
-- read a private preference as a project-wide input.
CREATE TABLE plan_scoped_preferences (
    project_id uuid NOT NULL,
    plan_version integer NOT NULL,
    preference_id uuid NOT NULL REFERENCES preferences,
    revision integer NOT NULL,
    agent_scope uuid NOT NULL REFERENCES agents,
    PRIMARY KEY (project_id,plan_version,preference_id),
    FOREIGN KEY (project_id,plan_version) REFERENCES plans
);

CREATE VIEW plan_bound_preferences AS
 SELECT project_id,plan_version,preference_id,revision,null::uuid AS agent_scope FROM plan_preferences
 UNION ALL
 SELECT project_id,plan_version,preference_id,revision,agent_scope FROM plan_scoped_preferences;

CREATE FUNCTION guard_unscoped_preference_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT FROM preferences f WHERE (f.id,f.revision)=(NEW.preference_id,NEW.revision) AND f.agent_scope IS NOT NULL) THEN
        RAISE EXCEPTION 'scoped preferences require a scope-aware plan writer';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER guard_unscoped_preference_binding BEFORE INSERT OR UPDATE ON plan_preferences
FOR EACH ROW EXECUTE FUNCTION guard_unscoped_preference_binding();

CREATE FUNCTION require_scoped_preference_worker() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status='submitted' AND current_setting('alliance.scoped_preference_worker',true) IS DISTINCT FROM 'true'
       AND EXISTS (SELECT FROM runs r JOIN plan_scoped_preferences p
                   ON (p.project_id,p.plan_version)=(r.project_id,r.plan_version) WHERE r.id=NEW.run_id) THEN
        RAISE EXCEPTION 'scoped preferences require an updated Worker';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER guard_scoped_preference_dispatch BEFORE INSERT OR UPDATE OF attempt_no ON invocations
FOR EACH ROW EXECUTE FUNCTION require_scoped_preference_worker();

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
         SELECT FROM plan_bound_preferences p JOIN preferences pref ON pref.id=p.preference_id AND pref.revision=p.revision
         WHERE(p.project_id,p.plan_version,p.preference_id,p.revision)=(r.project_id,r.plan_version,d.ref,d.revision)
           AND p.agent_scope IS NOT DISTINCT FROM pref.agent_scope AND (p.agent_scope IS NULL OR p.agent_scope=s.build_agent)
           AND pref.member_id=r.actor AND pref.enabled AND pref.deleted_at IS NULL AND NOT pref.departure_guard
           AND pref.body IS NOT NULL AND pref.expires_at>statement_timestamp())
     END
   )
 )
$$;

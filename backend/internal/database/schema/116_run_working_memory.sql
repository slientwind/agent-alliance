CREATE TABLE run_working_sets (
    run_id uuid NOT NULL REFERENCES runs ON DELETE CASCADE,
    ordinal integer NOT NULL CHECK (ordinal > 0),
    attempt_no integer NOT NULL CHECK (attempt_no > 0),
    plan_version integer NOT NULL,
    control_revision integer NOT NULL,
    message_through bigint NOT NULL DEFAULT 0,
    captured_at timestamptz NOT NULL DEFAULT now(),
    invalidated_at timestamptz,
    PRIMARY KEY (run_id,ordinal,attempt_no)
);

CREATE TABLE run_working_inputs (
    run_id uuid NOT NULL,
    ordinal integer NOT NULL,
    attempt_no integer NOT NULL,
    kind text NOT NULL CHECK (kind IN ('source','preference','rule')),
    ref uuid NOT NULL,
    revision integer NOT NULL,
    PRIMARY KEY(run_id,ordinal,attempt_no,kind,ref),
    FOREIGN KEY(run_id,ordinal,attempt_no) REFERENCES run_working_sets ON DELETE CASCADE
);
CREATE INDEX run_working_inputs_by_ref ON run_working_inputs(kind,ref,revision);

CREATE TABLE run_working_parents (
    run_id uuid NOT NULL,
    ordinal integer NOT NULL,
    attempt_no integer NOT NULL,
    parent_run uuid NOT NULL REFERENCES runs,
    parent_ordinal integer NOT NULL,
    parent_attempt integer NOT NULL,
    PRIMARY KEY(run_id,ordinal,attempt_no,parent_run,parent_ordinal),
    FOREIGN KEY(run_id,ordinal,attempt_no) REFERENCES run_working_sets ON DELETE CASCADE
);

CREATE FUNCTION run_memory_can_use_personal(run_key uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT FROM runs r JOIN members u ON u.id=r.actor
 WHERE r.id=run_key AND u.kind='person' AND u.enabled AND u.departed_at IS NULL
   AND (r.app_id IS NULL OR EXISTS(
       SELECT FROM delegations d JOIN applications a ON a.id=d.app_id
       JOIN members app ON app.id=a.id AND app.enabled
       WHERE d.id=r.delegated_by AND d.app_id=r.app_id AND d.member_id=r.actor
         AND a.revoked_at IS NULL AND a.expires_at>statement_timestamp()
         AND d.revoked_at IS NULL AND d.expires_at>statement_timestamp()
   )))
$$;

CREATE FUNCTION run_working_set_usable(run_key uuid, step_no integer, attempt integer) RETURNS boolean
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
   WHERE l.invalidated_at IS NOT NULL OR NOT EXISTS(
     SELECT FROM project_members pm JOIN members u ON u.id=pm.member_id AND u.enabled
     WHERE (pm.project_id,pm.member_id)=(r.project_id,r.actor))
   OR EXISTS(
     SELECT FROM run_working_inputs d WHERE(d.run_id,d.ordinal,d.attempt_no)=(l.run_id,l.ordinal,l.attempt_no)
     AND CASE d.kind
       WHEN 'source' THEN NOT EXISTS(
         SELECT FROM run_source_inputs f WHERE f.run_id=l.run_id AND f.source_id=d.ref AND f.version=d.revision
           AND (f.extracted IS NOT NULL OR f.picture_hash IS NOT NULL))
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

-- An accepted artifact edition follows its own access and retention rules.
-- Reusing a draft still depends on the working set that produced it.
CREATE FUNCTION working_artifact_origin(artifact_key uuid, edition integer)
RETURNS TABLE(run_id uuid,ordinal integer,attempt_no integer) LANGUAGE sql STABLE AS $$
 SELECT i.run_id,i.ordinal,i.attempt_no
 FROM invocations i JOIN (
   SELECT a.run_id,a.ordinal FROM artifacts a WHERE a.id=artifact_key AND edition=1
   UNION
   SELECT c.run_id,a.ordinal FROM change_batches c JOIN artifacts a ON a.id=c.artifact_id
   WHERE a.id=artifact_key AND c.applied_version=edition
 ) origin USING(run_id,ordinal)
 WHERE NOT EXISTS(SELECT FROM reviews v WHERE v.artifact_id=artifact_key AND v.version=edition)
$$;

CREATE FUNCTION invalidate_run_working_input() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE run_working_sets s SET invalidated_at=clock_timestamp()
    FROM run_working_inputs d
    WHERE (s.run_id,s.ordinal,s.attempt_no)=(d.run_id,d.ordinal,d.attempt_no)
      AND d.kind=TG_ARGV[0] AND d.ref=NEW.id AND s.invalidated_at IS NULL;
    RETURN NULL;
END $$;

CREATE TRIGGER invalidate_withdrawn_working_sources AFTER UPDATE OF revoked_at ON sources FOR EACH ROW
WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL)
EXECUTE FUNCTION invalidate_run_working_input('source');

CREATE TRIGGER invalidate_edited_working_preferences AFTER UPDATE ON preferences FOR EACH ROW
WHEN (OLD.revision IS DISTINCT FROM NEW.revision OR (OLD.enabled AND NOT NEW.enabled)
      OR (NOT OLD.departure_guard AND NEW.departure_guard)
      OR (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL))
EXECUTE FUNCTION invalidate_run_working_input('preference');

CREATE TRIGGER invalidate_disabled_working_rules AFTER UPDATE OF disabled ON team_rules FOR EACH ROW
WHEN (NOT OLD.disabled AND NEW.disabled)
EXECUTE FUNCTION invalidate_run_working_input('rule');

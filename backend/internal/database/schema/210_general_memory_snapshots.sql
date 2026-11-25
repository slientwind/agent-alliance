CREATE TABLE general_memory_snapshots (
 run_id uuid PRIMARY KEY REFERENCES general_agent_runs ON DELETE CASCADE,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ready')),
 cipher bytea,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((state='ready')=(cipher IS NOT NULL))
);
CREATE INDEX general_memory_jobs ON general_memory_snapshots(state,next_at,run_id);

CREATE TABLE general_memory_origins (
 run_id uuid NOT NULL REFERENCES general_memory_snapshots ON DELETE CASCADE,
 origin_run uuid NOT NULL REFERENCES runs ON DELETE CASCADE,
 artifact_id uuid NOT NULL,
 version integer NOT NULL,
 PRIMARY KEY(run_id,origin_run),
 FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions ON DELETE CASCADE
);

CREATE TABLE general_memory_dependencies (
 run_id uuid NOT NULL REFERENCES general_memory_snapshots ON DELETE CASCADE,
 kind text NOT NULL CHECK(kind IN ('source','preference','artifact')),
 ref uuid NOT NULL,
 PRIMARY KEY(run_id,kind,ref)
);
CREATE INDEX general_memory_dependencies_ref ON general_memory_dependencies(kind,ref,run_id);

-- Priors can feed a model again only under the current caller's exact access
-- and the source and rule pins of the current request.
CREATE FUNCTION general_memory_origin_readable(current_run uuid, historic_run uuid, file uuid, edition integer)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM runs active JOIN runs past ON past.project_id=active.project_id AND past.id=historic_run
   JOIN general_agent_runs g ON g.run_id=past.id
   JOIN artifacts a ON (a.id,a.run_id,a.current_version)=(file,past.id,edition)
   JOIN members m ON m.id=active.actor AND m.enabled
   JOIN project_members pm ON (pm.project_id,pm.member_id)=(active.project_id,m.id)
  WHERE active.id=current_run AND past.status='completed' AND run_review_notes_readable(past.id,m.id)
   AND distribution_readable(a.id,edition,m.id)
   AND general_mcp_observations_usable(past.id,past.charge_team)
   AND general_external_observations_usable(past.id)
   AND general_ecosystem_skills_usable(past.id)
   AND (g.studio_release IS NULL OR studio_release_usable(g.studio_release,m.id,active.project_id))
   AND (past.charge_team=active.charge_team OR NOT EXISTS(
     SELECT FROM general_agent_actions ga WHERE ga.run_id=past.id AND ga.mcp_server IS NOT NULL AND ga.state='completed'))
   AND NOT EXISTS(
     SELECT FROM plan_sources p JOIN sources s ON s.id=p.source_id
     WHERE (p.project_id,p.plan_version)=(past.project_id,past.plan_version)
      AND (s.project_id<>active.project_id OR s.revoked_at IS NOT NULL OR s.current_version<>p.source_version
       OR NOT EXISTS(SELECT FROM plan_sources pinned WHERE
       (pinned.project_id,pinned.plan_version,pinned.source_id,pinned.source_version)=(active.project_id,active.plan_version,p.source_id,p.source_version))))
   AND NOT EXISTS(
     SELECT FROM plan_bound_preferences p JOIN preferences f ON f.id=p.preference_id
     WHERE (p.project_id,p.plan_version)=(past.project_id,past.plan_version)
      AND (f.member_id<>m.id OR f.revision<>p.revision OR NOT f.enabled OR f.deleted_at IS NOT NULL OR f.departure_guard
       OR f.body IS NULL OR f.expires_at<=statement_timestamp()
       OR NOT EXISTS(SELECT FROM plan_bound_preferences current WHERE
         (current.project_id,current.plan_version,current.preference_id,current.revision)=(active.project_id,active.plan_version,p.preference_id,p.revision))))
   AND NOT EXISTS(
     SELECT FROM plan_rules p
     WHERE (p.project_id,p.plan_version)=(past.project_id,past.plan_version)
      AND (NOT rule_usable(p.rule_id,p.rule_revision,m.id)
       OR NOT EXISTS(SELECT FROM plan_rules current WHERE
         (current.project_id,current.plan_version,current.rule_id,current.rule_revision)=(active.project_id,active.plan_version,p.rule_id,p.rule_revision))))
 );
$$;

CREATE FUNCTION invalidate_general_memory() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE input_kind text;
BEGIN
 input_kind := CASE TG_TABLE_NAME WHEN 'sources' THEN 'source' WHEN 'preferences' THEN 'preference' ELSE 'artifact' END;
 UPDATE general_memory_snapshots s SET state='pending',cipher=null,next_at=clock_timestamp()
 WHERE s.state='ready' AND EXISTS(SELECT FROM general_memory_dependencies d WHERE (d.kind,d.ref,d.run_id)=(input_kind,NEW.id,s.run_id));
 RETURN NULL;
END
$$;

CREATE TRIGGER general_memory_source_changed AFTER UPDATE OF current_version,revoked_at,project_id ON sources
 FOR EACH ROW WHEN ((OLD.current_version,OLD.revoked_at,OLD.project_id) IS DISTINCT FROM (NEW.current_version,NEW.revoked_at,NEW.project_id))
 EXECUTE FUNCTION invalidate_general_memory();
CREATE TRIGGER general_memory_preference_changed AFTER UPDATE OF revision,enabled,deleted_at,departure_guard,expires_at ON preferences
 FOR EACH ROW WHEN ((OLD.revision,OLD.enabled,OLD.deleted_at,OLD.departure_guard,OLD.expires_at)
  IS DISTINCT FROM (NEW.revision,NEW.enabled,NEW.deleted_at,NEW.departure_guard,NEW.expires_at))
 EXECUTE FUNCTION invalidate_general_memory();
CREATE TRIGGER general_memory_artifact_changed AFTER UPDATE OF current_version ON artifacts
 FOR EACH ROW WHEN (OLD.current_version IS DISTINCT FROM NEW.current_version)
 EXECUTE FUNCTION invalidate_general_memory();

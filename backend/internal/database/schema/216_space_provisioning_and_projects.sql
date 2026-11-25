INSERT INTO spaces(kind,slug,name) VALUES('commons','commons','全企公共底座')
ON CONFLICT DO NOTHING;

-- Keep one identity from onboarding through disable/reactivate. Departed
-- employees retain a private namespace for the handover/retention policy.
INSERT INTO spaces(kind,owner_member_id,slug,name)
SELECT 'personal',id,'personal-'||id,'个人空间' FROM members WHERE kind='person'
ON CONFLICT DO NOTHING;
INSERT INTO spaces(kind,team_id,slug,name)
SELECT 'team',id,'team-'||id,left(name,120) FROM teams
ON CONFLICT DO NOTHING;

CREATE FUNCTION provision_member_space() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.kind='person' AND NEW.enabled AND NEW.departed_at IS NULL THEN
  INSERT INTO spaces(kind,owner_member_id,slug,name)
   VALUES('personal',NEW.id,'personal-'||NEW.id,'个人空间') ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER members_provision_space AFTER INSERT OR UPDATE OF kind,enabled,departed_at ON members
 FOR EACH ROW EXECUTE FUNCTION provision_member_space();

CREATE FUNCTION provision_team_space() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO spaces(kind,team_id,slug,name)
  VALUES('team',NEW.id,'team-'||NEW.id,left(NEW.name,120)) ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
CREATE TRIGGER teams_provision_space AFTER INSERT ON teams FOR EACH ROW EXECUTE FUNCTION provision_team_space();

WITH scope AS (
 SELECT s.id,tm.member_id,m.role,t.budget_owner,
  row_number() over(partition by s.id order by (t.budget_owner=m.id) desc nulls last,(m.role='admin') desc,m.created_at,m.id) succession
 FROM spaces s JOIN teams t ON t.id=s.team_id JOIN team_members tm ON t.id=tm.team_id
 JOIN members m ON m.id=tm.member_id AND m.kind='person' AND m.enabled AND m.departed_at IS NULL
 WHERE s.kind='team'
)
INSERT INTO space_members(space_id,member_id,role)
SELECT id,member_id,CASE WHEN succession=1 OR role='admin' OR budget_owner=member_id THEN 'space_admin' ELSE 'member' END FROM scope
ON CONFLICT(space_id,member_id) DO UPDATE SET role=CASE WHEN space_members.role='guest' THEN excluded.role ELSE space_members.role END;

CREATE FUNCTION synchronize_team_space_member() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  target uuid;
  next_role text;
BEGIN
 IF TG_OP='DELETE' THEN
  DELETE FROM space_members sm USING spaces s WHERE s.id=sm.space_id AND s.kind='team'
   AND s.team_id=OLD.team_id AND sm.member_id=OLD.member_id;
  RETURN OLD;
 END IF;
 SELECT id INTO target FROM spaces WHERE kind='team' AND team_id=NEW.team_id FOR UPDATE;
 SELECT CASE WHEN m.role='admin' OR NOT EXISTS(SELECT FROM space_members sm
  JOIN members admin ON admin.id=sm.member_id AND admin.enabled AND admin.departed_at IS NULL
  WHERE sm.space_id=target AND sm.role='space_admin') THEN 'space_admin' ELSE 'member' END
 INTO next_role FROM members m WHERE m.id=NEW.member_id AND m.kind='person' AND m.enabled AND m.departed_at IS NULL;
 IF target IS NOT NULL AND next_role IS NOT NULL THEN
  INSERT INTO space_members(space_id,member_id,role) VALUES(target,NEW.member_id,next_role)
  ON CONFLICT(space_id,member_id) DO UPDATE SET role=excluded.role WHERE space_members.role='guest';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER team_members_sync_space AFTER INSERT OR DELETE ON team_members
 FOR EACH ROW EXECUTE FUNCTION synchronize_team_space_member();

ALTER TABLE projects ADD COLUMN space_id uuid REFERENCES spaces(id);
-- Put previously shared conversations and application workflows in their
-- team space. A legacy conversation with one participant and one responsible
-- person can use that member's private space without exposing a prior draft.
UPDATE projects p SET space_id=s.id
FROM spaces s WHERE s.kind='personal' AND s.owner_member_id=p.owner AND p.owner=p.final_reviewer
 AND p.reviewer_assignment IS NULL
 AND EXISTS(SELECT FROM project_memberships pm WHERE pm.project_id=p.id AND pm.member_id=p.owner)
 AND NOT EXISTS(SELECT FROM project_memberships pm WHERE pm.project_id=p.id AND pm.member_id<>p.owner)
 AND NOT EXISTS(SELECT FROM reviewer_handoffs h WHERE h.project_id=p.id)
 AND NOT EXISTS(SELECT FROM enterprise_knowledge_sources k WHERE k.project_id=p.id)
 AND NOT EXISTS(SELECT FROM runs r WHERE r.project_id=p.id AND (r.service_id IS NOT NULL OR r.app_id IS NOT NULL));
UPDATE projects p SET space_id=s.id FROM spaces s
WHERE p.space_id IS NULL AND s.kind='team' AND p.team_id=s.team_id;
ALTER TABLE projects ALTER COLUMN space_id SET NOT NULL;
CREATE INDEX projects_by_space ON projects(space_id,updated_at DESC,id);

CREATE FUNCTION project_space_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  selected spaces%ROWTYPE;
BEGIN
 IF TG_OP='UPDATE' AND NEW.space_id IS DISTINCT FROM OLD.space_id THEN
  RAISE EXCEPTION 'project space is immutable' USING ERRCODE='23514';
 END IF;
 IF NEW.space_id IS NULL THEN
  SELECT id INTO NEW.space_id FROM spaces WHERE team_id=NEW.team_id AND kind='team';
 END IF;
 SELECT * INTO STRICT selected FROM spaces WHERE id=NEW.space_id;
 IF selected.kind='commons' OR
    (selected.kind='team' AND NEW.team_id IS DISTINCT FROM selected.team_id) OR
    (selected.kind='personal' AND (NEW.owner IS DISTINCT FROM selected.owner_member_id OR
     NEW.final_reviewer IS DISTINCT FROM selected.owner_member_id OR NEW.reviewer_assignment IS NOT NULL)) THEN
  RAISE EXCEPTION 'project does not satisfy space scope' USING ERRCODE='23514';
 END IF;
 IF TG_OP='INSERT' AND selected.archived_at IS NOT NULL THEN
  RAISE EXCEPTION 'project space is archived' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER projects_space_guard BEFORE INSERT OR UPDATE OF space_id,team_id,owner,final_reviewer,reviewer_assignment ON projects
 FOR EACH ROW EXECUTE FUNCTION project_space_guard();

INSERT INTO space_members(space_id,member_id,role)
SELECT DISTINCT p.space_id,pm.member_id,'guest'
FROM project_memberships pm JOIN projects p ON p.id=pm.project_id JOIN spaces s ON s.id=p.space_id AND s.kind='team'
JOIN members m ON m.id=pm.member_id AND m.kind='person' AND m.enabled AND m.departed_at IS NULL
ON CONFLICT DO NOTHING;

CREATE FUNCTION project_member_space_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  selected spaces%ROWTYPE;
BEGIN
 SELECT s.* INTO STRICT selected FROM projects p JOIN spaces s ON s.id=p.space_id WHERE p.id=NEW.project_id;
 IF selected.kind='personal' AND (NEW.member_id IS DISTINCT FROM selected.owner_member_id OR NEW.recovery_assignment IS NOT NULL) THEN
  RAISE EXCEPTION 'personal project is private' USING ERRCODE='23514';
 END IF;
 IF selected.kind='team' AND EXISTS(SELECT FROM members m
  WHERE m.id=NEW.member_id AND m.kind='person' AND m.enabled AND m.departed_at IS NULL) THEN
  INSERT INTO space_members(space_id,member_id,role) VALUES(selected.id,NEW.member_id,'guest')
  ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER project_members_scope BEFORE INSERT OR UPDATE OF project_id,member_id,recovery_assignment ON project_memberships
 FOR EACH ROW EXECUTE FUNCTION project_member_space_guard();

CREATE FUNCTION release_unused_project_guest() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 DELETE FROM space_members sm USING projects p WHERE p.id=OLD.project_id AND sm.space_id=p.space_id
  AND sm.member_id=OLD.member_id AND sm.role='guest' AND sm.granted_by IS NULL
  AND NOT EXISTS(SELECT FROM project_memberships pm JOIN projects other ON other.id=pm.project_id
    WHERE other.space_id=sm.space_id AND pm.member_id=sm.member_id);
 RETURN OLD;
END $$;
CREATE TRIGGER project_members_release_guest AFTER DELETE ON project_memberships
 FOR EACH ROW EXECUTE FUNCTION release_unused_project_guest();

CREATE FUNCTION project_space_member(target uuid, actor uuid) RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS(
  SELECT FROM projects p JOIN spaces s ON s.id=p.space_id AND s.archived_at IS NULL
  WHERE p.id=$1 AND (space_role(s.id,$2) IS NOT NULL OR (s.kind='team' AND EXISTS(
   SELECT FROM members m JOIN team_members tm ON tm.member_id=m.id AND tm.team_id=s.team_id
   WHERE m.id=$2 AND m.enabled AND m.kind='application'
  )))
 )
$$;

CREATE FUNCTION notify_space_membership_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 target uuid;
 actor uuid;
BEGIN
 target:=CASE WHEN TG_OP='DELETE' THEN OLD.space_id ELSE NEW.space_id END;
 actor:=CASE WHEN TG_OP='DELETE' THEN OLD.member_id ELSE NEW.member_id END;
 PERFORM pg_notify('alliance_project_updates',p.id::text)
 FROM projects p JOIN project_memberships pm ON pm.project_id=p.id AND pm.member_id=actor
 WHERE p.space_id=target;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER space_members_notify_projects AFTER INSERT OR UPDATE OR DELETE ON space_members
 FOR EACH ROW EXECUTE FUNCTION notify_space_membership_change();

CREATE FUNCTION notify_space_archive_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_notify('alliance_project_updates',p.id::text) FROM projects p WHERE p.space_id=NEW.id;
 RETURN NEW;
END $$;
CREATE TRIGGER spaces_notify_archive AFTER UPDATE OF archived_at ON spaces FOR EACH ROW
 WHEN (NEW.archived_at IS DISTINCT FROM OLD.archived_at) EXECUTE FUNCTION notify_space_archive_change();

CREATE FUNCTION personal_project(target uuid) RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS(SELECT FROM projects p JOIN spaces s ON s.id=p.space_id WHERE p.id=$1 AND s.kind='personal')
$$;

-- The existing view remains updatable. Its predicate protects all read
-- paths that already consume project membership, including original files.
CREATE OR REPLACE VIEW project_members AS
SELECT pm.project_id,pm.member_id,pm.can_review,pm.can_view_costs,pm.recovery_assignment
FROM project_memberships pm
WHERE project_space_member(pm.project_id,pm.member_id)
AND (pm.recovery_assignment IS NULL OR EXISTS(
 SELECT FROM reviewer_handoffs h JOIN projects p ON p.id=h.project_id AND p.reviewer_assignment=h.id
 WHERE h.id=pm.recovery_assignment AND h.project_id=pm.project_id AND h.successor=pm.member_id
 AND h.state='accepted' AND h.access_ended_at IS NULL AND h.recovery_until>statement_timestamp()
 AND p.stage NOT IN ('delivered','adopted','cancelled','failed')
));

-- Normal administrative recovery and the existing departure transfer may only
-- operate on team projects. Personal project ownership remains immutable.
CREATE OR REPLACE FUNCTION staff_departure_projects(leaving uuid) RETURNS TABLE(project_id uuid, team_id uuid)
 LANGUAGE sql STABLE AS $$
 WITH delivery AS (
  SELECT DISTINCT ON (q.project_id,q.plan_version,q.item)
   q.project_id,q.plan_version,q.state,q.responsible
  FROM delivery_item_assessments q JOIN projects p ON (p.id,p.plan_version)=(q.project_id,q.plan_version)
  ORDER BY q.project_id,q.plan_version,q.item,q.id DESC
 ), targets AS (
  SELECT p.id FROM projects p WHERE p.stage NOT IN ('delivered','adopted','cancelled','failed')
   AND leaving IN (p.owner,p.final_reviewer)
  UNION
  SELECT q.project_id FROM review_actions q WHERE leaving=any(q.recipients)
  UNION
  SELECT a.project_id FROM artifacts a JOIN feedback f ON f.artifact_id=a.id
   WHERE f.reviewer=leaving AND f.state IN ('suggested','required','investigate','verification')
  UNION
  SELECT a.project_id FROM artifact_comments c JOIN artifacts a ON a.id=c.artifact_id
   JOIN artifact_comment_mentions m ON m.comment_id=c.id AND m.member_id=leaving AND m.transferred_at IS NULL
   WHERE c.resolved_at IS NULL AND artifact_comment_readable(c.id,leaving)
  UNION
  SELECT q.project_id FROM delivery q WHERE q.state='missing' AND q.responsible=leaving
 )
 SELECT p.id,p.team_id FROM projects p JOIN targets t ON t.id=p.id
 JOIN spaces s ON s.id=p.space_id AND s.kind='team'
$$;

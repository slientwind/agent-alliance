ALTER TABLE projects ADD COLUMN privacy_state text NOT NULL DEFAULT 'active' CHECK(privacy_state IN ('active','sealed_private'));
ALTER TABLE kb_collections ADD COLUMN privacy_state text NOT NULL DEFAULT 'active' CHECK(privacy_state IN ('active','sealed_private')),
 ADD CONSTRAINT kb_sealed_disabled CHECK(privacy_state='active' OR NOT enabled);
ALTER TABLE studio_drafts ADD COLUMN privacy_state text NOT NULL DEFAULT 'active' CHECK(privacy_state IN ('active','sealed_private')),
 ADD COLUMN shared_handoff_revision int NOT NULL DEFAULT 0 CHECK(shared_handoff_revision BETWEEN 0 AND revision);

CREATE TABLE studio_sealed_versions (
 draft_id uuid NOT NULL REFERENCES studio_drafts(id),
 revision int NOT NULL,
 owner uuid NOT NULL REFERENCES members(id),
 spec jsonb NOT NULL,
 sealed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(draft_id,revision)
);

CREATE FUNCTION studio_draft_shared(target uuid) RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS(SELECT FROM studio_releases r WHERE r.draft_id=$1 AND r.agent_scope='team' AND r.state='published')
 OR EXISTS(SELECT FROM space_publication_drafts where draft_id=$1);
$$;

CREATE OR REPLACE FUNCTION studio_release_usable(release_id uuid, actor uuid, project uuid DEFAULT NULL) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM studio_releases r JOIN studio_drafts d ON d.id=r.draft_id
  JOIN members m ON m.id=actor AND m.enabled AND m.departed_at IS NULL AND m.kind='person'
  WHERE r.id=release_id AND r.state='published' AND (
   r.agent_scope='enterprise' OR (
    (r.agent_scope='team' OR (r.owner=m.id AND d.owner=m.id AND d.privacy_state='active'))
    AND EXISTS(SELECT FROM team_members tm WHERE (tm.team_id,tm.member_id)=(r.team_id,m.id))
    AND (project IS NULL OR EXISTS(SELECT FROM projects p WHERE (p.id,p.team_id)=(project,r.team_id)
     AND p.privacy_state='active'))
   )
  )
 );
$$;

CREATE OR REPLACE FUNCTION project_space_member(target uuid, actor uuid) RETURNS boolean LANGUAGE sql STABLE STRICT AS $$
 SELECT EXISTS(
  SELECT FROM projects p JOIN spaces s ON s.id=p.space_id AND s.archived_at IS NULL
  WHERE p.id=$1 AND p.privacy_state='active' AND (space_role(s.id,$2) IS NOT NULL OR (s.kind='team' AND EXISTS(
   SELECT FROM members m JOIN team_members tm ON tm.member_id=m.id AND tm.team_id=s.team_id
   WHERE m.id=$2 AND m.enabled AND m.kind='application'
  )))
 )
$$;

CREATE FUNCTION staff_shared_asset_teams(leaving uuid) RETURNS TABLE(team_id uuid) LANGUAGE sql STABLE AS $$
 SELECT s.team_id FROM kb_collections c JOIN spaces s ON s.id=c.space_id AND s.kind='team' WHERE c.maintainer=leaving
 UNION SELECT s.team_id FROM kb_documents d JOIN spaces s ON s.id=d.space_id AND s.kind='team' WHERE d.maintainer=leaving
 UNION SELECT s.team_id FROM business_connector_bindings b JOIN spaces s ON s.id=b.space_id AND s.kind='team' WHERE b.owner_id=leaving
 UNION SELECT s.team_id FROM business_connectors c JOIN spaces s ON s.id=c.space_id AND s.kind='team' WHERE c.maintainer=leaving
 UNION SELECT s.team_id FROM business_mutation_policies c JOIN spaces s ON s.id=c.space_id AND s.kind='team' WHERE c.maintainer=leaving
 UNION SELECT s.team_id FROM event_triggers t JOIN spaces s ON s.id=t.space_id AND s.kind='team' WHERE t.maintainer=leaving
 UNION SELECT s.team_id FROM pending_system_mutations a JOIN spaces s ON s.id=a.space_id AND s.kind='team'
  WHERE a.approver_id=leaving AND a.status IN ('pending','approved')
$$;

CREATE OR REPLACE FUNCTION staff_departure_teams(leaving uuid) RETURNS TABLE(team_id uuid) LANGUAGE sql STABLE AS $$
 SELECT p.team_id FROM staff_departure_projects(leaving) p
 UNION SELECT a.team_id FROM agents a WHERE a.owner=leaving AND NOT a.removed
 UNION SELECT r.team_id FROM team_rules r WHERE r.maintainer=leaving
 UNION SELECT d.team_id FROM studio_drafts d WHERE d.owner=leaving AND d.privacy_state='active' AND studio_draft_shared(d.id)
 UNION SELECT a.team_id FROM automation_schedules a WHERE a.owner=leaving AND a.state<>'deleted'
 UNION SELECT t.team_id FROM staff_shared_asset_teams(leaving) t
$$;

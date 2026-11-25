ALTER TABLE artifact_comment_mentions ADD COLUMN transferred_at timestamptz;

CREATE TABLE staff_departure_batches (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 departing uuid NOT NULL REFERENCES members,
 successor uuid NOT NULL REFERENCES members,
 actor uuid NOT NULL REFERENCES members,
 reason text NOT NULL,
 transferred jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(departing<>successor)
);

CREATE FUNCTION staff_departure_projects(leaving uuid) RETURNS TABLE(project_id uuid, team_id uuid)
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
$$;

CREATE FUNCTION staff_departure_teams(leaving uuid) RETURNS TABLE(team_id uuid) LANGUAGE sql STABLE AS $$
 SELECT p.team_id FROM staff_departure_projects(leaving) p
 UNION SELECT a.team_id FROM agents a WHERE a.owner=leaving AND NOT a.removed
 UNION SELECT r.team_id FROM team_rules r WHERE r.maintainer=leaving
 UNION SELECT d.team_id FROM studio_drafts d WHERE d.owner=leaving
 UNION SELECT a.team_id FROM automation_schedules a WHERE a.owner=leaving AND a.state<>'deleted'
$$;

-- Keep the recorded publisher immutable. Access to a personal publication
-- follows the current maintainer of the original draft.
CREATE OR REPLACE FUNCTION studio_release_usable(release_id uuid, actor uuid, project uuid DEFAULT NULL) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM studio_releases r JOIN studio_drafts d ON d.id=r.draft_id
   JOIN members m ON m.id=actor AND m.enabled AND m.kind='person'
  WHERE r.id=release_id AND r.state='published' AND (
   r.agent_scope='enterprise' OR (
    (r.agent_scope='team' OR d.owner=m.id)
    AND EXISTS(SELECT FROM team_members tm WHERE (tm.team_id,tm.member_id)=(r.team_id,m.id))
    AND (project IS NULL OR EXISTS(SELECT FROM projects p WHERE (p.id,p.team_id)=(project,r.team_id)))
   )
  )
 );
$$;

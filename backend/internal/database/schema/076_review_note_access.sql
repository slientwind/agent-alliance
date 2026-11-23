-- Distribution approval covers the delivered version. Working notes may also
-- quote withdrawn inputs and are evaluated against the current project duties.
CREATE FUNCTION run_review_notes_readable(target_run uuid, viewer uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS (
   SELECT FROM runs r JOIN projects p ON p.id=r.project_id
   JOIN project_members m ON m.project_id=p.id AND m.member_id=viewer
   JOIN members u ON u.id=m.member_id AND u.enabled
   WHERE r.id=target_run AND (m.can_review OR p.owner=viewer OR NOT EXISTS (
     SELECT FROM plan_sources ps JOIN sources s ON s.id=ps.source_id
     WHERE (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
     AND s.revoked_at IS NOT NULL AND s.created_by<>viewer
   ))
 )
$$;

-- The same instruction may be copied to several targets. Access to its text
-- requires access to every target of that item, including historical runs.
CREATE FUNCTION feedback_notes_readable(item feedback, viewer uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT EXISTS (
   SELECT FROM artifacts a WHERE a.id=item.artifact_id AND run_review_notes_readable(a.run_id,viewer)
 ) AND NOT EXISTS (
   SELECT FROM feedback f JOIN artifacts a ON a.id=f.artifact_id
   WHERE (f.submission_id,f.submission_item)=(item.submission_id,item.submission_item)
   AND f.artifact_id<>item.artifact_id AND NOT run_review_notes_readable(a.run_id,viewer)
 )
$$;

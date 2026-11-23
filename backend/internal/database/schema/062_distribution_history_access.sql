-- The version browser can select superseded working drafts as well as delivered
-- versions. Check each withdrawn run input even after its candidate is replaced.
CREATE OR REPLACE FUNCTION distribution_readable(target uuid, ver integer, member uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(
 SELECT FROM artifacts a JOIN runs r ON r.id=a.run_id JOIN projects p ON p.id=a.project_id
 JOIN project_members m ON m.project_id=p.id AND m.member_id=member
 JOIN plan_sources ps ON (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
 JOIN withdrawn_delivery_sources s ON s.id=ps.source_id
 LEFT JOIN LATERAL(SELECT decision FROM distribution_reviews v
 WHERE(v.source_id,v.artifact_id,v.version)=(s.id,a.id,ver) ORDER BY revision DESC LIMIT 1) latest ON true
 WHERE a.id=target AND NOT m.can_review AND member NOT IN(p.owner,s.created_by) AND latest.decision IS DISTINCT FROM 'reference')
$$;

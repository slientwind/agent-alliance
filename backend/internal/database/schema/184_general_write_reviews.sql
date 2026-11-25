CREATE FUNCTION general_tool_approver(target_run uuid, target_level text, member uuid) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM runs r
   JOIN projects p ON (p.id,p.plan_version)=(r.project_id,r.plan_version)
   JOIN members u ON u.id=$3 AND u.enabled AND u.kind='person'
   JOIN team_members tm ON (tm.team_id,tm.member_id)=(r.charge_team,u.id)
   JOIN project_members pm ON (pm.project_id,pm.member_id)=(p.id,u.id)
  WHERE r.id=$1 AND (($2='L1' AND r.actor=u.id) OR ($2='L2' AND p.final_reviewer=u.id AND pm.can_review))
 );
$$;

CREATE FUNCTION agent_admissible(target uuid, rev integer, actor uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM agents a JOIN agent_revisions r ON r.agent_id=a.id
  JOIN team_members t ON t.team_id=a.team_id JOIN members u ON u.id=t.member_id
  LEFT JOIN model_resources m ON m.id=r.resource_id
  WHERE a.id=target AND r.revision=rev AND u.id=actor AND u.enabled
   AND NOT a.disabled AND NOT a.emergency AND NOT a.removed
   AND (r.resource_id IS NULL OR (m.team_id=a.team_id AND m.enabled AND NOT m.emergency))
   AND agent_verified(target,rev)
 )
$$;

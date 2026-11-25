CREATE FUNCTION general_run_inputs_usable(target uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce((select current.plan_version=r.plan_version
  and exists(select from members m join project_members pm on pm.member_id=m.id and pm.project_id=r.project_id
    join team_members tm on tm.member_id=m.id and tm.team_id=r.charge_team where m.id=r.actor and m.enabled)
  and not exists(select from plan_sources ps join sources s on s.id=ps.source_id
    where (ps.project_id,ps.plan_version)=(r.project_id,r.plan_version)
     and (s.project_id<>r.project_id or s.revoked_at is not null or s.current_version<>ps.source_version))
  and not exists(select from plan_rules pr where (pr.project_id,pr.plan_version)=(r.project_id,r.plan_version)
    and not rule_usable(pr.rule_id,pr.rule_revision,r.actor))
  and general_mcp_observations_usable(r.id,r.charge_team) and general_external_observations_usable(r.id)
  and general_ecosystem_skills_usable(r.id) and (g.studio_release is null or studio_release_usable(g.studio_release,r.actor,r.project_id))
  from general_agent_runs g join runs r on r.id=g.run_id join projects current on current.id=r.project_id where r.id=target),false);
$$;

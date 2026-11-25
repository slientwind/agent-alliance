CREATE TABLE general_agent_web_search (
 run_id uuid PRIMARY KEY REFERENCES general_agent_runs ON DELETE CASCADE,
 service_revision integer NOT NULL,
 query_cipher bytea NOT NULL
);

CREATE FUNCTION general_web_search_usable(target_run uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM general_agent_web_search w
   JOIN web_search_integration s ON s.revision=w.service_revision AND s.enabled
   JOIN runs r ON r.id=w.run_id AND r.status='running'
   JOIN projects p ON (p.id,p.plan_version)=(r.project_id,r.plan_version)
   JOIN members u ON u.id=r.actor AND u.enabled AND u.kind='person'
   JOIN project_members pm ON (pm.project_id,pm.member_id)=(p.id,u.id)
   JOIN team_members tm ON (tm.team_id,tm.member_id)=(r.charge_team,u.id)
  WHERE w.run_id=$1
 );
$$;

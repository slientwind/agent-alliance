CREATE TABLE mcp_team_grants (
 server_id uuid NOT NULL REFERENCES mcp_servers,
 team_id uuid NOT NULL REFERENCES teams,
 enabled boolean NOT NULL,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 PRIMARY KEY(server_id,team_id)
);
CREATE INDEX mcp_team_grants_by_team ON mcp_team_grants(team_id,server_id) WHERE enabled;

CREATE TABLE general_agent_mcp_servers (
 run_id uuid NOT NULL REFERENCES general_agent_runs ON DELETE CASCADE,
 server_id uuid NOT NULL REFERENCES mcp_servers,
 server_revision integer NOT NULL,
 grant_revision integer NOT NULL,
 PRIMARY KEY(run_id,server_id)
);

ALTER TABLE general_agent_actions ADD COLUMN mcp_server uuid REFERENCES mcp_servers,
 ADD COLUMN mcp_tool_digest text,
 ADD CHECK((mcp_server IS NULL)=(mcp_tool_digest IS NULL));

CREATE FUNCTION general_mcp_observations_usable(run_id uuid, team_id uuid) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(
  SELECT FROM general_agent_actions a
   JOIN general_agent_mcp_servers binding ON (binding.run_id,binding.server_id)=(a.run_id,a.mcp_server)
   LEFT JOIN mcp_servers s ON (s.id,s.revision)=(binding.server_id,binding.server_revision) AND s.enabled
   LEFT JOIN mcp_team_grants g ON (g.server_id,g.team_id,g.revision)=(binding.server_id,$2,binding.grant_revision) AND g.enabled
  WHERE a.run_id=$1 AND a.state='completed' AND (s.id IS NULL OR g.team_id IS NULL)
 );
$$;

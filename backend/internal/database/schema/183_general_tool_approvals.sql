ALTER TABLE general_agent_actions
 ADD COLUMN mcp_level text CHECK(mcp_level IN ('L0','L1','L2')),
 ADD COLUMN mcp_tool_label text,
 ADD COLUMN mcp_recipient text;
UPDATE general_agent_actions SET mcp_level='L0' WHERE mcp_server IS NOT NULL;
ALTER TABLE general_agent_actions
 ADD CHECK((mcp_server IS NULL)=(mcp_level IS NULL));

CREATE TABLE general_agent_tool_approvals (
 id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
 run_id uuid NOT NULL,
 turn integer NOT NULL,
 position integer NOT NULL,
 level text NOT NULL CHECK(level IN ('L1','L2')),
 fingerprint text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','approved','denied')),
 revision integer NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),
 decided_by uuid REFERENCES members,
 decided_at timestamptz,
 next_wake_at timestamptz,
 wake_sent_at timestamptz,
 PRIMARY KEY(run_id,turn,position),
 FOREIGN KEY(run_id,turn,position) REFERENCES general_agent_actions ON DELETE CASCADE,
 CHECK((state='pending')=(decided_at IS NULL AND decided_by IS NULL)),
 CHECK((state='pending')=(next_wake_at IS NULL))
);
CREATE INDEX general_tool_approval_wake ON general_agent_tool_approvals(next_wake_at,id) WHERE wake_sent_at IS NULL;

CREATE FUNCTION general_mcp_binding_current(target_run uuid, target_server uuid) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM general_agent_mcp_servers b JOIN runs r ON r.id=b.run_id
   JOIN mcp_servers s ON (s.id,s.revision)=(b.server_id,b.server_revision) AND s.enabled
   JOIN mcp_team_grants g ON (g.server_id,g.team_id,g.revision)=(b.server_id,r.charge_team,b.grant_revision) AND g.enabled
  WHERE b.run_id=target_run AND b.server_id=target_server
 );
$$;

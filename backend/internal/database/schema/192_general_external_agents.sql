ALTER TABLE general_agent_runs ADD COLUMN direct_external boolean NOT NULL DEFAULT false;

CREATE TABLE general_external_agents (
 run_id uuid NOT NULL REFERENCES general_agent_runs ON DELETE CASCADE,
 agent_id uuid NOT NULL,
 revision integer NOT NULL,
 PRIMARY KEY(run_id,agent_id),
 FOREIGN KEY(agent_id,revision) REFERENCES external_agent_admissions
);

ALTER TABLE general_agent_actions
 ADD COLUMN external_agent uuid,
 ADD COLUMN external_label text,
 ADD COLUMN external_recipient text,
 ADD FOREIGN KEY(run_id,external_agent) REFERENCES general_external_agents(run_id,agent_id),
 ADD CHECK((external_agent IS NULL)=(external_label IS NULL) AND
           (external_agent IS NULL)=(external_recipient IS NULL) AND
           (external_agent IS NULL OR mcp_server IS NULL));

CREATE TABLE general_external_jobs (
 run_id uuid NOT NULL,
 turn integer NOT NULL,
 position integer NOT NULL,
 remote_receipt bytea NOT NULL,
 expires_at timestamptz NOT NULL,
 poll_count integer NOT NULL DEFAULT 0,
 poll_until timestamptz,
 PRIMARY KEY(run_id,turn,position),
 FOREIGN KEY(run_id,turn,position) REFERENCES general_agent_actions ON DELETE CASCADE
);

CREATE FUNCTION general_external_binding_current(target_run uuid, target_agent uuid) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
  SELECT FROM general_external_agents b JOIN runs r ON r.id=b.run_id
  WHERE b.run_id=target_run AND b.agent_id=target_agent AND
    external_admission_can_use(b.agent_id,b.revision,r.actor)
 );
$$;

CREATE FUNCTION general_external_observations_usable(target_run uuid) RETURNS boolean
 LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(
  SELECT FROM general_agent_actions a
  WHERE a.run_id=target_run AND a.external_agent IS NOT NULL AND a.state='completed'
   AND NOT general_external_binding_current(a.run_id,a.external_agent)
 );
$$;

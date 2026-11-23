CREATE TABLE model_resources (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL, team_id uuid NOT NULL REFERENCES teams,
 created_by uuid NOT NULL REFERENCES members, enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE model_revisions (
 resource_id uuid REFERENCES model_resources, revision integer NOT NULL,
 endpoint text NOT NULL, model text NOT NULL, encrypted_key bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(resource_id,revision)
);
CREATE TABLE agents (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), owner uuid NOT NULL REFERENCES members,
 team_id uuid NOT NULL REFERENCES teams, published_revision integer,
 disabled boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE agent_revisions (
 agent_id uuid REFERENCES agents, revision integer NOT NULL, name text NOT NULL, instructions text NOT NULL,
 resource_id uuid NOT NULL, resource_revision integer NOT NULL, key_override bytea,
 created_by uuid NOT NULL REFERENCES members, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(agent_id,revision), FOREIGN KEY(resource_id,resource_revision) REFERENCES model_revisions
);
ALTER TABLE agents ADD FOREIGN KEY(id,published_revision) REFERENCES agent_revisions(agent_id,revision);
CREATE TABLE validations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), agent_id uuid NOT NULL, revision integer NOT NULL,
 actor uuid REFERENCES members, status text NOT NULL, evidence text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), ended_at timestamptz,
 FOREIGN KEY(agent_id,revision) REFERENCES agent_revisions
);

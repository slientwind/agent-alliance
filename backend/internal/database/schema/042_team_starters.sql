CREATE TABLE team_starters (
 team_id uuid NOT NULL REFERENCES teams, slot text NOT NULL CHECK(slot IN ('writer','checker','document')),
 agent_id uuid NOT NULL UNIQUE REFERENCES agents, template_version integer NOT NULL,
 PRIMARY KEY(team_id,slot)
);
CREATE TABLE starter_samples (
 project_id uuid PRIMARY KEY REFERENCES projects, team_id uuid NOT NULL REFERENCES teams,
 actor uuid NOT NULL REFERENCES members, request_key text NOT NULL, agents uuid[] NOT NULL, revisions integer[] NOT NULL,
 UNIQUE(actor,request_key)
);
CREATE INDEX starter_samples_team ON starter_samples(team_id);

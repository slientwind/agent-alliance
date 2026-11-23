CREATE TABLE projects (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), owner uuid NOT NULL REFERENCES members,
 team_id uuid NOT NULL REFERENCES teams, final_reviewer uuid NOT NULL REFERENCES members,
 title text NOT NULL, stage text NOT NULL DEFAULT 'brief', revision integer NOT NULL DEFAULT 1,
 plan_version integer NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE project_members (
 project_id uuid REFERENCES projects, member_id uuid REFERENCES members, can_review boolean NOT NULL DEFAULT false,
 PRIMARY KEY(project_id,member_id)
);
CREATE TABLE messages (
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, project_id uuid NOT NULL REFERENCES projects,
 author uuid REFERENCES members, kind text NOT NULL, body text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE plans (
 project_id uuid REFERENCES projects, version integer NOT NULL, brief text NOT NULL,
 confirmed_by uuid REFERENCES members, PRIMARY KEY(project_id,version)
);
CREATE TABLE nodes (
 project_id uuid NOT NULL,plan_version integer NOT NULL, ordinal integer NOT NULL,agent_id uuid NOT NULL,agent_revision integer NOT NULL,
 PRIMARY KEY(project_id,plan_version,ordinal), FOREIGN KEY(project_id,plan_version) REFERENCES plans,
 FOREIGN KEY(agent_id,agent_revision) REFERENCES agent_revisions
);
CREATE TABLE runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),project_id uuid NOT NULL,plan_version integer NOT NULL,
 actor uuid REFERENCES members NOT NULL, status text NOT NULL DEFAULT 'queued',charge_team uuid REFERENCES teams NOT NULL,
 request_key text NOT NULL, request_hash bytea NOT NULL, max_calls integer NOT NULL CHECK(max_calls>0),
 created_at timestamptz NOT NULL DEFAULT now(),FOREIGN KEY(project_id,plan_version) REFERENCES plans,
 UNIQUE(actor,request_key)
);
CREATE TABLE outbox (
 run_id uuid PRIMARY KEY REFERENCES runs, sent_at timestamptz, attempts integer NOT NULL DEFAULT 0, next_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE invocations (
 run_id uuid REFERENCES runs,ordinal integer NOT NULL, status text NOT NULL, tokens integer,
 cost_state text NOT NULL DEFAULT 'unknown', result text,diagnostic text,started_at timestamptz NOT NULL DEFAULT now(),ended_at timestamptz,
 PRIMARY KEY(run_id,ordinal)
);
CREATE TABLE artifacts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), project_id uuid REFERENCES projects NOT NULL, run_id uuid REFERENCES runs NOT NULL,
 ordinal integer NOT NULL, name text NOT NULL, current_version integer NOT NULL DEFAULT 1, UNIQUE(run_id,ordinal)
);
CREATE TABLE artifact_versions (
 artifact_id uuid REFERENCES artifacts, version integer NOT NULL, body text NOT NULL,
 reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(artifact_id,version)
);
CREATE TABLE reviews (
 artifact_id uuid NOT NULL, version integer NOT NULL, reviewer uuid REFERENCES members NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(artifact_id,version),FOREIGN KEY(artifact_id,version) REFERENCES artifact_versions
);
CREATE TABLE deliveries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), project_id uuid UNIQUE REFERENCES projects NOT NULL, confirmed_by uuid REFERENCES members NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE delivery_items (
 delivery_id uuid REFERENCES deliveries,artifact_id uuid NOT NULL,version integer NOT NULL,
 PRIMARY KEY(delivery_id,artifact_id),FOREIGN KEY(artifact_id,version) REFERENCES reviews
);
CREATE INDEX messages_project ON messages(project_id,seq);
CREATE INDEX runs_project ON runs(project_id,created_at);

CREATE TABLE workflow_templates (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 team_id uuid NOT NULL REFERENCES teams,
 owner uuid NOT NULL REFERENCES members,
 current_version integer NOT NULL DEFAULT 1 CHECK(current_version>0),
 published_version integer NOT NULL DEFAULT 0 CHECK(published_version BETWEEN 0 AND current_version),
 requested_version integer NOT NULL DEFAULT 0 CHECK(requested_version IN (0,current_version)),
 decision_reason text NOT NULL DEFAULT '',
 archived_at timestamptz,
 updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workflow_template_versions (
 template_id uuid NOT NULL REFERENCES workflow_templates,
 version integer NOT NULL CHECK(version>0),
 name text NOT NULL,
 instructions text NOT NULL,
 inputs text NOT NULL,
 outcomes text NOT NULL,
 requires_sources boolean NOT NULL,
 nodes jsonb NOT NULL CHECK(jsonb_typeof(nodes)='array' AND jsonb_array_length(nodes) BETWEEN 1 AND 12),
 origin_project uuid NOT NULL,
 origin_plan integer NOT NULL,
 author uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,
 request_hash bytea NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(template_id,version),
 UNIQUE(author,request_key),
 FOREIGN KEY(origin_project,origin_plan) REFERENCES plans(project_id,version)
);

CREATE TABLE workflow_template_uses (
 project_id uuid PRIMARY KEY REFERENCES projects,
 template_id uuid NOT NULL,
 version integer NOT NULL,
 actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,
 request_hash bytea NOT NULL,
 FOREIGN KEY(template_id,version) REFERENCES workflow_template_versions,
 UNIQUE(actor,request_key)
);

CREATE INDEX workflow_templates_by_team ON workflow_templates(team_id,updated_at DESC) WHERE archived_at IS NULL;

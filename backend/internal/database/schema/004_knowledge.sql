CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE objects (hash text PRIMARY KEY, size_bytes bigint NOT NULL CHECK(size_bytes>=0), created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE sources (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), project_id uuid NOT NULL REFERENCES projects,name text NOT NULL,
 current_version integer NOT NULL DEFAULT 1, revoked_at timestamptz, created_by uuid NOT NULL REFERENCES members
);
CREATE TABLE source_versions (
 source_id uuid REFERENCES sources,version integer NOT NULL, object_hash text REFERENCES objects NOT NULL,
 media_type text NOT NULL, extracted text, embedding vector,author uuid REFERENCES members NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(source_id,version)
);
CREATE TABLE plan_sources (
 project_id uuid NOT NULL,plan_version integer NOT NULL,source_id uuid NOT NULL,source_version integer NOT NULL,
 PRIMARY KEY(project_id,plan_version,source_id),FOREIGN KEY(project_id,plan_version) REFERENCES plans,FOREIGN KEY(source_id,source_version) REFERENCES source_versions
);
CREATE TABLE preferences (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), member_id uuid REFERENCES members NOT NULL, body text,
 revision integer NOT NULL DEFAULT 1, enabled boolean NOT NULL DEFAULT true,deleted_at timestamptz,
 review_at timestamptz NOT NULL DEFAULT now()+interval '90 days',expires_at timestamptz NOT NULL DEFAULT now()+interval '365 days'
);
CREATE TABLE plan_preferences (
 project_id uuid NOT NULL,plan_version integer NOT NULL,preference_id uuid NOT NULL REFERENCES preferences,revision integer NOT NULL,
 PRIMARY KEY(project_id,plan_version,preference_id),FOREIGN KEY(project_id,plan_version) REFERENCES plans
);

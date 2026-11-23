CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TABLE enterprise (
 id boolean PRIMARY KEY DEFAULT true CHECK (id), name text NOT NULL,
 default_team uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE members (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text NOT NULL UNIQUE,
 display_name text NOT NULL, password_hash text NOT NULL, role text NOT NULL CHECK (role IN ('admin','member')),
 enabled boolean NOT NULL DEFAULT true, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE teams (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL UNIQUE);
ALTER TABLE enterprise ADD FOREIGN KEY (default_team) REFERENCES teams(id);
CREATE TABLE team_members (
 team_id uuid REFERENCES teams NOT NULL, member_id uuid REFERENCES members NOT NULL,
 PRIMARY KEY(team_id,member_id)
);
CREATE TABLE sessions (
 token_hash bytea PRIMARY KEY, member_id uuid NOT NULL REFERENCES members, expires_at timestamptz NOT NULL
);
CREATE TABLE invites (
 token_hash bytea PRIMARY KEY, team_id uuid NOT NULL REFERENCES teams, issued_by uuid NOT NULL REFERENCES members,
 expires_at timestamptz NOT NULL, used_by uuid REFERENCES members
);
CREATE TABLE audit_events (
 seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, actor uuid REFERENCES members,
 action text NOT NULL, subject text NOT NULL, detail jsonb NOT NULL DEFAULT '{}', at timestamptz NOT NULL DEFAULT now()
);

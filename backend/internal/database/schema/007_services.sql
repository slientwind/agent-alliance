ALTER TABLE members ADD COLUMN kind text NOT NULL DEFAULT 'person' CHECK(kind IN('person','application'));
CREATE TABLE services(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),team_id uuid REFERENCES teams NOT NULL, name text NOT NULL,
 owner uuid REFERENCES members NOT NULL,enabled boolean NOT NULL DEFAULT true,current_version integer NOT NULL DEFAULT 1);
CREATE TABLE service_versions(service_id uuid REFERENCES services,version integer NOT NULL,instructions text NOT NULL,reviewer uuid REFERENCES members NOT NULL,
 published_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(service_id,version));
CREATE TABLE service_nodes(service_id uuid NOT NULL,version integer NOT NULL,ordinal integer NOT NULL,agent_id uuid NOT NULL,agent_revision integer NOT NULL,
 PRIMARY KEY(service_id,version,ordinal),FOREIGN KEY(service_id,version) REFERENCES service_versions,FOREIGN KEY(agent_id,agent_revision) REFERENCES agent_revisions);
CREATE TABLE applications(id uuid PRIMARY KEY REFERENCES members,service_id uuid REFERENCES services NOT NULL,token_hash bytea UNIQUE NOT NULL,expires_at timestamptz NOT NULL,revoked_at timestamptz);
CREATE TABLE delegations(id uuid PRIMARY KEY DEFAULT gen_random_uuid(),app_id uuid REFERENCES applications NOT NULL,member_id uuid REFERENCES members NOT NULL,
 token_hash bytea UNIQUE NOT NULL,expires_at timestamptz NOT NULL,revoked_at timestamptz);
ALTER TABLE runs ADD COLUMN app_id uuid REFERENCES applications,ADD COLUMN delegated_by uuid REFERENCES delegations,
 ADD COLUMN service_id uuid,ADD COLUMN service_version integer,ADD FOREIGN KEY(service_id,service_version) REFERENCES service_versions;

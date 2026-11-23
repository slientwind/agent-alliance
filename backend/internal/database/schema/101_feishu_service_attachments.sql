ALTER TABLE channel_proposals
 ADD COLUMN origin_message_id text NOT NULL DEFAULT '',
 ADD COLUMN origin_time bigint NOT NULL DEFAULT 0,
 ADD COLUMN file_message_id text NOT NULL DEFAULT '',
 ADD COLUMN file_key text NOT NULL DEFAULT '',
 ADD COLUMN file_name text NOT NULL DEFAULT '',
 ADD COLUMN file_time bigint NOT NULL DEFAULT 0;

ALTER TABLE channel_contexts ADD COLUMN changed_at timestamptz NOT NULL DEFAULT clock_timestamp();

CREATE TABLE channel_attachment_jobs (
 id uuid PRIMARY KEY,
 event_id text UNIQUE NOT NULL REFERENCES channel_events(id),
 tenant text NOT NULL,
 chat_id text NOT NULL,
 member_id uuid NOT NULL REFERENCES members(id),
 binding_id uuid NOT NULL,
 service_id uuid NOT NULL,
 version integer NOT NULL,
 calls integer NOT NULL CHECK (calls>0),
 input text NOT NULL,
 file_message_id text NOT NULL,
 file_key text NOT NULL,
 file_name text NOT NULL,
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL,
 next_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 FOREIGN KEY (service_id,version) REFERENCES service_versions(service_id,version)
);
CREATE INDEX channel_attachment_jobs_due ON channel_attachment_jobs (tenant,next_at);

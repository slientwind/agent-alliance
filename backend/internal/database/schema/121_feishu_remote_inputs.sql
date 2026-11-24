ALTER TABLE remote_task_inputs ADD COLUMN channel_binding_id uuid;

CREATE TABLE channel_remote_input_proposals (
  id uuid PRIMARY KEY,
  tenant text NOT NULL,
  chat_id text NOT NULL,
  member_id uuid NOT NULL,
  binding_id uuid NOT NULL,
  context_changed_at timestamptz NOT NULL,
  run_id uuid NOT NULL,
  ordinal integer NOT NULL,
  control_revision integer NOT NULL,
  input_revision integer NOT NULL CHECK(input_revision>0),
  origin_time bigint NOT NULL,
  encrypted_body bytea,
  input_id uuid REFERENCES remote_task_inputs,
  expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes',
  FOREIGN KEY(tenant,member_id) REFERENCES channel_links(tenant,member_id) ON DELETE CASCADE,
  FOREIGN KEY(run_id,ordinal) REFERENCES remote_jobs(run_id,ordinal),
  UNIQUE(tenant,chat_id,member_id),
  CHECK((input_id IS NULL)=(encrypted_body IS NOT NULL))
);
CREATE INDEX channel_remote_input_proposals_expiry ON channel_remote_input_proposals(expires_at);

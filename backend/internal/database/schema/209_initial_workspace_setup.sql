CREATE TABLE initial_workspace_setup (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton) REFERENCES enterprise(id),
 actor uuid NOT NULL REFERENCES members,
 request_key text NOT NULL,
 request_hash bytea NOT NULL,
 receipt_cipher bytea NOT NULL,
 completed_at timestamptz NOT NULL DEFAULT now()
);

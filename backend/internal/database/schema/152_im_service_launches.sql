CREATE TABLE im_service_launches (
    id uuid PRIMARY KEY,
    binding_id uuid NOT NULL REFERENCES im_bindings(id) ON DELETE CASCADE,
    chat_id text NOT NULL,
    thread_id text NOT NULL,
    service_id uuid NOT NULL,
    version integer NOT NULL,
    routing_revision integer NOT NULL,
    calls integer NOT NULL CHECK (calls > 0),
    goal bytea,
    expires_at timestamptz NOT NULL DEFAULT (clock_timestamp() + interval '10 minutes'),
    run_id uuid UNIQUE REFERENCES runs(id) ON DELETE CASCADE,
    accepted_at timestamptz,
    FOREIGN KEY (service_id, version) REFERENCES service_versions (service_id, version) ON DELETE CASCADE,
    CHECK ((run_id IS NULL AND accepted_at IS NULL AND goal IS NOT NULL)
        OR (run_id IS NOT NULL AND accepted_at IS NOT NULL AND goal IS NULL))
);

CREATE UNIQUE INDEX im_service_launch_pending ON im_service_launches (binding_id, chat_id, thread_id)
    WHERE run_id IS NULL;
CREATE INDEX im_service_launch_retention ON im_service_launches ((coalesce(accepted_at, expires_at)));

ALTER TABLE im_events ADD COLUMN reply_argument bytea;

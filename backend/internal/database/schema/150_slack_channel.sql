CREATE TABLE im_bindings (
    connection text NOT NULL,
    external_user text NOT NULL,
    member_id uuid NOT NULL REFERENCES members,
    id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    PRIMARY KEY (connection,external_user),
    UNIQUE (connection,member_id)
);

CREATE TABLE im_binding_codes (
    connection text NOT NULL,
    member_id uuid NOT NULL REFERENCES members,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (connection,member_id)
);

CREATE TABLE im_threads (
    binding_id uuid NOT NULL REFERENCES im_bindings(id) ON DELETE CASCADE,
    chat_id text NOT NULL,
    thread_id text NOT NULL,
    project_id uuid NOT NULL REFERENCES projects ON DELETE CASCADE,
    PRIMARY KEY (binding_id,chat_id,thread_id)
);

-- An incoming event and its reply intent share one durable lifecycle. A
-- missing binding is a fact at reception, even if the user links later.
CREATE TABLE im_events (
    connection text NOT NULL,
    event_id text NOT NULL,
    chat_id text NOT NULL,
    thread_id text NOT NULL,
    message_id text NOT NULL,
    external_user text NOT NULL,
    binding_id uuid,
    payload bytea,
    project_id uuid REFERENCES projects ON DELETE SET NULL,
    reply_kind text NOT NULL DEFAULT '',
    note text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'received' CHECK (state IN ('received','ready','sending','sent','unknown','failed','suppressed')),
    process_attempts integer NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (connection,event_id),
    UNIQUE (connection,chat_id,message_id),
    CHECK ((state='received') = (payload IS NOT NULL))
);
CREATE INDEX im_events_due ON im_events(connection,next_at,received_at) WHERE state IN ('received','ready','sending');
CREATE INDEX im_pending_in_thread ON im_events(connection,chat_id,thread_id,received_at,event_id) WHERE state='received';
CREATE INDEX im_replies_in_thread ON im_events(connection,chat_id,thread_id,received_at,event_id) WHERE state IN ('ready','sending');

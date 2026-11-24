ALTER TABLE model_revisions ADD COLUMN stream_text boolean NOT NULL DEFAULT false
    CHECK (NOT stream_text OR operation='text');
ALTER TABLE runs ADD COLUMN draft_seq bigint NOT NULL DEFAULT 0;

CREATE TABLE run_draft_fragments (
    run_id uuid NOT NULL,
    seq bigint NOT NULL,
    ordinal integer NOT NULL,
    attempt_no integer NOT NULL,
    control_revision integer NOT NULL,
    byte_offset integer NOT NULL CHECK (byte_offset >= 0),
    body text NOT NULL CHECK (octet_length(body) BETWEEN 1 AND 65536),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id,seq),
    UNIQUE (run_id,ordinal,attempt_no,byte_offset),
    FOREIGN KEY (run_id,ordinal) REFERENCES invocations ON DELETE CASCADE
);
CREATE INDEX run_draft_fragment_expiry ON run_draft_fragments(created_at);

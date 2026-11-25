CREATE TABLE studio_playground_calls (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 draft_id uuid NOT NULL REFERENCES studio_drafts ON DELETE CASCADE,
 kind text NOT NULL CHECK(kind IN ('trial','refinement')),
 expected integer NOT NULL,
 request_key text NOT NULL,
 request_hash bytea NOT NULL,
 state text NOT NULL DEFAULT 'working' CHECK(state IN ('working','ready','failed','applied')),
 response_cipher bytea,
 applied_revision integer,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(draft_id,kind,request_key),
 CHECK((state IN ('ready','applied'))=(response_cipher IS NOT NULL)),
 CHECK((state='applied')=(applied_revision IS NOT NULL))
);

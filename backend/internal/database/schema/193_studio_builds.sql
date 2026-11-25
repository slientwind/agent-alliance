CREATE TABLE studio_drafts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner uuid NOT NULL REFERENCES members,
 team_id uuid NOT NULL REFERENCES teams,
 revision integer NOT NULL DEFAULT 1,
 spec jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX studio_drafts_owner ON studio_drafts(owner,updated_at DESC);

CREATE TABLE studio_turns (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 draft_id uuid NOT NULL REFERENCES studio_drafts ON DELETE CASCADE,
 request_key text NOT NULL,
 request_hash bytea NOT NULL,
 expected integer NOT NULL,
 input_cipher bytea NOT NULL,
 state text NOT NULL DEFAULT 'working' CHECK(state IN ('working','completed','failed')),
 response jsonb,
 started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(draft_id,request_key),
 CHECK((state='completed')=(response IS NOT NULL))
);
CREATE INDEX studio_turns_history ON studio_turns(draft_id,started_at DESC) WHERE state='completed';
CREATE UNIQUE INDEX studio_turn_one_live ON studio_turns(draft_id) WHERE state='working';

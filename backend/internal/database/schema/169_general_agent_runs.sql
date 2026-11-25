CREATE TABLE general_agent_runs (
 run_id uuid PRIMARY KEY REFERENCES runs ON DELETE CASCADE,
 tier text NOT NULL CHECK(tier IN ('default','fast','reasoning')),
 progress jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE general_agent_turns (
 run_id uuid NOT NULL REFERENCES general_agent_runs ON DELETE CASCADE,
 turn integer NOT NULL CHECK(turn BETWEEN 1 AND 9),
 state text NOT NULL CHECK(state IN ('submitted','tools','finished')),
 response bytea,
 PRIMARY KEY(run_id,turn),
 CHECK((state='submitted')=(response IS NULL))
);

CREATE TABLE general_agent_dispatch (
 run_id uuid PRIMARY KEY REFERENCES general_agent_runs ON DELETE CASCADE,
 sent_at timestamptz,
 next_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0
);

CREATE TABLE general_agent_actions (
 run_id uuid NOT NULL,
 turn integer NOT NULL,
 position integer NOT NULL CHECK(position BETWEEN 1 AND 4),
 tool_name text NOT NULL,
 call_id text NOT NULL,
 args_cipher bytea NOT NULL,
 fingerprint text NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','completed','rejected')),
 observation_cipher bytea,
 observation_hash text,
 artifact_id uuid REFERENCES artifacts,
 PRIMARY KEY(run_id,turn,position),
 FOREIGN KEY(run_id,turn) REFERENCES general_agent_turns ON DELETE CASCADE,
 CHECK((state IN ('completed','rejected'))=(observation_cipher IS NOT NULL))
);

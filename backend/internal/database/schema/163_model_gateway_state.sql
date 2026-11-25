CREATE TABLE model_gateway_state (
 model_id uuid PRIMARY KEY REFERENCES model_catalog,
 window_start timestamptz NOT NULL,
 requests integer NOT NULL DEFAULT 0 CHECK(requests>=0),
 tokens bigint NOT NULL DEFAULT 0 CHECK(tokens>=0),
 failures integer NOT NULL DEFAULT 0 CHECK(failures BETWEEN 0 AND 3),
 open_until timestamptz,
 probe_until timestamptz
);

CREATE TABLE model_gateway_attempts (
 operation_key text PRIMARY KEY REFERENCES model_gateway_charges,
 model_id uuid NOT NULL REFERENCES model_catalog,
 attempt integer NOT NULL CHECK(attempt>0),
 fallback boolean NOT NULL,
 status text NOT NULL CHECK(status IN ('succeeded','failed','cancelled','rejected')),
 provider_status integer CHECK(provider_status BETWEEN 100 AND 599),
 started_at timestamptz NOT NULL,
 finished_at timestamptz NOT NULL CHECK(finished_at>=started_at)
);

CREATE INDEX model_gateway_attempts_recent ON model_gateway_attempts(started_at,model_id);

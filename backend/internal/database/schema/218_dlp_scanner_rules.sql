CREATE TABLE dlp_rules (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind text NOT NULL CHECK (kind IN ('custom_term','floor_price')),
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
  sensitivity text NOT NULL CHECK (sensitivity IN ('confidential','secret')),
  encrypted_match bytea NOT NULL,
  enabled boolean NOT NULL DEFAULT true,
  revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
  updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

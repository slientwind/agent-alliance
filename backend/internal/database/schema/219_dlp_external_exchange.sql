CREATE TABLE dlp_egress_exchanges (
  run_id uuid REFERENCES runs(id) ON DELETE CASCADE,
  validation_id uuid REFERENCES validations(id) ON DELETE CASCADE,
  scope_id uuid GENERATED ALWAYS AS (coalesce(run_id,validation_id)) STORED,
  exchange text NOT NULL CHECK (char_length(exchange) BETWEEN 1 AND 200),
  destination text NOT NULL CHECK (char_length(destination) BETWEEN 1 AND 253),
  encrypted_tokens bytea NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY(scope_id,exchange),
  CHECK (num_nonnulls(run_id,validation_id)=1)
);
CREATE INDEX dlp_egress_exchanges_expiration ON dlp_egress_exchanges(expires_at);

CREATE TABLE dlp_egress_audit_logs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  run_id uuid REFERENCES runs(id) ON DELETE CASCADE,
  validation_id uuid REFERENCES validations(id) ON DELETE CASCADE,
  exchange text NOT NULL,
  actor uuid NOT NULL REFERENCES members(id),
  destination text NOT NULL,
  operation text NOT NULL,
  outcome text NOT NULL CHECK (outcome IN ('clear','masked','blocked')),
  counts jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
  CHECK (num_nonnulls(run_id,validation_id)=1)
);
CREATE INDEX dlp_egress_audit_run ON dlp_egress_audit_logs(run_id,created_at,id);
CREATE INDEX dlp_egress_audit_validation ON dlp_egress_audit_logs(validation_id,created_at,id) WHERE validation_id IS NOT NULL;

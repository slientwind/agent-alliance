ALTER TABLE management_grants ADD COLUMN allow_export boolean NOT NULL DEFAULT false;
CREATE TABLE management_exports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    grant_id uuid NOT NULL REFERENCES management_grants,
    request_key uuid NOT NULL,
    request_hash bytea NOT NULL,
    include_costs boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    expires_at timestamptz NOT NULL,
    payload jsonb,
    UNIQUE (grant_id, request_key)
);
CREATE INDEX pending_management_export_cleanup ON management_exports(expires_at) WHERE payload IS NOT NULL;

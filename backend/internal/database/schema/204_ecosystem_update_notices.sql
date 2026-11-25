CREATE TABLE ecosystem_update_notices (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 prior_package uuid NOT NULL REFERENCES enterprise_approved_packages,
 new_catalog uuid NOT NULL REFERENCES ecosystem_catalog,
 summary jsonb NOT NULL CHECK(jsonb_typeof(summary)='object'),
 discovered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 acknowledged_by uuid REFERENCES members,
 acknowledged_at timestamptz,
 UNIQUE(prior_package,new_catalog),
 CHECK((acknowledged_by IS NULL)=(acknowledged_at IS NULL))
);
CREATE INDEX ecosystem_update_notices_pending ON ecosystem_update_notices(discovered_at DESC,id) WHERE acknowledged_at IS NULL;

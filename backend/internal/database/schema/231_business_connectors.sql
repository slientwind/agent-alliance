CREATE TABLE business_connectors (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 space_id uuid NOT NULL REFERENCES spaces(id),
 category text NOT NULL CHECK(category IN ('erp','crm','commerce','warehouse','internal')),
 driver text NOT NULL CHECK(driver IN ('saas_pack','readonly_sql','openapi_rest','browser_rpa_readonly')),
 provider text NOT NULL,
 name text NOT NULL CHECK(char_length(btrim(name)) BETWEEN 1 AND 120),
 endpoint text NOT NULL,
 auth_mode text NOT NULL CHECK(auth_mode IN ('service','obo')),
 credential_cipher bytea,
 settings jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(settings)='object'),
 operations jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(operations)='array'),
 maintainer uuid NOT NULL REFERENCES members(id),
 enabled boolean NOT NULL DEFAULT true,
 health_status text NOT NULL DEFAULT 'untested' CHECK(health_status IN ('untested','ready','unavailable')),
 checked_at timestamptz,
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(id,space_id)
);
CREATE INDEX business_connectors_space ON business_connectors(space_id,category,name,id);

CREATE TABLE business_connector_bindings (
 connector_id uuid PRIMARY KEY,
 space_id uuid NOT NULL,
 owner_id uuid NOT NULL REFERENCES members(id),
 revision int NOT NULL DEFAULT 1 CHECK(revision>0),
 FOREIGN KEY(connector_id,space_id) REFERENCES business_connectors(id,space_id) ON DELETE CASCADE
);

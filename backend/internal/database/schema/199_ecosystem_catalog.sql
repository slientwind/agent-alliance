CREATE TABLE ecosystem_sources (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 120),
 kind text NOT NULL CHECK(kind IN ('mcp_registry','skills_git','a2a_directory','private_git')),
 endpoint text NOT NULL CHECK(endpoint LIKE 'https://%' AND length(endpoint)<=2048),
 source_config jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(source_config)='object'),
 cron text NOT NULL CHECK(length(btrim(cron)) BETWEEN 1 AND 120),
 time_zone text NOT NULL DEFAULT 'UTC' CHECK(length(btrim(time_zone)) BETWEEN 1 AND 128),
 enabled boolean NOT NULL DEFAULT true,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 credential bytea CHECK(credential IS NULL OR kind='private_git'),
 created_by uuid NOT NULL REFERENCES members,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(kind,endpoint)
);

CREATE TABLE ecosystem_catalog (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_id uuid NOT NULL REFERENCES ecosystem_sources,
 source_revision integer NOT NULL CHECK(source_revision>0),
 upstream_key text NOT NULL CHECK(length(btrim(upstream_key)) BETWEEN 1 AND 256),
 upstream_version text NOT NULL CHECK(length(btrim(upstream_version)) BETWEEN 1 AND 256),
 upstream_digest text NOT NULL CHECK(upstream_digest ~ '^[0-9a-f]{64}$'),
 kind text NOT NULL CHECK(kind IN ('skill','mcp','agent')),
 name text NOT NULL CHECK(length(btrim(name)) BETWEEN 1 AND 160),
 description text NOT NULL DEFAULT '' CHECK(length(description)<=4000),
 homepage text CHECK(homepage LIKE 'https://%' AND length(homepage)<=2048),
 package_url text CHECK(package_url LIKE 'https://%' AND length(package_url)<=2048),
 metadata jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(metadata)='object'),
 upstream_updated_at timestamptz,
 discovered_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(source_id,upstream_key,upstream_version,upstream_digest)
);
CREATE INDEX ecosystem_catalog_recent ON ecosystem_catalog(source_id,last_seen_at DESC,id);
CREATE INDEX ecosystem_catalog_versions ON ecosystem_catalog(source_id,upstream_key,discovered_at DESC);

CREATE TABLE enterprise_approved_packages (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 catalog_id uuid NOT NULL UNIQUE REFERENCES ecosystem_catalog,
 content_hash text NOT NULL CHECK(content_hash ~ '^[0-9a-f]{64}$'),
 manifest jsonb NOT NULL CHECK(jsonb_typeof(manifest)='object'),
 approved_by uuid NOT NULL REFERENCES members,
 approved_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 revoked_by uuid REFERENCES members,
 revoked_at timestamptz,
 CHECK((revoked_at IS NULL)=(revoked_by IS NULL))
);
CREATE INDEX ecosystem_active_approvals ON enterprise_approved_packages(approved_at DESC) WHERE revoked_at IS NULL;

CREATE FUNCTION protect_ecosystem_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.id,NEW.catalog_id,NEW.content_hash,NEW.manifest,NEW.approved_by,NEW.approved_at)
    IS DISTINCT FROM (OLD.id,OLD.catalog_id,OLD.content_hash,OLD.manifest,OLD.approved_by,OLD.approved_at) THEN
  RAISE EXCEPTION 'Approved ecosystem snapshots are immutable' USING ERRCODE='check_violation';
 END IF;
 RETURN NEW;
END
$$;
CREATE TRIGGER ecosystem_approval_snapshot BEFORE UPDATE ON enterprise_approved_packages
 FOR EACH ROW EXECUTE FUNCTION protect_ecosystem_snapshot();

CREATE TABLE user_skill_bindings (
 member_id uuid NOT NULL REFERENCES members,
 approved_package_id uuid NOT NULL REFERENCES enterprise_approved_packages,
 enabled boolean NOT NULL DEFAULT true,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(member_id,approved_package_id)
);
CREATE INDEX ecosystem_package_bindings ON user_skill_bindings(approved_package_id,member_id) WHERE enabled;

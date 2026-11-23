CREATE TABLE oidc_bindings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    issuer text NOT NULL, client_id text NOT NULL, subject text NOT NULL,
    member_id uuid NOT NULL REFERENCES members, bound_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer,client_id,subject), UNIQUE (issuer,client_id,member_id)
);
ALTER TABLE sessions
    ADD COLUMN oidc_binding uuid REFERENCES oidc_bindings ON DELETE CASCADE,
    ADD COLUMN emergency boolean NOT NULL DEFAULT false CHECK (NOT emergency OR oidc_binding IS NULL);
CREATE INDEX oidc_session_binding ON sessions(oidc_binding) WHERE oidc_binding IS NOT NULL;
CREATE TABLE oidc_flows (
    state_hash bytea PRIMARY KEY, browser_hash bytea NOT NULL, nonce text NOT NULL, verifier text NOT NULL,
    issuer text NOT NULL, client_id text NOT NULL, redirect_uri text NOT NULL,
    intent text NOT NULL CHECK(intent IN ('login','bind','enroll')),
    binding_session bytea REFERENCES sessions(token_hash) ON DELETE CASCADE,
    invite_hash bytea, email text, display_name text, password_hash text,
    expires_at timestamptz NOT NULL DEFAULT now()+interval '5 minutes',
    CHECK ((intent='bind')=(binding_session IS NOT NULL)),
    CHECK ((intent='enroll')=(invite_hash IS NOT NULL AND email IS NOT NULL AND display_name IS NOT NULL AND password_hash IS NOT NULL))
);
CREATE INDEX oidc_flow_expiry ON oidc_flows(expires_at);
CREATE FUNCTION remove_departed_oidc() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM oidc_bindings WHERE member_id=NEW.id;
    RETURN NEW;
END $$;
CREATE TRIGGER remove_departed_oidc AFTER UPDATE OF departed_at ON members FOR EACH ROW
    WHEN(OLD.departed_at IS NULL AND NEW.departed_at IS NOT NULL) EXECUTE FUNCTION remove_departed_oidc();
CREATE TABLE enterprise_login_policy (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 issuer text NOT NULL DEFAULT '', client_id text NOT NULL DEFAULT '', mode text NOT NULL DEFAULT '',
 emergency_member text NOT NULL DEFAULT '', redirect_uri text NOT NULL DEFAULT ''
);
INSERT INTO enterprise_login_policy(singleton) VALUES(true);

CREATE FUNCTION invalidate_emergency_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  DELETE FROM sessions WHERE member_id=NEW.id AND emergency;
  RETURN NEW;
END $$;
CREATE TRIGGER invalidate_emergency_on_demotion AFTER UPDATE OF role ON members FOR EACH ROW
 WHEN(OLD.role='admin' AND NEW.role<>'admin') EXECUTE FUNCTION invalidate_emergency_session();

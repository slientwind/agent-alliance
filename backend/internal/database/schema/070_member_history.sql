ALTER TABLE members ADD COLUMN account_revision bigint NOT NULL DEFAULT 1;
CREATE FUNCTION advance_account_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.account_revision := OLD.account_revision + 1;
    RETURN NEW;
END $$;
CREATE TRIGGER member_account_change BEFORE UPDATE OF enabled, role, departed_at ON members
FOR EACH ROW WHEN ((OLD.enabled,OLD.role,OLD.departed_at) IS DISTINCT FROM (NEW.enabled,NEW.role,NEW.departed_at))
EXECUTE FUNCTION advance_account_revision();
CREATE INDEX member_audit_page ON audit_events(subject,seq DESC)
WHERE action IN ('member.enroll','member.change','department.assign');
CREATE INDEX member_team_audit_page ON audit_events((detail->>'member'),seq DESC)
WHERE action='team.membership';

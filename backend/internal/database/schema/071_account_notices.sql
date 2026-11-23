CREATE TABLE account_notices (
    event_seq bigint PRIMARY KEY REFERENCES audit_events(seq) ON DELETE CASCADE,
    member_id uuid NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    read_at timestamptz
);
CREATE INDEX unread_account_notices ON account_notices(member_id,event_seq) WHERE read_at IS NULL;

CREATE FUNCTION notify_account_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO account_notices(event_seq,member_id) VALUES(NEW.seq, NEW.subject::uuid);
    RETURN NEW;
END $$;
CREATE TRIGGER notice_on_account_change AFTER INSERT ON audit_events
FOR EACH ROW WHEN (NEW.action='member.change' AND
    (NEW.detail->>'role' IS DISTINCT FROM NEW.detail->>'oldRole'
     OR NEW.detail->>'enabled' IS DISTINCT FROM NEW.detail->>'oldEnabled'
     OR coalesce(NEW.detail->>'employment','')<>''))
EXECUTE FUNCTION notify_account_change();

UPDATE delegations d SET revoked_at=now() FROM members m
WHERE m.id=d.member_id AND NOT m.enabled AND d.revoked_at IS NULL;

CREATE FUNCTION wake_project_session() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  PERFORM pg_notify('alliance_project_updates',OLD.project_id::text);
 ELSE
  PERFORM pg_notify('alliance_project_updates',NEW.project_id::text);
 END IF;
 RETURN NULL;
END
$$;

CREATE TRIGGER project_session_event_wakeup AFTER INSERT ON project_session_events
 FOR EACH ROW EXECUTE FUNCTION wake_project_session();
CREATE TRIGGER project_session_member_wakeup AFTER INSERT OR UPDATE OR DELETE ON project_memberships
 FOR EACH ROW EXECUTE FUNCTION wake_project_session();

CREATE INDEX project_memberships_by_member ON project_memberships(member_id,project_id);

CREATE FUNCTION wake_member_project_sessions() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE actor uuid;
BEGIN
 IF TG_TABLE_NAME='members' THEN
  actor := NEW.id;
 ELSIF TG_OP='DELETE' THEN
  actor := OLD.member_id;
 ELSE
  actor := NEW.member_id;
 END IF;
 PERFORM pg_notify('alliance_project_updates',project_id::text) FROM project_memberships WHERE member_id=actor;
 RETURN NULL;
END
$$;

CREATE TRIGGER project_session_team_membership_wakeup AFTER INSERT OR DELETE ON team_members
 FOR EACH ROW EXECUTE FUNCTION wake_member_project_sessions();
CREATE TRIGGER project_session_account_wakeup AFTER UPDATE OF enabled,departed_at ON members
 FOR EACH ROW WHEN ((OLD.enabled,OLD.departed_at) IS DISTINCT FROM (NEW.enabled,NEW.departed_at))
 EXECUTE FUNCTION wake_member_project_sessions();

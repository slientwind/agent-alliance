ALTER TABLE general_agent_turns ADD COLUMN started_at timestamptz;
ALTER TABLE general_agent_turns ALTER COLUMN started_at SET DEFAULT clock_timestamp();

CREATE CONSTRAINT TRIGGER project_session_general_turn_created AFTER INSERT ON general_agent_turns
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_general_turn_changed AFTER UPDATE ON general_agent_turns
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state IS DISTINCT FROM OLD.state)
 EXECUTE FUNCTION publish_project_session_event();

CREATE CONSTRAINT TRIGGER project_session_general_action_created AFTER INSERT ON general_agent_actions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publish_project_session_event();
CREATE CONSTRAINT TRIGGER project_session_general_action_changed AFTER UPDATE ON general_agent_actions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.state IS DISTINCT FROM OLD.state)
 EXECUTE FUNCTION publish_project_session_event();

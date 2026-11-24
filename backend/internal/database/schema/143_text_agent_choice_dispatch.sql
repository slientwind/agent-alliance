-- A rolling upgrade may leave older Workers on the same Temporal queue.
-- Deny old dispatch transactions for a text Agent choice before they can
-- send selection data or reserve an external effect.
CREATE FUNCTION require_text_agent_choice_worker() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='submitted' AND current_setting('alliance.text_agent_choice_worker',true) IS DISTINCT FROM 'true'
   AND EXISTS(
     SELECT FROM runs r JOIN plan_tool_choices route
       ON (route.project_id,route.plan_version)=(r.project_id,r.plan_version)
     JOIN nodes n ON (n.project_id,n.plan_version,n.ordinal)=(route.project_id,route.plan_version,route.ordinal)
     JOIN agent_revisions a ON (a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
     WHERE r.id=NEW.run_id AND NEW.ordinal IN (route.selector,route.ordinal)
       AND agent_choice_profile(a)='text'
   ) THEN
   RAISE EXCEPTION 'text Agent choice requires an updated Worker';
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER guard_text_agent_choice_dispatch
 BEFORE INSERT OR UPDATE OF attempt_no ON invocations
 FOR EACH ROW EXECUTE FUNCTION require_text_agent_choice_worker();

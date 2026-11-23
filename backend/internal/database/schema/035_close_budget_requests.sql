ALTER TABLE budget_requests DROP CONSTRAINT budget_requests_state_check;
ALTER TABLE budget_requests ADD CHECK(state IN ('pending','approved','rejected','withdrawn','closed'));

CREATE FUNCTION close_budget_requests() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 WITH closed AS (
  UPDATE budget_requests SET state='closed',decided_at=now(),
   decision_reason=CASE NEW.stage WHEN 'delivered' THEN '项目已交付' WHEN 'adopted' THEN '成果已采纳' ELSE '项目已取消' END
  WHERE project_id=NEW.id AND state='pending' RETURNING id
 ) INSERT INTO audit_events(actor,action,subject,detail)
 SELECT NULL,'budget.request.close',id::text,jsonb_build_object('project',NEW.id,'stage',NEW.stage) FROM closed;
 RETURN NEW;
END $$;
CREATE TRIGGER close_terminal_budget_requests AFTER UPDATE OF stage ON projects FOR EACH ROW
 WHEN(NEW.stage IN ('delivered','adopted','cancelled') AND NEW.stage IS DISTINCT FROM OLD.stage)
 EXECUTE FUNCTION close_budget_requests();

WITH closed AS (
 UPDATE budget_requests br SET state='closed',decided_at=now(),decision_reason='项目已结束'
 FROM projects p WHERE p.id=br.project_id AND p.stage IN ('delivered','adopted','cancelled') AND br.state='pending'
 RETURNING br.id,p.id AS project,p.stage
) INSERT INTO audit_events(actor,action,subject,detail)
 SELECT NULL,'budget.request.close',id::text,jsonb_build_object('project',project,'stage',stage) FROM closed;

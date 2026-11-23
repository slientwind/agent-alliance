-- A negative receipt belongs to a single operation. Close the run only on
-- an explicit control decision after every dispatched operation is accounted for.
CREATE FUNCTION run_failure_confirmable(target uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (SELECT FROM runs WHERE id=target AND status IN ('running','blocked','reconcile','paused'))
       AND EXISTS (SELECT FROM invocations i JOIN remote_jobs j USING(run_id,ordinal)
            WHERE i.run_id=target AND i.status='remote_closed' AND i.remote_status IN ('failed','rejected')
              AND j.phase='finished' AND j.detail IN ('remote_failed','remote_rejected'))
       AND NOT EXISTS (SELECT FROM invocations WHERE run_id=target AND status NOT IN ('completed','remote_closed'))
       AND NOT EXISTS (SELECT FROM remote_jobs WHERE run_id=target AND phase<>'finished')
       AND NOT EXISTS (
            SELECT FROM superseded_calls s WHERE s.run_id=target AND NOT EXISTS (
                SELECT FROM late_call_receipts l
                WHERE (l.run_id,l.ordinal,l.attempt_no)=(s.run_id,s.ordinal,s.attempt_no)
                  AND (l.outcome='completed' OR (l.outcome='remote'
                       AND l.remote_state IN ('completed','failed','canceled','rejected')))
            )
       )
$$;

CREATE OR REPLACE FUNCTION trace_run_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' OR OLD.status IS DISTINCT FROM NEW.status THEN
   IF NEW.status IN ('completed','cancelled','failed') AND NEW.finished_at IS NULL THEN
     NEW.finished_at=clock_timestamp();
     SELECT NEW.finished_at+t.trace_days*interval '1 day' INTO NEW.trace_expires_at
       FROM projects p JOIN teams t ON t.id=p.team_id WHERE p.id=NEW.project_id;
   END IF;
 END IF;
 RETURN NEW;
END $$;

DROP TRIGGER close_terminal_budget_requests ON projects;
CREATE OR REPLACE FUNCTION close_budget_requests() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 WITH closed AS (
  UPDATE budget_requests SET state='closed',decided_at=now(),
   decision_reason=CASE NEW.stage WHEN 'delivered' THEN '项目已交付' WHEN 'adopted' THEN '成果已采纳' WHEN 'failed' THEN '运行已确认失败' ELSE '项目已取消' END
  WHERE project_id=NEW.id AND state='pending' RETURNING id
 ) INSERT INTO audit_events(actor,action,subject,detail)
 SELECT NULL,'budget.request.close',id::text,jsonb_build_object('project',NEW.id,'stage',NEW.stage) FROM closed;
 RETURN NEW;
END $$;
CREATE TRIGGER close_terminal_budget_requests AFTER UPDATE OF stage ON projects FOR EACH ROW
 WHEN(NEW.stage IN ('delivered','adopted','cancelled','failed') AND NEW.stage IS DISTINCT FROM OLD.stage)
 EXECUTE FUNCTION close_budget_requests();

DROP TRIGGER handoff_project_closed ON projects;
CREATE TRIGGER handoff_project_closed AFTER UPDATE OF stage ON projects FOR EACH ROW
 WHEN (NEW.stage IN ('delivered','adopted','cancelled','failed')) EXECUTE FUNCTION close_responsibility_offers();

DROP TRIGGER project_recovery_ended ON projects;
CREATE OR REPLACE VIEW project_members AS SELECT pm.project_id,pm.member_id,pm.can_review,pm.can_view_costs,pm.recovery_assignment
 FROM project_memberships pm WHERE pm.recovery_assignment IS NULL OR EXISTS(
 SELECT FROM reviewer_handoffs h JOIN projects p ON p.id=h.project_id AND p.reviewer_assignment=h.id
 WHERE h.id=pm.recovery_assignment AND h.project_id=pm.project_id AND h.successor=pm.member_id
 AND h.state='accepted' AND h.access_ended_at IS NULL AND h.recovery_until>statement_timestamp()
 AND p.stage NOT IN ('delivered','adopted','cancelled','failed'));

CREATE OR REPLACE FUNCTION end_recovery_after_project_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE reviewer_handoffs SET access_ended_at=least(now(),recovery_until),access_end_reason=CASE WHEN recovery_until<=now() THEN '授权已到期' WHEN NEW.stage IN('delivered','adopted','cancelled','failed') THEN '项目已结束' ELSE '确认职责已转交' END
 WHERE id=OLD.reviewer_assignment AND recovery_until IS NOT NULL AND access_ended_at IS NULL;
 RETURN NEW;
END $$;
CREATE TRIGGER project_recovery_ended AFTER UPDATE OF stage,reviewer_assignment ON projects FOR EACH ROW
 WHEN (OLD.reviewer_assignment IS NOT NULL AND (NEW.reviewer_assignment IS DISTINCT FROM OLD.reviewer_assignment OR (NEW.stage IS DISTINCT FROM OLD.stage AND NEW.stage IN('delivered','adopted','cancelled','failed'))))
 EXECUTE FUNCTION end_recovery_after_project_change();

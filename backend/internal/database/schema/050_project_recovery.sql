ALTER TABLE reviewer_handoffs ADD COLUMN recovery_until timestamptz,
 ADD COLUMN access_ended_at timestamptz, ADD COLUMN access_end_reason text;
ALTER TABLE reviewer_handoffs DROP CONSTRAINT reviewer_handoffs_check;
ALTER TABLE reviewer_handoffs ADD CHECK(previous_reviewer<>successor OR recovery_until IS NOT NULL),
 ADD CHECK(recovery_until IS NULL OR emergency);
CREATE INDEX reviewer_recovery_scope ON reviewer_handoffs(project_id,successor) WHERE recovery_until IS NOT NULL;

-- All existing project consumers (files, inboxes, channels and the worker included) read effective membership.
ALTER TABLE project_members RENAME TO project_memberships;
ALTER TABLE project_memberships ADD COLUMN recovery_assignment uuid REFERENCES reviewer_handoffs;
CREATE VIEW project_members AS SELECT pm.project_id,pm.member_id,pm.can_review,pm.can_view_costs,pm.recovery_assignment
 FROM project_memberships pm WHERE pm.recovery_assignment IS NULL OR EXISTS(
 SELECT FROM reviewer_handoffs h JOIN projects p ON p.id=h.project_id AND p.reviewer_assignment=h.id
 WHERE h.id=pm.recovery_assignment AND h.project_id=pm.project_id AND h.successor=pm.member_id
 AND h.state='accepted' AND h.access_ended_at IS NULL AND h.recovery_until>statement_timestamp()
 AND p.stage NOT IN ('delivered','adopted','cancelled'));

CREATE FUNCTION end_recovery_after_project_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE reviewer_handoffs SET access_ended_at=least(now(),recovery_until),access_end_reason=CASE WHEN recovery_until<=now() THEN '授权已到期' WHEN NEW.stage IN('delivered','adopted','cancelled') THEN '项目已结束' ELSE '确认职责已转交' END
 WHERE id=OLD.reviewer_assignment AND recovery_until IS NOT NULL AND access_ended_at IS NULL;
 RETURN NEW;
END $$;
CREATE TRIGGER project_recovery_ended AFTER UPDATE OF stage,reviewer_assignment ON projects FOR EACH ROW
 WHEN (OLD.reviewer_assignment IS NOT NULL AND (NEW.reviewer_assignment IS DISTINCT FROM OLD.reviewer_assignment OR (NEW.stage IS DISTINCT FROM OLD.stage AND NEW.stage IN('delivered','adopted','cancelled'))))
 EXECUTE FUNCTION end_recovery_after_project_change();

CREATE FUNCTION end_recovery_on_team_loss() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE reviewer_handoffs h SET access_ended_at=now(),access_end_reason='团队授权已撤销',
 state=CASE WHEN h.state='pending' THEN 'superseded' ELSE h.state END
 FROM projects p WHERE p.id=h.project_id AND p.team_id=OLD.team_id AND OLD.member_id IN(h.proposer,h.successor)
 AND h.recovery_until IS NOT NULL AND h.access_ended_at IS NULL AND h.state IN('pending','accepted');
 RETURN OLD;
END $$;
CREATE TRIGGER recovery_team_loss AFTER DELETE ON team_members FOR EACH ROW EXECUTE FUNCTION end_recovery_on_team_loss();
CREATE FUNCTION end_recovery_on_account_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE reviewer_handoffs SET access_ended_at=now(),access_end_reason='账号或管理授权已变更'
 WHERE recovery_until IS NOT NULL AND access_ended_at IS NULL AND state IN('accepted','pending','superseded')
 AND ((NOT NEW.enabled AND NEW.id IN(proposer,successor)) OR (NEW.role<>'admin' AND NEW.id=proposer));
 RETURN NEW;
END $$;
CREATE TRIGGER recovery_account_change AFTER UPDATE OF role,enabled ON members FOR EACH ROW
 WHEN ((OLD.enabled AND NOT NEW.enabled) OR (OLD.role='admin' AND NEW.role<>'admin'))
 EXECUTE FUNCTION end_recovery_on_account_change();

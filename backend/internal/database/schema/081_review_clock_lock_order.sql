-- The scan, policy snapshot, and stage-change trigger serialize on the project
-- row. Taking an advisory lock before the clock FK checked this row used to
-- deadlock with a writer that already held the row and then requested that lock.
CREATE OR REPLACE FUNCTION sync_review_clocks(target uuid) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 PERFORM id FROM projects WHERE id=target FOR NO KEY UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 WITH active AS MATERIALIZED (SELECT * FROM review_actions WHERE project_id=target),
 retired AS (
   UPDATE review_clocks c SET closed_at=now()
   WHERE c.project_id=target AND c.closed_at IS NULL AND NOT EXISTS (
    SELECT FROM active a WHERE a.action_key=c.action_key)
 )
 INSERT INTO review_clocks(action_key,project_id,opened_at,original_due,due_at,lead_hours,escalation_hours)
 SELECT a.action_key,target,a.opened,d.deadline,d.deadline,
        coalesce(p.lead_hours,t.lead_hours,2),coalesce(p.escalation_hours,t.escalation_hours,4)
 FROM active a LEFT JOIN review_policies p ON p.project_id=target
 LEFT JOIN review_policies t ON t.team_id=a.team_id
 CROSS JOIN LATERAL (SELECT a.opened+make_interval(hours=>coalesce(p.hours,t.hours,24)) AS deadline) d
 WHERE NOT EXISTS (SELECT FROM review_clocks c WHERE c.action_key=a.action_key AND c.closed_at IS NULL)
 ON CONFLICT(action_key) WHERE closed_at IS NULL DO NOTHING;
END $$;

CREATE OR REPLACE FUNCTION close_project_review_clocks() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 UPDATE review_clocks SET closed_at=clock_timestamp() WHERE project_id=NEW.id AND closed_at IS NULL;
 RETURN NEW;
END $$;

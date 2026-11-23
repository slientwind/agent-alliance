ALTER TABLE planning_requests ADD COLUMN rule_pins jsonb NOT NULL DEFAULT '[]';
ALTER TABLE messages ADD COLUMN planning_request uuid REFERENCES planning_requests;
CREATE FUNCTION planning_rule_pins(project_key uuid, who uuid) RETURNS jsonb
LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN coalesce(bool_and(rule_usable(r.id,r.published_revision,who)),true)
 THEN coalesce(jsonb_agg(jsonb_build_object('id',r.id,'rev',r.published_revision) ORDER BY r.id),'[]')
 END FROM projects p JOIN team_rules r ON r.team_id=p.team_id
 WHERE p.id=project_key AND NOT r.disabled AND r.published_revision IS NOT NULL
$$;
CREATE VIEW current_planning_requests AS
 SELECT q.*,CASE WHEN q.status='ready' AND q.rule_pins IS DISTINCT FROM planning_rule_pins(q.project_id,q.actor)
 THEN 'stale' ELSE q.status END AS current_status FROM planning_requests q;
CREATE FUNCTION planning_rules_readable(pins jsonb, who uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT FROM jsonb_to_recordset(pins) x(id uuid, rev integer)
 WHERE NOT rule_usable(x.id,x.rev,who))
$$;

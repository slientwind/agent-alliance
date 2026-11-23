-- Field confirmation is an input decision by a person. Delegated services
-- retain the owner as the decision maker. For an autonomous application the
-- current final reviewer can make this specific decision in the workspace.
CREATE VIEW column_correction_authority AS
 SELECT r.id run_id,m.id member_id FROM runs r JOIN projects p ON p.id=r.project_id
 JOIN project_members pm ON pm.project_id=p.id JOIN members m ON m.id=pm.member_id
 WHERE m.enabled AND m.kind='person' AND (
   EXISTS(SELECT FROM call_review_authority a WHERE (a.run_id,a.member_id)=(r.id,m.id))
   OR (r.app_id IS NOT NULL AND r.delegated_by IS NULL AND p.final_reviewer=m.id AND pm.can_review
       AND EXISTS(SELECT FROM team_members t WHERE (t.team_id,t.member_id)=(p.team_id,m.id)))
 );

CREATE OR REPLACE FUNCTION visible_column_issues(target uuid, actor uuid, document jsonb)
 RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT CASE WHEN document IS NOT NULL
 AND EXISTS(SELECT FROM column_correction_authority ca WHERE ca.run_id=target AND ca.member_id=actor)
 AND NOT EXISTS(SELECT FROM run_source_inputs f WHERE f.run_id=target AND f.extracted IS NULL)
 AND NOT EXISTS(SELECT FROM column_correction_items(document) issue
   WHERE NOT EXISTS(SELECT FROM run_source_inputs f WHERE f.run_id=target
   AND f.source_id::text=issue->>'source' AND f.version::text=issue->>'version' AND f.extracted IS NOT NULL))
 THEN (SELECT jsonb_agg(issue ORDER BY n) FROM column_correction_items(document) WITH ORDINALITY AS q(issue,n)) END
$$;

CREATE OR REPLACE VIEW column_suggestion_targets AS
 SELECT r.id run_id,r.control_revision,ca.member_id actor,p.id project_id,i.ordinal,i.attempt_no,i.input_issue,
        m.id resource_id,v.revision resource_revision,v.key_generation,v.encrypted_key,v.endpoint,
        v.model,v.estimate_cny,m.team_id,b.id budget_id
 FROM runs r JOIN projects p ON (p.id,p.plan_version)=(r.project_id,r.plan_version)
 JOIN column_correction_authority ca ON ca.run_id=r.id
 JOIN invocations i ON i.run_id=r.id AND i.status='input_rejected' AND i.input_issue IS NOT NULL
 JOIN model_revisions v ON (v.resource_id,v.revision)=(p.planning_resource,p.planning_revision)
 JOIN model_resources m ON m.id=v.resource_id AND m.enabled AND NOT m.emergency
 JOIN team_members tm ON (tm.team_id,tm.member_id)=(m.team_id,ca.member_id)
 JOIN resource_budgets b ON b.model_id=m.id
 WHERE r.status='reconcile' AND v.operation='text' AND NOT v.key_blocked
 AND NOT EXISTS(SELECT FROM call_reviews c
  WHERE (c.run_id,c.ordinal,c.attempt_no)=(i.run_id,i.ordinal,i.attempt_no) AND c.decision='redo');

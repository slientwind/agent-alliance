CREATE VIEW task_participants AS
SELECT pm.*, CASE
 WHEN pm.member_id=p.owner THEN 'owner'
 WHEN pm.member_id=p.final_reviewer AND pm.can_review THEN 'approver'
 WHEN pm.can_review THEN 'reviewer'
 ELSE 'contributor'
END AS role
FROM project_members pm JOIN projects p ON p.id=pm.project_id;

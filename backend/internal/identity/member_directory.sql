SELECT (
SELECT coalesce(jsonb_agg(jsonb_build_object('id',m.id,'name',m.display_name,'email',m.email,
    'role',m.role,'enabled',m.enabled,'departed',m.departed_at IS NOT NULL,
    'createdAt',m.created_at,'accountRevision',m.account_revision::text,
    'department',d.name,'registration',enrollment.method,'teams',t.teams)
    ORDER BY m.created_at,m.id),'[]')
FROM members m
LEFT JOIN departments d ON d.id=m.department_id
LEFT JOIN LATERAL (
    SELECT CASE e.detail->>'role' WHEN 'admin' THEN 'initial' WHEN 'member' THEN 'invitation' END AS method
    FROM audit_events e WHERE e.subject=m.id::text AND e.action='member.enroll'
    ORDER BY e.seq LIMIT 1
) enrollment ON true
CROSS JOIN LATERAL (
    SELECT coalesce(jsonb_agg(jsonb_build_object('id',t.id,'name',t.name) ORDER BY t.name,t.id),'[]') AS teams
    FROM team_members tm JOIN teams t ON t.id=tm.team_id WHERE tm.member_id=m.id
) t
WHERE m.kind='person'
) FROM members viewer WHERE viewer.id=$1 AND viewer.enabled AND viewer.role='admin'

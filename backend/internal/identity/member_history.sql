SELECT target.id, (
    SELECT coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
        'id',e.seq::text,'at',e.at,'action',e.action,'actor',who.display_name,
        'reason',CASE WHEN e.action<>'member.enroll' THEN e.detail->>'reason' END,
        'oldRole',CASE WHEN e.action='member.change' THEN e.detail->>'oldRole' END,
        'role',CASE WHEN e.action IN ('member.enroll','member.change') THEN e.detail->>'role' END,
        'oldEnabled',CASE WHEN e.action='member.change' THEN e.detail->'oldEnabled' END,
        'enabled',CASE WHEN e.action='member.change' THEN e.detail->'enabled' END,
        'employment',CASE WHEN e.action='member.change' THEN nullif(e.detail->>'employment','') END,
        'joined',CASE WHEN e.action='team.membership' THEN e.detail->'joined' END,
        'team',CASE WHEN e.action='team.membership' THEN coalesce(team.name,e.subject) END,
        'previousDepartment',CASE WHEN e.action='department.assign' THEN coalesce(olddept.name,e.detail->>'previous') END,
        'department',CASE WHEN e.action='department.assign' THEN coalesce(dept.name,e.detail->>'department') END
    )) ORDER BY e.seq DESC),'[]')
    FROM (
        SELECT seq,at,actor,action,subject,detail FROM audit_events
        WHERE (subject=target.id::text AND action IN ('member.enroll','member.change','department.assign')
            OR action='team.membership' AND detail->>'member'=target.id::text)
          AND ($3::bigint IS NULL OR seq<$3)
        ORDER BY seq DESC LIMIT 21
    ) e LEFT JOIN members who ON who.id=e.actor
    LEFT JOIN teams team ON e.action='team.membership' AND team.id::text=e.subject
    LEFT JOIN departments olddept ON e.action='department.assign' AND olddept.id::text=e.detail->>'previous'
    LEFT JOIN departments dept ON e.action='department.assign' AND dept.id::text=e.detail->>'department'
)
FROM members target, members admin
WHERE target.id=$1 AND target.kind='person' AND admin.id=$2 AND admin.enabled AND admin.role='admin'

SELECT jsonb_build_object('rows', (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY x.id),'[]')
    FROM (
        SELECT a.id, a.display_name AS name, a.kind, count(*) AS runs
        FROM runs r JOIN projects p ON p.id=r.project_id JOIN members a ON a.id=r.actor
        WHERE (g.enterprise_id IS TRUE OR p.team_id=g.team_id OR p.originating_department=g.department_id)
          AND r.created_at >= $3 AND r.created_at < $4
          AND ($5::uuid IS NULL OR a.id > $5)
          AND ($6::text='' OR strpos(lower(a.display_name),lower($6))>0)
        GROUP BY a.id ORDER BY a.id LIMIT 26
    ) x))
FROM management_grants g JOIN members m ON m.id=g.member_id AND m.enabled
WHERE g.id=$1 AND g.member_id=$2 AND g.allow_details AND g.revoked_at IS NULL AND g.expires_at>statement_timestamp()

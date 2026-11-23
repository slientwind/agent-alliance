SELECT jsonb_build_object('rows', (SELECT coalesce(jsonb_agg(
    jsonb_build_object('id',r.id,'actor',r.actor_name,'kind',r.actor_kind,
        'reviewer',r.reviewer,'status',r.status,'createdAt',r.created_at,'finishedAt',r.finished_at,
        'elapsedSeconds',greatest(0,trunc(extract(epoch FROM (coalesce(r.finished_at,statement_timestamp())-r.created_at)))),
        'block',CASE
            WHEN r.status='paused' THEN 'paused'
            WHEN r.status='reconcile' THEN 'receipt'
            WHEN r.status='blocked' THEN CASE
                WHEN e.code='budget_denied' THEN 'budget'
                WHEN e.code IN ('resource_denied','delegation_expired','reviewer_changed') THEN 'permission'
                WHEN e.code='verification_failed' THEN 'validation'
                WHEN e.code IN ('input_changed','base_changed') THEN 'input'
                ELSE 'other' END END)
    || CASE WHEN g.detail_costs THEN jsonb_build_object('costs',c.data) ELSE '{}'::jsonb END
    ORDER BY r.created_at DESC,r.id DESC),'[]')
    FROM (
        SELECT r.id,r.status,r.created_at,r.finished_at,a.display_name AS actor_name,a.kind AS actor_kind,
               reviewer.display_name AS reviewer
        FROM runs r JOIN projects p ON p.id=r.project_id JOIN members a ON a.id=r.actor
        JOIN members reviewer ON reviewer.id=p.final_reviewer
        WHERE (g.enterprise_id IS TRUE OR p.team_id=g.team_id OR p.originating_department=g.department_id)
          AND r.created_at >= $3 AND r.created_at < $4
          AND ($5::uuid IS NULL OR r.actor=$5)
          AND ($6::timestamptz IS NULL OR (r.created_at,r.id)<($6,$7::uuid))
        ORDER BY r.created_at DESC,r.id DESC LIMIT 26
    ) r
    LEFT JOIN LATERAL (
        SELECT code FROM trace_events WHERE run_id=r.id AND kind='checkpoint' AND r.status='blocked'
        ORDER BY seq DESC LIMIT 1
    ) e ON true
    LEFT JOIN LATERAL (
        SELECT jsonb_build_object('entries',count(*),'recordedCny',coalesce(sum(s.amount_cny),0)::text,
            'remainingEstimateCny',coalesce(sum(greatest(s.reserved_cny-s.amount_cny,0)) FILTER (WHERE NOT s.settled),0)::text,
            'unknown',count(*) FILTER (WHERE NOT s.settled AND s.reserved_cny IS NULL),
            'pending',count(*) FILTER (WHERE NOT s.settled)) AS data
        FROM spend s WHERE g.detail_costs AND s.run_id=r.id AND
            (g.enterprise_id IS TRUE OR s.team_id=g.team_id OR s.originating_department=g.department_id)
    ) c ON g.detail_costs))
FROM management_grants g JOIN members m ON m.id=g.member_id AND m.enabled
WHERE g.id=$1 AND g.member_id=$2 AND g.allow_details AND g.revoked_at IS NULL AND g.expires_at>statement_timestamp()

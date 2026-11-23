SELECT jsonb_build_object(
    'runs',o.runs,'completed',o.completed,'waiting',o.waiting,'runStates',o.run_states,'checked',o.checked,
    'reviewed',o.reviewed,'adopted',o.adopted,'editions',o.editions,
    'from',$3::timestamptz,'to',$4::timestamptz,'checkedAt',statement_timestamp(),'expiresAt',g.expires_at,
    'costs',CASE WHEN g.include_costs THEN c.costs END
)
FROM management_grants g JOIN members m ON m.id=g.member_id AND m.enabled
CROSS JOIN LATERAL (
    SELECT coalesce(sum(n) FILTER (WHERE kind='run'),0)::bigint AS runs,
           coalesce(sum(n) FILTER (WHERE kind='run' AND state='completed'),0)::bigint AS completed,
           coalesce(sum(n) FILTER (WHERE kind='run' AND state IN ('blocked','reconcile','paused')),0)::bigint AS waiting,
           coalesce(sum(n) FILTER (WHERE kind='check'),0)::bigint AS checked,
           coalesce(sum(n) FILTER (WHERE kind='review'),0)::bigint AS reviewed,
           coalesce(sum(n) FILTER (WHERE kind='adoption'),0)::bigint AS adopted,
           coalesce(sum(n) FILTER (WHERE kind='delivery'),0)::bigint AS editions,
           coalesce(jsonb_object_agg(state,n) FILTER (WHERE kind='run'),'{}') AS run_states
    FROM (
        SELECT kind, CASE WHEN status IN ('queued','running','blocked','reconcile','paused','completed','failed','cancelled')
                THEN status ELSE 'other' END AS state, count(*) AS n
        FROM management_outcome_events e
        WHERE (g.enterprise_id IS TRUE OR e.team_id=g.team_id OR e.department_id=g.department_id)
          AND e.at >= $3 AND e.at < $4
        GROUP BY kind,state
    ) period_events
) o
LEFT JOIN LATERAL (
    SELECT jsonb_build_object('calls',count(*),'tokens',coalesce(sum(tokens),0),
        'recordedCny',coalesce(sum(recorded),0)::text,'remainingEstimateCny',coalesce(sum(reserved),0)::text,
        'pending',count(*) FILTER (WHERE pending),'unknown',count(*) FILTER (WHERE unknown)) AS costs
    FROM management_call_costs e WHERE g.include_costs AND (g.enterprise_id IS TRUE OR e.team_id=g.team_id OR e.department_id=g.department_id)
    AND e.at >= $3 AND e.at < $4
) c ON g.include_costs
WHERE g.id=$1 AND g.member_id=$2 AND g.revoked_at IS NULL AND g.expires_at>statement_timestamp()

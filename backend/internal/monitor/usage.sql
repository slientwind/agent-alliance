WITH raw_windows AS MATERIALIZED (SELECT * FROM monitor_usage_raw_ranges($1,$2)),
event_counts AS (
    SELECT actor,kind,state,n FROM monitor_member_outcome_hourly
    WHERE bucket_hour >= $1 AND bucket_hour+interval '1 hour' <= $2 AND ($4::uuid IS NULL OR team_id=$4)
    UNION ALL
    SELECT actor,kind,CASE WHEN status IN ('queued','running','blocked','reconcile','paused','completed','failed','cancelled')
      THEN status ELSE 'other' END AS state,count(*)
    FROM raw_windows w JOIN management_outcome_events e ON e.at>=w.lo AND e.at<w.hi
    WHERE ($4::uuid IS NULL OR team_id=$4) GROUP BY actor,kind,state
), period_events AS (
    SELECT actor,kind,state,sum(n)::bigint AS n FROM event_counts GROUP BY actor,kind,state
), outcomes AS (
    SELECT actor, coalesce(sum(n) FILTER (WHERE kind='run'),0)::bigint AS runs,
           coalesce(sum(n) FILTER (WHERE kind='run' AND state='completed'),0)::bigint AS completed,
           coalesce(sum(n) FILTER (WHERE kind='run' AND state IN ('blocked','reconcile','paused')),0)::bigint AS waiting,
           coalesce(sum(n) FILTER (WHERE kind='check'),0)::bigint AS checked,
           coalesce(sum(n) FILTER (WHERE kind='review'),0)::bigint AS reviewed,
           coalesce(sum(n) FILTER (WHERE kind='adoption'),0)::bigint AS adopted,
           coalesce(sum(n) FILTER (WHERE kind='delivery'),0)::bigint AS editions,
           coalesce(jsonb_object_agg(state,n) FILTER (WHERE kind='run'),'{}') AS run_states
    FROM period_events GROUP BY actor
), cost_counts AS (
    SELECT actor,attempts,tokens,unknown,pending,reserved,amount FROM monitor_member_cost_hourly
    WHERE bucket_hour >= $1 AND bucket_hour+interval '1 hour' <= $2 AND ($4::uuid IS NULL OR team_id=$4)
    UNION ALL
    SELECT actor,count(*) AS attempts,sum(tokens),count(*) FILTER (WHERE unknown),count(*) FILTER (WHERE pending),sum(reserved),sum(recorded)
    FROM raw_windows w JOIN management_call_costs c ON c.at>=w.lo AND c.at<w.hi
    WHERE ($4::uuid IS NULL OR team_id=$4) GROUP BY actor
), costs AS (
    SELECT actor,sum(attempts)::bigint AS attempts,sum(tokens)::bigint AS tokens,sum(unknown)::bigint AS unknown,
       sum(pending)::bigint AS pending,sum(reserved) AS reserved,sum(amount) AS amount FROM cost_counts GROUP BY actor
)
SELECT m.id, m.display_name, m.kind,
       coalesce(o.runs,0), coalesce(o.completed,0), coalesce(o.waiting,0),
       coalesce(c.attempts,0), coalesce(c.tokens,0), coalesce(c.unknown,0), coalesce(c.amount,0)::text,
       coalesce(o.checked,0), coalesce(o.reviewed,0), coalesce(o.adopted,0), coalesce(o.editions,0),
       coalesce(c.pending,0), coalesce(c.reserved,0)::text, coalesce(o.run_states,'{}')
FROM members m LEFT JOIN outcomes o ON o.actor=m.id LEFT JOIN costs c ON c.actor=m.id
WHERE $3='' OR m.id::text=$3 ORDER BY m.kind,m.display_name,m.id

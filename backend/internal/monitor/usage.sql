WITH period_events AS (
    SELECT actor, kind, CASE WHEN status IN ('queued','running','blocked','reconcile','paused','completed','failed','cancelled')
                THEN status ELSE 'other' END AS state, count(*) AS n
    FROM management_outcome_events
    WHERE at >= $1 AND at < $2 AND ($4::uuid IS NULL OR team_id=$4)
    GROUP BY actor,kind,state
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
), costs AS (
    SELECT actor, count(*) AS attempts, sum(tokens) AS tokens, count(*) FILTER (WHERE unknown) AS unknown,
           count(*) FILTER (WHERE pending) AS pending, sum(reserved) AS reserved, sum(recorded) AS amount
    FROM management_call_costs WHERE at >= $1 AND at < $2 AND ($4::uuid IS NULL OR team_id=$4) GROUP BY actor
)
SELECT m.id, m.display_name, m.kind,
       coalesce(o.runs,0), coalesce(o.completed,0), coalesce(o.waiting,0),
       coalesce(c.attempts,0), coalesce(c.tokens,0), coalesce(c.unknown,0), coalesce(c.amount,0)::text,
       coalesce(o.checked,0), coalesce(o.reviewed,0), coalesce(o.adopted,0), coalesce(o.editions,0),
       coalesce(c.pending,0), coalesce(c.reserved,0)::text, coalesce(o.run_states,'{}')
FROM members m LEFT JOIN outcomes o ON o.actor=m.id LEFT JOIN costs c ON c.actor=m.id
WHERE $3='' OR m.id::text=$3 ORDER BY m.kind,m.display_name,m.id

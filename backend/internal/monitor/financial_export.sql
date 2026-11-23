WITH scoped AS MATERIALIZED (
    SELECT c.*
    FROM management_grants g JOIN management_call_costs c
      ON g.enterprise_id IS TRUE OR c.team_id=g.team_id OR c.department_id=g.department_id
    WHERE g.id=$1 AND g.member_id=$2 AND g.financial_export AND g.include_costs
      AND g.revoked_at IS NULL AND g.expires_at>clock_timestamp()
      AND c.at >= $3 AND c.at < $4
    LIMIT 10001
), volume AS (SELECT count(*) AS n FROM scoped)
SELECT CASE WHEN n <= 10000 THEN jsonb_build_object(
    'version',1,'kind','callCosts','currency','CNY','scope',jsonb_build_object('kind',$5::text,'name',$6::text),
    'from',$3::timestamptz,'to',$4::timestamptz,'generatedAt',statement_timestamp(),
    'totals',(SELECT jsonb_build_object('calls',count(*),'tokens',coalesce(sum(tokens),0),
        'recordedCny',coalesce(sum(recorded),0)::text,'remainingEstimateCny',coalesce(sum(reserved),0)::text,
        'pending',count(*) FILTER (WHERE pending),'unknown',count(*) FILTER (WHERE unknown)) FROM scoped),
    'calls',(SELECT coalesce(jsonb_agg(jsonb_build_object(
        'callId',encode(sha256(convert_to(c.op_key,'UTF8')),'hex'),'ledgerId',c.spend_id,'at',c.at,'operation',c.operation,
        'actorId',c.actor,'payerTeamId',c.team_id,'departmentId',c.department_id,'tokens',c.tokens,
        'recordedCny',c.recorded::text,'remainingEstimateCny',CASE WHEN NOT c.unknown THEN c.reserved::text END,
        'resource', (SELECT jsonb_build_object('budgetId',s.resource_budget,'revision',s.source_revision,'name',s.source_name)
            FROM spend s WHERE s.id=c.spend_id AND s.resource_budget IS NOT NULL),
        'pending',c.pending,'unknown',c.unknown,'postings',(SELECT coalesce(jsonb_agg(jsonb_build_object(
            'reference',b.line_key,'recordedAt',b.recorded_at,'amountCny',b.amount_cny::text,'final',b.final)
            ORDER BY b.recorded_at,b.line_key),'[]') FROM bill_rows b WHERE b.spend_id=c.spend_id AND b.team_id=c.team_id)
        ) ORDER BY c.at,c.op_key),'[]') FROM scoped c)
) END FROM volume

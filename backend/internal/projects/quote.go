package projects

// Only expose expected project costs and whether the plan fits the current limits.
// A participating member need not have access to the paying team's other spending.
const planQuoteSQL = `(
 with candidates as (
  select n.ordinal,n.agent_id,n.agent_revision from nodes n
  where (n.project_id,n.plan_version)=(p.id,p.plan_version)
  union all
  select route.ordinal,route.alternative,route.alternative_revision from plan_tool_choices route
  where (route.project_id,route.plan_version)=(p.id,p.plan_version)
 ), candidate_costs as (
  select n.ordinal,n.agent_id,n.agent_revision,a.team_id,bind.budget_id, r.card is not null as remote,
   case when r.tool is not null then 0 else mr.estimate_cny end as amount
  from candidates n join agents a on a.id=n.agent_id
  join agent_revisions r on (r.agent_id,r.revision)=(n.agent_id,n.agent_revision)
  join agent_budget_bindings bind on(bind.agent_id,bind.revision)=(n.agent_id,n.agent_revision)
  left join model_revisions mr on (mr.resource_id,mr.revision)=(r.resource_id,r.resource_revision)
 ), costs as (
  select ordinal,team_id,case when bool_or(amount is null) then null else max(amount) end amount,
    bool_or(remote) remote from candidate_costs group by ordinal,team_id
 ), resource_costs as (
   select ordinal,budget_id,case when bool_or(amount is null) then null else max(amount) end amount
   from candidate_costs group by ordinal,budget_id
 ), payer_costs as (
  select team_id, coalesce(sum(amount),0) as known, count(*) filter(where amount is null) as unknown
  from costs group by team_id
 ), totals as (
  select coalesce(sum(amount),0) as known,count(amount) as priced,
   count(*) filter(where amount is null) as unknown, coalesce(bool_or(remote),false) as remote from costs
 )
 select jsonb_build_object('known',q.known::text,'priced',q.priced,'unknown',q.unknown,'remote',q.remote,
  'projectShort',budget_amount_short(b.used,q.known,q.unknown>0,b.limit_cny),
  'grantShort',exists(select from (select agent_id,agent_revision,count(distinct ordinal) needed
    from candidate_costs group by agent_id,agent_revision) c
   left join effective_project_agent_grants g on(g.project_id,g.agent_id,g.revision)=(p.id,c.agent_id,c.agent_revision)
   where not project_agent_access(null,c.agent_id,c.agent_revision,p.owner)
   and (g.id is null or c.needed+project_agent_grant_usage(g.id)>g.call_limit)),
  'resourceShort',exists(select from (select budget_id,count(*) needed,coalesce(sum(amount),0) cost,count(*) filter(where amount is null) unknown from resource_costs group by budget_id) c
    join resource_budget_status rb on rb.id=c.budget_id where rb.calls+c.needed>rb.call_limit or budget_amount_short(rb.used,c.cost,c.unknown>0,rb.limit_cny)),
  'teamShort',exists(
   select from payer_costs pc join budgets tb on tb.team_id=pc.team_id
   cross join lateral (select coalesce(sum(amount_cny + case when settled then 0 else greatest(coalesce(reserved_cny,0)-amount_cny,0) end),0) as used
                      from spend where team_id=pc.team_id) s
   where budget_amount_short(s.used,pc.known,pc.unknown>0,tb.limit_cny)))
 from totals q join project_budget_status b on b.project_id=p.id)`

with rules as materialized (select * from alert_rules where enabled),
external_calls as (
 select ar.id,c.agent_id,c.team_id,c.started_at,c.failure,
  row_number() over(partition by ar.id,c.agent_id,c.team_id order by c.started_at desc,c.run_id desc) as position
 from rules ar join monitor_call_facts c on ar.kind='external_failure' and c.source_type='external'
  and (ar.team_id is null or ar.team_id=c.team_id) and c.ended_at is not null
  and c.started_at>=$1::timestamptz-make_interval(mins=>ar.window_minutes) and c.started_at<=$1
), external_candidates as (
 select r.id,r.revision,r.silence_minutes,c.agent_id::text||'/'||c.team_id::text as subject,
  extract(epoch from max(c.started_at))::text as event_key,
  '外部 Agent 连续失败 '||r.threshold||' 次。Agent '||c.agent_id as message
 from external_calls c join rules r on r.id=c.id where c.position<=r.threshold
 group by r.id,r.revision,r.silence_minutes,r.threshold,c.agent_id,c.team_id
 having count(*)=r.threshold and bool_and(c.failure)
), fallback_candidates as (
 select r.id,r.revision,r.silence_minutes,c.team_id::text||'/'||c.tier as subject,
  extract(epoch from max(a.started_at))::text as event_key,
  '模型档位 '||c.tier||' 已切换备选 '||count(*)||' 次。团队 '||c.team_id as message
 from rules r join model_gateway_attempts a on r.kind='model_fallback' and a.fallback
   and a.started_at>=$1::timestamptz-make_interval(mins=>r.window_minutes) and a.started_at<=$1
 join model_gateway_charges c on c.operation_key=a.operation_key and (r.team_id is null or c.team_id=r.team_id)
 group by r.id,r.revision,r.silence_minutes,r.threshold,c.team_id,c.tier having count(*)>=r.threshold
), by_department as (
 select r.id,r.revision,r.silence_minutes,r.threshold,s.originating_department,
  date_trunc('day',s.created_at,'UTC') as day_started,sum(s.amount_cny) as cost
 from rules r join spend s on r.kind='department_spike' and s.originating_department is not null
  and (r.team_id is null or r.team_id=s.team_id) and s.created_at>=date_trunc('day',$1,'UTC')-interval '168 hours' and s.created_at<=$1
 group by r.id,r.revision,r.silence_minutes,r.threshold,s.originating_department,day_started
), spike_candidates as (
 select id,revision,silence_minutes,originating_department::text as subject,
  to_char($1 at time zone 'UTC','YYYY-MM-DD') as event_key,
  '部门今日模型与工具消费已达到过去 7 日日均的 '||threshold||'% 。部门 '||originating_department as message
 from by_department group by id,revision,silence_minutes,threshold,originating_department
 having coalesce(sum(cost) filter(where day_started<date_trunc('day',$1,'UTC')),0)>0
 and coalesce(sum(cost) filter(where day_started=date_trunc('day',$1,'UTC')),0)*700
    >= sum(cost) filter(where day_started<date_trunc('day',$1,'UTC'))*threshold
), budget_candidates as (
 select r.id,r.revision,r.silence_minutes,b.scope||'/'||b.scope_id||'/'||b.threshold as subject,
  to_char(b.month_start,'YYYY-MM') as event_key,
  (case b.scope when 'enterprise' then '企业' when 'team' then '团队' else '员工' end)||
  '模型月度预算已达到 '||b.threshold||'%。已用与预占合计 '||b.used_micro_cny||' 微元，预算 '||b.limit_micro_cny||' 微元。' as message
 from rules r join model_budget_alert_events b on r.kind='budget_limit' and b.threshold=r.threshold
  and (r.team_id is null or (b.scope='team' and b.scope_id=r.team_id))
  and b.occurred_at>=$1::timestamptz-interval '1 day' and b.occurred_at<=$1
), candidates as (
 select * from external_candidates union all select * from fallback_candidates
 union all select * from spike_candidates union all select * from budget_candidates
)
insert into alert_deliveries(rule_id,rule_revision,subject,event_key,message,created_at,next_at)
select c.id,c.revision,c.subject,c.event_key,c.message,$1,$1 from candidates c
where not exists(select from alert_deliveries d where (d.rule_id,d.rule_revision,d.subject)=(c.id,c.revision,c.subject)
 and d.created_at>$1::timestamptz-make_interval(mins=>c.silence_minutes) and d.state<>'cancelled')
on conflict do nothing

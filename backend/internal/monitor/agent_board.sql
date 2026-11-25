with scoped as materialized (
 select * from monitor_agent_hourly_stats where bucket_hour>=$1 and bucket_hour<$2
 and ($3::uuid is null or team_id=$3) and ($4::text is null or source_type=$4)
), tally as (
 select team_id,agent_id,source_type,sum(calls_total) as calls,sum(calls_succeeded) as success,
  sum(lightweight_total) as lightweight,sum(lightweight_effective) as effective,
  sum(collaborative_total) as collaborative,sum(collaborative_approved) as approved,
  sum(tokens_total) as tokens,sum(prompt_tokens) as prompt,sum(completion_tokens) as completion,
  sum(cached_tokens) as cached,sum(cost_micro_cny) as cost,sum(undisclosed_calls) as undisclosed
 from scoped group by 1,2,3
), buckets as (
 select s.team_id,s.agent_id,s.source_type,b.key::int as ms,sum(b.value::bigint) as samples
 from scoped s cross join lateral jsonb_each_text(s.latency_histogram) b group by 1,2,3,4
), distribution as (
 select *,sum(samples) over(partition by team_id,agent_id,source_type order by ms) as seen,
  sum(samples) over(partition by team_id,agent_id,source_type) as total from buckets
), percentiles as (
 select team_id,agent_id,source_type,min(ms) filter(where seen>=total*0.50) as p50,min(ms) filter(where seen>=total*0.95) as p95
 from distribution group by 1,2,3
), ranked as (
 select t.*,l.p50,l.p95,coalesce(ar.name,a.id::text) as name,t.success/nullif(t.calls,0) as success_rate,
  t.effective/nullif(t.lightweight,0) as effective_rate,t.approved/nullif(t.collaborative,0) as approval_rate,
  row_number() over(order by case $5 when 'success' then t.success/nullif(t.calls,0) when 'effective' then t.effective/nullif(t.lightweight,0)
   when 'approved' then t.approved/nullif(t.collaborative,0) when 'cost' then t.cost when 'p95' then l.p95 else t.calls end desc nulls last,
   t.agent_id,t.team_id,t.source_type) as row_num
 from tally t join agents a on a.id=t.agent_id left join agent_revisions ar on (ar.agent_id,ar.revision)=(a.id,a.published_revision)
 left join percentiles l on (l.team_id,l.agent_id,l.source_type)=(t.team_id,t.agent_id,t.source_type)
)
select coalesce(jsonb_agg(jsonb_build_object('teamId',team_id,'agentId',agent_id,'name',name,'source',source_type,
 'calls',calls,'successfulCalls',success,'successRate',success_rate,'lightweight',lightweight,'effective',effective,'effectiveRate',effective_rate,
 'collaborative',collaborative,'approved',approved,'approvalRate',approval_rate,'p50Ms',p50,'p95Ms',p95,'tokens',tokens,
 'promptTokens',prompt,'completionTokens',completion,'cachedTokens',cached,'costMicroCny',cost::text,'undisclosed',undisclosed)
 order by row_num),'[]') from ranked where row_num>$6 and row_num<=$6+25

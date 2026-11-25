with runs as materialized (
 select * from monitor_run_hourly_stats where bucket_hour >= $1 and bucket_hour < $2 and ($3::uuid is null or team_id=$3)
), calls as materialized (
 select bucket_hour,calls_total,calls_succeeded,prompt_tokens,completion_tokens,cached_tokens,tokens_total,cost_micro_cny,undisclosed_calls
 from monitor_model_hourly_stats where bucket_hour >= $1 and bucket_hour < $2 and ($3::uuid is null or team_id=$3)
 union all
 select bucket_hour,calls_total,calls_succeeded,prompt_tokens,completion_tokens,cached_tokens,tokens_total,cost_micro_cny,undisclosed_calls
 from monitor_agent_hourly_stats where source_type in ('external','tool') and bucket_hour >= $1 and bucket_hour < $2 and ($3::uuid is null or team_id=$3)
), run_days as (
 select (bucket_hour at time zone 'UTC')::date as day,sum(total) as tasks,
  coalesce(sum(total) filter(where status='completed'),0) as completed,
  coalesce(sum(total) filter(where quality_track='lightweight'),0) as lightweight,
  coalesce(sum(accepted) filter(where quality_track='lightweight'),0) as effective,
  coalesce(sum(total) filter(where quality_track='collaborative'),0) as collaborative,
  coalesce(sum(accepted) filter(where quality_track='collaborative'),0) as approved
 from runs group by 1
), call_days as (
 select (bucket_hour at time zone 'UTC')::date as day,sum(tokens_total) as tokens,sum(cost_micro_cny)::text as cost
 from calls group by 1
)
select jsonb_build_object(
 'kpi', (select jsonb_build_object('calls',coalesce(sum(calls_total),0),'successfulCalls',coalesce(sum(calls_succeeded),0),
  'tokens',coalesce(sum(tokens_total),0),'promptTokens',coalesce(sum(prompt_tokens),0),'completionTokens',coalesce(sum(completion_tokens),0),
  'cachedTokens',coalesce(sum(cached_tokens),0),'costMicroCny',coalesce(sum(cost_micro_cny),0)::text,'undisclosed',coalesce(sum(undisclosed_calls),0)) from calls),
 'quality', (select jsonb_build_object('lightweight',jsonb_build_object('total',coalesce(sum(lightweight),0),'met',coalesce(sum(effective),0)),
  'collaborative',jsonb_build_object('total',coalesce(sum(collaborative),0),'met',coalesce(sum(approved),0))) from run_days),
 'funnel', (select jsonb_build_object('submitted',coalesce(sum(total),0),'completed',coalesce(sum(total) filter(where status='completed'),0),
  'outputPassed',coalesce(sum(output_passed),0),'accepted',coalesce(sum(accepted),0)) from runs),
 'taskTrend', (select coalesce(jsonb_agg(to_jsonb(r) order by day),'[]') from run_days r),
 'usageTrend', (select coalesce(jsonb_agg(jsonb_build_object('day',day,'tokens',tokens,'costMicroCny',cost) order by day),'[]') from call_days)
)

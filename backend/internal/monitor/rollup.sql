with facts as materialized (
  select h.bucket_hour,f.*,extract(epoch from f.ended_at-f.started_at)*1000 as latency_ms
  from unnest($1::timestamptz[]) h(bucket_hour)
  join monitor_call_facts f on f.started_at>=h.bucket_hour and f.started_at<h.bucket_hour+interval '1 hour'
), agents as (
  insert into monitor_agent_hourly_stats(bucket_hour,team_id,agent_id,source_type,model_id,tier,runs_total,runs_succeeded,
    calls_total,calls_succeeded,lightweight_total,lightweight_effective,collaborative_total,collaborative_approved,
    p50_ms,p95_ms,prompt_tokens,completion_tokens,cached_tokens,tokens_total,cost_micro_cny,undisclosed_calls)
  select bucket_hour,team_id,agent_id,source_type,model_id,tier,count(distinct run_id),
    count(distinct run_id) filter(where run_succeeded),count(*),count(*) filter(where success),
    count(distinct run_id) filter(where quality_track='lightweight'),count(distinct run_id) filter(where lightweight_effective),
    count(distinct run_id) filter(where quality_track='collaborative'),count(distinct run_id) filter(where collaborative_approved),
    percentile_cont(0.50) within group(order by latency_ms) filter(where latency_ms>=0),
    percentile_cont(0.95) within group(order by latency_ms) filter(where latency_ms>=0),
    coalesce(sum(prompt_tokens),0),coalesce(sum(completion_tokens),0),coalesce(sum(cached_tokens),0),
    coalesce(sum(tokens_total),0),sum(cost_micro_cny),count(*) filter(where undisclosed)
  from facts where agent_id is not null group by bucket_hour,team_id,agent_id,source_type,model_id,tier
)
insert into monitor_model_hourly_stats(bucket_hour,team_id,model_id,provider_id,tier,calls_total,calls_succeeded,calls_failed,
  fallback_count,p50_ms,p95_ms,prompt_tokens,completion_tokens,cached_tokens,tokens_total,cost_micro_cny,undisclosed_calls)
select bucket_hour,team_id,model_id,provider_id,tier,count(*),count(*) filter(where success),count(*) filter(where failure),
  count(*) filter(where fallback),percentile_cont(0.50) within group(order by latency_ms) filter(where latency_ms>=0),
  percentile_cont(0.95) within group(order by latency_ms) filter(where latency_ms>=0),
  coalesce(sum(prompt_tokens),0),coalesce(sum(completion_tokens),0),coalesce(sum(cached_tokens),0),
  coalesce(sum(tokens_total),0),sum(cost_micro_cny),count(*) filter(where undisclosed)
from facts where model_id is not null group by bucket_hour,team_id,model_id,provider_id,tier

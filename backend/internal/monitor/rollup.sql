with facts as materialized (
  select h.bucket_hour,f.*,extract(epoch from f.ended_at-f.started_at)*1000 as latency_ms
  from unnest($1::timestamptz[]) h(bucket_hour)
  join monitor_call_facts f on f.started_at>=h.bucket_hour and f.started_at<h.bucket_hour+interval '1 hour'
), agent_latency as (
  select bucket_hour,team_id,agent_id,source_type,model_id,tier,jsonb_object_agg(upper_ms,samples) as histogram
  from (select bucket_hour,team_id,agent_id,source_type,model_id,tier,monitor_latency_bucket(latency_ms) as upper_ms,count(*) as samples
   from facts where agent_id is not null and latency_ms>=0 group by 1,2,3,4,5,6,7) buckets
  group by 1,2,3,4,5,6
), agents as (
  insert into monitor_agent_hourly_stats(bucket_hour,team_id,agent_id,source_type,model_id,tier,runs_total,runs_succeeded,
    calls_total,calls_succeeded,lightweight_total,lightweight_effective,collaborative_total,collaborative_approved,
    p50_ms,p95_ms,prompt_tokens,completion_tokens,cached_tokens,tokens_total,cost_micro_cny,undisclosed_calls,latency_histogram)
  select f.bucket_hour,f.team_id,f.agent_id,f.source_type,f.model_id,f.tier,count(distinct run_id),
    count(distinct run_id) filter(where run_succeeded),count(*),count(*) filter(where success),
    count(distinct run_id) filter(where quality_track='lightweight'),count(distinct run_id) filter(where lightweight_effective),
    count(distinct run_id) filter(where quality_track='collaborative'),count(distinct run_id) filter(where collaborative_approved),
    percentile_cont(0.50) within group(order by latency_ms) filter(where latency_ms>=0),
    percentile_cont(0.95) within group(order by latency_ms) filter(where latency_ms>=0),
    coalesce(sum(prompt_tokens),0),coalesce(sum(completion_tokens),0),coalesce(sum(cached_tokens),0),
    coalesce(sum(tokens_total),0),sum(cost_micro_cny),count(*) filter(where undisclosed),coalesce(l.histogram,'{}')
  from facts f left join agent_latency l on (f.bucket_hour,f.team_id,f.agent_id,f.source_type)=(l.bucket_hour,l.team_id,l.agent_id,l.source_type)
    and f.model_id is not distinct from l.model_id and f.tier is not distinct from l.tier
  where f.agent_id is not null group by f.bucket_hour,f.team_id,f.agent_id,f.source_type,f.model_id,f.tier,l.histogram
), runs as (
  insert into monitor_run_hourly_stats(bucket_hour,team_id,status,quality_track,total,output_passed,accepted)
  select h.bucket_hour,q.team_id,r.status,q.quality_track,count(*),
    count(*) filter(where r.status='completed' and (
      (q.quality_track='lightweight' and (q.lightweight_effective or exists(select from artifacts a where a.run_id=r.id)))
      or (q.quality_track='collaborative' and exists(select from artifacts a join reviews v on v.artifact_id=a.id where a.run_id=r.id))
    )),count(*) filter(where q.lightweight_effective or q.collaborative_approved)
  from unnest($1::timestamptz[]) h(bucket_hour) join runs r on r.created_at>=h.bucket_hour and r.created_at<h.bucket_hour+interval '1 hour'
  join run_quality_outcomes q on q.run_id=r.id group by h.bucket_hour,q.team_id,r.status,q.quality_track
)
insert into monitor_model_hourly_stats(bucket_hour,team_id,model_id,provider_id,tier,calls_total,calls_succeeded,calls_failed,
  fallback_count,p50_ms,p95_ms,prompt_tokens,completion_tokens,cached_tokens,tokens_total,cost_micro_cny,undisclosed_calls)
select bucket_hour,team_id,model_id,provider_id,tier,count(*),count(*) filter(where success),count(*) filter(where failure),
  count(*) filter(where fallback),percentile_cont(0.50) within group(order by latency_ms) filter(where latency_ms>=0),
  percentile_cont(0.95) within group(order by latency_ms) filter(where latency_ms>=0),
  coalesce(sum(prompt_tokens),0),coalesce(sum(completion_tokens),0),coalesce(sum(cached_tokens),0),
  coalesce(sum(tokens_total),0),sum(cost_micro_cny),count(*) filter(where undisclosed)
from facts where model_id is not null group by bucket_hour,team_id,model_id,provider_id,tier

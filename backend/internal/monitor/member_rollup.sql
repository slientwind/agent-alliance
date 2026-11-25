with hours as (select unnest($1::timestamptz[]) bucket_hour), outcomes as (
 insert into monitor_member_outcome_hourly(bucket_hour,actor,team_id,kind,state,n)
 select h.bucket_hour,e.actor,e.team_id,e.kind,case when e.status in ('queued','running','blocked','reconcile','paused',
  'completed','failed','cancelled') then e.status else 'other' end,count(*)
 from hours h join management_outcome_events e on e.at>=h.bucket_hour and e.at<h.bucket_hour+interval '1 hour'
 where e.actor is not null group by 1,2,3,4,5
), costs as (
 insert into monitor_member_cost_hourly(bucket_hour,actor,team_id,attempts,tokens,unknown,pending,reserved,amount)
 select h.bucket_hour,c.actor,c.team_id,count(*),coalesce(sum(c.tokens),0),count(*) filter(where c.unknown),
  count(*) filter(where c.pending),coalesce(sum(c.reserved),0),coalesce(sum(c.recorded),0)
 from hours h join management_call_costs c on c.at>=h.bucket_hour and c.at<h.bucket_hour+interval '1 hour'
 where c.actor is not null group by 1,2,3
), quality as (
 insert into monitor_member_quality_hourly(bucket_hour,actor,team_id,quality_track,total,met)
 select h.bucket_hour,q.actor,q.team_id,q.quality_track,count(*),count(*) filter(where q.lightweight_effective or q.collaborative_approved)
 from hours h join run_quality_outcomes q on q.created_at>=h.bucket_hour and q.created_at<h.bucket_hour+interval '1 hour'
 group by 1,2,3,4
)
insert into monitor_member_rollup_done(bucket_hour) select bucket_hour from hours on conflict do nothing

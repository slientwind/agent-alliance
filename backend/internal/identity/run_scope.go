package identity

// RunGrantSQL checks the current caller scope of an accepted service run.
const RunGrantSQL = `select r.service_id is null or (r.app_id is null and exists(
 select from services s join team_members team on team.team_id=s.team_id and team.member_id=r.actor
 join project_members pm on pm.project_id=r.project_id and pm.member_id=r.actor join members actor on actor.id=r.actor and actor.enabled
 where s.id=r.service_id)) or exists(
 select from applications a join services s on s.id=a.service_id join members app on app.id=a.id and app.enabled
 join team_members app_grant on app_grant.team_id=s.team_id and app_grant.member_id=app.id
 join members actor on actor.id=r.actor and actor.enabled
 join team_members actor_grant on actor_grant.team_id=s.team_id and actor_grant.member_id=actor.id
 join project_members pm on pm.project_id=r.project_id and pm.member_id=actor.id
 where a.id=r.app_id and a.revoked_at is null and a.expires_at>now() and s.id=r.service_id
 and (r.delegated_by is null or exists(select from delegations d where d.id=r.delegated_by and d.member_id=r.actor and d.app_id=a.id and d.revoked_at is null and d.expires_at>now())))
 from runs r where r.id=$1`

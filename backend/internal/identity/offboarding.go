package identity

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s Server) departurePreview(w http.ResponseWriter, r *http.Request, _ Member) error {
	person := r.PathValue("id")
	if _, err := uuid.Parse(person); err != nil {
		return web.Fail(404, "找不到成员")
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(search) > 120 {
		return web.Fail(422, "搜索词最多 120 字")
	}
	mode, team := r.URL.Query().Get("mode"), r.URL.Query().Get("teamId")
	if mode == "" {
		mode = "depart"
	}
	if mode == "transfer" {
		if !handoffTeamID(team) {
			return web.Fail(422, "请选择要移出的团队空间")
		}
	} else if mode != "depart" || team != "" {
		return web.Fail(422, "未知交接模式")
	}
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `with target as (select nullif($3,'')::uuid as team_id),
	required_teams as materialized (
	 select t.team_id from staff_departure_teams($1) t cross join target where target.team_id is null
	 union select team_id from target where team_id is not null
	)
	select jsonb_build_object(
	 'mode',$4::text,'teamId',target.team_id,'accountRevision',leaving.account_revision::text,
	 'teams',array(select jsonb_build_object('id',t.id,'spaceId',s.id,'name',s.name)
	  from team_members tm join teams t on t.id=tm.team_id join spaces s on s.team_id=t.id and s.archived_at is null
	  where tm.member_id=$1 order by s.name,s.id),
	 'projects',(select count(*) from staff_departure_projects($1) p where target.team_id is null or p.team_id=target.team_id),
	 'agents',(select count(*) from agents where owner=$1 and not removed and (target.team_id is null or team_id=target.team_id)),
	 'drafts',(select count(*) from studio_drafts where owner=$1 and privacy_state='active' and studio_draft_shared(id)
	  and (target.team_id is null or team_id=target.team_id)),
	 'rules',(select count(*) from team_rules where maintainer=$1 and (target.team_id is null or team_id=target.team_id)),
	 'automations',(select count(*) from automation_schedules where owner=$1 and state<>'deleted' and (target.team_id is null or team_id=target.team_id)),
	 'personal',jsonb_build_object(
	  'projects',(select count(*) from projects p join spaces s on s.id=p.space_id
	    where s.kind='personal' and s.owner_member_id=$1 and p.privacy_state='active'),
	  'drafts',(select count(*) from studio_drafts where owner=$1 and privacy_state='active' and not studio_draft_shared(id)),
	  'collections',(select count(*) from kb_collections c join spaces s on s.id=c.space_id
	    where s.kind='personal' and s.owner_member_id=$1 and c.privacy_state='active'),
	  'oboGrants',(select count(*) from business_obo_grants where member_id=$1)),
	 'shared',jsonb_build_object(
	  'collections',(select count(*) from kb_collections c join spaces s on s.id=c.space_id
	    where c.maintainer=$1 and s.kind='team' and (target.team_id is null or s.team_id=target.team_id)),
	  'businessBindings',(select count(*) from business_connector_bindings b join spaces s on s.id=b.space_id
	    where b.owner_id=$1 and s.kind='team' and (target.team_id is null or s.team_id=target.team_id)),
	  'events',(select count(*) from event_triggers t join spaces s on s.id=t.space_id
	    where t.maintainer=$1 and s.kind='team' and (target.team_id is null or s.team_id=target.team_id)),
	  'approvals',(select count(*) from pending_system_mutations a join spaces s on s.id=a.space_id
	    where a.approver_id=$1 and a.status in ('pending','approved') and (target.team_id is null or s.team_id=target.team_id))),
	 'successors',array(select jsonb_build_object('id',m.id,'name',m.display_name,'email',m.email)
	   from members m where m.id<>$1 and m.enabled and m.departed_at is null and m.kind='person'
	   and ($2='' or strpos(lower(m.display_name),lower($2))>0 or strpos(lower(m.email),lower($2))>0)
	   and not exists(select from required_teams t where not exists(
	     select from team_members tm where (tm.team_id,tm.member_id)=(t.team_id,m.id)))
	   order by m.display_name,m.id limit 250))
	from members leaving cross join target where leaving.id=$1 and leaving.kind='person' and leaving.departed_at is null
	 and (target.team_id is null or (leaving.enabled and exists(
	  select from team_members tm join spaces s on s.team_id=tm.team_id and s.archived_at is null
	  where (tm.team_id,tm.member_id)=(target.team_id,leaving.id))))`, person, search, team, mode).Scan(&result)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "成员或团队交接范围已失效")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func handoffTeamID(team string) bool {
	id, err := uuid.Parse(team)
	return err == nil && id != uuid.Nil && id.String() == team
}

func applyDepartureHandoff(ctx context.Context, tx pgx.Tx, actor, departing, successor, reason, team string) error {
	ref, err := uuid.Parse(successor)
	if err != nil || ref == uuid.Nil || ref.String() == departing {
		return web.Fail(422, "请选择另一位在职成员接手")
	}
	successor = ref.String()
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 1000 {
		return web.Fail(422, "请填写交接原因，最多 1000 字")
	}
	var active string
	err = tx.QueryRow(ctx, `select id from members where id=$1 and enabled and departed_at is null and kind='person' for share`, successor).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(409, "接手人已无法履职，请重新选择")
	}
	if err != nil {
		return err
	}
	if team != "" {
		var space string
		if err := tx.QueryRow(ctx, `select s.id from team_members tm join spaces s on s.team_id=tm.team_id and s.archived_at is null
		 where (tm.team_id,tm.member_id)=($1,$2) for update of tm for share of s`, team, departing).Scan(&space); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return web.Fail(409, "成员已移出该团队空间，请刷新后核对")
			}
			return err
		}
	}
	if _, err := tx.Exec(ctx, `select p.id from projects p join staff_departure_projects($1) t on t.project_id=p.id
	 where ($2='' or p.team_id=nullif($2,'')::uuid) order by p.id for update of p`, departing, team); err != nil {
		return err
	}
	for _, query := range []string{
		"select id from agents where owner=$1 and not removed",
		"select id from studio_drafts where owner=$1",
		"select id from team_rules where maintainer=$1",
		"select id from automation_schedules where owner=$1 and state<>'deleted'",
	} {
		// All affected rows are locked before checking the successor's team scope.
		if _, err := tx.Exec(ctx, query+" and ($2='' or team_id=nullif($2,'')::uuid) order by id for update", departing, team); err != nil {
			return err
		}
	}
	const teams = `(select team_id from staff_departure_teams($1) where $3=''
	 union select nullif($3,'')::uuid where $3<>'')`
	if _, err := tx.Exec(ctx, `select team_id from team_members where member_id=$2
	 and team_id in `+teams+` order by team_id for share`, departing, successor, team); err != nil {
		return err
	}
	var covered bool
	if err := tx.QueryRow(ctx, `select not exists(select from `+teams+` t where not exists(
	 select from team_members m where (m.team_id,m.member_id)=(t.team_id,$2)))`, departing, successor, team).Scan(&covered); err != nil {
		return err
	}
	if !covered {
		return web.Fail(409, "接手人须已加入所有相关团队，请重新选择")
	}
	sealed, err := sealPrivateAssets(ctx, tx, departing)
	if err != nil {
		return err
	}
	var projectIDs []string
	if err := tx.QueryRow(ctx, `select array(select project_id from staff_departure_projects($1)
	 where ($2='' or team_id=nullif($2,'')::uuid) order by project_id)`, departing, team).
		Scan(&projectIDs); err != nil {
		return err
	}
	var temporary bool
	if err := tx.QueryRow(ctx, `select exists(select from project_memberships old
	 where old.member_id=$1 and old.project_id=any($3::uuid[]) and old.recovery_assignment is not null
	  and not exists(select from project_memberships next where next.project_id=old.project_id
	    and next.member_id=$2 and next.recovery_assignment is null))`, departing, successor, projectIDs).Scan(&temporary); err != nil {
		return err
	}
	if temporary {
		return web.Fail(409, "临时接管项目须交给已有正式项目访问权的成员")
	}
	if _, err := tx.Exec(ctx, `insert into project_memberships(project_id,member_id,can_review,can_view_costs)
	  select p.id,$2,coalesce(m.can_review,false) or $1 in (p.owner,p.final_reviewer),coalesce(m.can_view_costs,false)
	  from projects p left join project_members m on (m.project_id,m.member_id)=(p.id,$1)
	  where p.id=any($3::uuid[])
	  on conflict(project_id,member_id) do update set can_review=project_memberships.can_review or excluded.can_review,
	   can_view_costs=project_memberships.can_view_costs or excluded.can_view_costs,recovery_assignment=null`,
		departing, successor, projectIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update projects set owner=case when owner=$1 then $2 else owner end,
	  final_reviewer=case when final_reviewer=$1 then $2 else final_reviewer end,
	  reviewer_assignment=case when final_reviewer=$1 then null else reviewer_assignment end,
	  revision=revision+1,updated_at=clock_timestamp() where id=any($3::uuid[])`, departing, successor, projectIDs); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update reviewer_handoffs set state='superseded',decided_at=clock_timestamp(),decision_by=$3,decision_reason=$4
	  where project_id=any($2::uuid[]) and state='pending' and previous_reviewer=$1`, departing, projectIDs, actor, reason); err != nil {
		return err
	}
	var counts struct {
		Feedback, Mentions, Requirements, Agents, Rules, Automations int64
	}
	feedback, err := tx.Exec(ctx, `with changed as (
	 update feedback f set reviewer=$2,revision=f.revision+1 from artifacts a
	  where a.id=f.artifact_id and a.project_id=any($4::uuid[]) and f.reviewer=$1
	   and f.state in ('suggested','required','investigate','verification')
	  returning f.id,f.revision,f.base_version
	) insert into feedback_events(feedback_id,revision,actor,action,reason,artifact_version)
	 select id,revision,$3,'transfer',$5,base_version from changed`, departing, successor, actor, projectIDs, reason)
	if err != nil {
		return err
	}
	counts.Feedback = feedback.RowsAffected()
	mentions, err := tx.Exec(ctx, `with moved as (
	 update artifact_comment_mentions m set transferred_at=clock_timestamp()
	  from artifact_comments c join artifacts a on a.id=c.artifact_id
	  where m.comment_id=c.id and m.member_id=$1 and m.transferred_at is null
	   and c.resolved_at is null and a.project_id=any($3::uuid[]) and artifact_comment_readable(c.id,$2)
	  returning m.comment_id)
	 insert into artifact_comment_mentions(comment_id,member_id)
	 select comment_id,$2 from moved on conflict (comment_id,member_id) do update set transferred_at=null`, departing, successor, projectIDs)
	if err != nil {
		return err
	}
	counts.Mentions = mentions.RowsAffected()
	requirements, err := tx.Exec(ctx, `with current as (
	 select distinct on (q.project_id,q.plan_version,q.item) q.* from delivery_item_assessments q
	  join projects p on (p.id,p.plan_version)=(q.project_id,q.plan_version)
	  where p.id=any($3::uuid[]) order by q.project_id,q.plan_version,q.item,q.id desc
	) insert into delivery_item_assessments(project_id,plan_version,item,run_id,artifact_id,artifact_version,
	 state,reason,responsible,continue_when,reviewer,file_evidence,remote_receipt_version,upstream_evidence_step)
	 select project_id,plan_version,item,run_id,artifact_id,artifact_version,state,reason,$2,continue_when,
	  case when reviewer=$1 then $2 else reviewer end,file_evidence,remote_receipt_version,upstream_evidence_step
	 from current where responsible=$1 and state='missing'`, departing, successor, projectIDs)
	if err != nil {
		return err
	}
	counts.Requirements = requirements.RowsAffected()
	for _, change := range []struct {
		sql   string
		count *int64
	}{
		{"update agents set owner=$2,maintenance_vacant=false,maintenance_version=maintenance_version+1,proposed_owner=null,proposed_by=null,transfer_reason=null where owner=$1 and not removed", &counts.Agents},
		{"update team_rules set maintainer=$2,maintenance_vacant=false,maintenance_version=maintenance_version+1,proposed_owner=null,proposed_by=null,transfer_reason=null where maintainer=$1", &counts.Rules},
		{"update automation_schedules set owner=$2,revision=revision+1,sync_next_at=clock_timestamp(),updated_at=clock_timestamp() where owner=$1 and state<>'deleted'", &counts.Automations},
	} {
		tag, err := tx.Exec(ctx, change.sql+" and ($3='' or team_id=nullif($3,'')::uuid)", departing, successor, team)
		if err != nil {
			return err
		}
		*change.count = tag.RowsAffected()
	}
	shared, err := transferSharedAssets(ctx, tx, departing, successor, team)
	if err != nil {
		return err
	}
	detail := map[string]any{"projects": projectIDs, "feedback": counts.Feedback, "mentions": counts.Mentions, "requirements": counts.Requirements,
		"agents": counts.Agents, "rules": counts.Rules, "automations": counts.Automations, "personal": sealed, "shared": shared}
	action := "member.departure.handoff"
	if team != "" {
		detail["teamId"] = team
		action = "member.transfer.handoff"
		if err := leaveTeamScope(ctx, tx, departing, team); err != nil {
			return err
		}
	}
	var batch string
	if err := tx.QueryRow(ctx, `insert into staff_departure_batches(departing,successor,actor,reason,transferred)
	 values($1,$2,$3,$4,$5) returning id`, departing, successor, actor, reason, detail).Scan(&batch); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, action, departing, map[string]any{"batch": batch, "successor": successor, "teamId": team})
}

func leaveTeamScope(ctx context.Context, tx pgx.Tx, member, team string) error {
	for _, statement := range []string{
		`delete from project_memberships pm using projects p, spaces s
		 where pm.project_id=p.id and p.space_id=s.id and pm.member_id=$1 and s.team_id=$2`,
		`delete from trace_grants g using runs r, projects p, spaces s
		 where g.run_id=r.id and r.project_id=p.id and p.space_id=s.id and g.member_id=$1 and s.team_id=$2`,
		`delete from channel_contexts c using projects p, spaces s
		 where c.project_id=p.id and p.space_id=s.id and c.member_id=$1 and s.team_id=$2`,
		`update teams set trace_manager=null,trace_policy_revision=trace_policy_revision+1 where trace_manager=$1 and id=$2`,
		`delete from team_members where member_id=$1 and team_id=$2`,
	} {
		if _, err := tx.Exec(ctx, statement, member, team); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "update members set account_revision=account_revision+1 where id=$1", member)
	return err
}

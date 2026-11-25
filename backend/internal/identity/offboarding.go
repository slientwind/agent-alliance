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
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `with teams as materialized (select team_id from staff_departure_teams($1))
	select jsonb_build_object(
	 'projects',(select count(*) from staff_departure_projects($1)),
	 'agents',(select count(*) from agents where owner=$1 and not removed),
	 'drafts',(select count(*) from studio_drafts where owner=$1),
	 'rules',(select count(*) from team_rules where maintainer=$1),
	 'automations',(select count(*) from automation_schedules where owner=$1 and state<>'deleted'),
	 'successors',array(select jsonb_build_object('id',m.id,'name',m.display_name,'email',m.email)
	   from members m where m.id<>$1 and m.enabled and m.departed_at is null and m.kind='person'
	   and ($2='' or strpos(lower(m.display_name),lower($2))>0 or strpos(lower(m.email),lower($2))>0)
	   and not exists(select from teams t where not exists(
	     select from team_members tm where (tm.team_id,tm.member_id)=(t.team_id,m.id)))
	   order by m.display_name,m.id limit 250))
	from members leaving where leaving.id=$1 and leaving.kind='person' and leaving.departed_at is null`, person, search).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func applyDepartureHandoff(ctx context.Context, tx pgx.Tx, actor, departing, successor, reason string) error {
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
	if _, err := tx.Exec(ctx, `select p.id from projects p join staff_departure_projects($1) t on t.project_id=p.id
	 order by p.id for update of p`, departing); err != nil {
		return err
	}
	for _, query := range []string{
		"select id from agents where owner=$1 and not removed order by id for update",
		"select id from studio_drafts where owner=$1 order by id for update",
		"select id from team_rules where maintainer=$1 order by id for update",
		"select id from automation_schedules where owner=$1 and state<>'deleted' order by id for update",
	} {
		// All affected rows are locked before checking the successor's team scope.
		if _, err := tx.Exec(ctx, query, departing); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `select team_id from team_members where member_id=$2
	 and team_id in (select team_id from staff_departure_teams($1)) order by team_id for share`, departing, successor); err != nil {
		return err
	}
	var covered bool
	if err := tx.QueryRow(ctx, `select not exists(select from staff_departure_teams($1) t where not exists(
	 select from team_members m where (m.team_id,m.member_id)=(t.team_id,$2)))`, departing, successor).Scan(&covered); err != nil {
		return err
	}
	if !covered {
		return web.Fail(409, "接手人须已加入所有相关团队，请重新选择")
	}
	var projectIDs []string
	if err := tx.QueryRow(ctx, `select array(select project_id from staff_departure_projects($1) order by project_id)`, departing).
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
		Feedback, Mentions, Requirements, Agents, Drafts, Rules, Automations int64
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
		{"update studio_drafts set owner=$2,revision=revision+1,updated_at=clock_timestamp() where owner=$1", &counts.Drafts},
		{"update team_rules set maintainer=$2,maintenance_vacant=false,maintenance_version=maintenance_version+1,proposed_owner=null,proposed_by=null,transfer_reason=null where maintainer=$1", &counts.Rules},
		{"update automation_schedules set owner=$2,state='paused',revision=revision+1,sync_next_at=clock_timestamp(),updated_at=clock_timestamp() where owner=$1 and state<>'deleted'", &counts.Automations},
	} {
		tag, err := tx.Exec(ctx, change.sql, departing, successor)
		if err != nil {
			return err
		}
		*change.count = tag.RowsAffected()
	}
	detail := map[string]any{"projects": projectIDs, "feedback": counts.Feedback, "mentions": counts.Mentions, "requirements": counts.Requirements,
		"agents": counts.Agents, "drafts": counts.Drafts, "rules": counts.Rules, "pausedAutomations": counts.Automations}
	var batch string
	if err := tx.QueryRow(ctx, `insert into staff_departure_batches(departing,successor,actor,reason,transferred)
	 values($1,$2,$3,$4,$5) returning id`, departing, successor, actor, reason, detail).Scan(&batch); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "member.departure.handoff", departing, map[string]any{"batch": batch, "successor": successor})
}

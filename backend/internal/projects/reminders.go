package projects

import (
	"context"
	"net/http"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type ReviewReminder struct {
	Project        string    `json:"project"`
	Title          string    `json:"title"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	Version        int       `json:"version"`
	Clock          string    `json:"clock"`
	Revision       int       `json:"revision"`
	Phase          string    `json:"phase"`
	Opened         time.Time `json:"opened"`
	OriginalDue    time.Time `json:"originalDue"`
	Due            time.Time `json:"due"`
	Overdue        bool      `json:"overdue"`
	Owner          bool      `json:"owner"`
	Subject        string    `json:"subject"`
	ActionRevision int       `json:"actionRevision"`
	Step           int       `json:"step"`
	CanHandle      bool      `json:"canHandle"`
	Assignees      []string  `json:"assignees"`
}

func (s Server) reminders(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	var items []ReviewReminder
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select sync_review_clocks(p.id) from projects p join project_members m on m.project_id=p.id
            where p.stage='review' and m.member_id=$1 order by p.id`, u.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select c.project_id,p.title,c.kind,c.name,c.version,c.clock,c.clock_revision,t.phase,c.opened_at,c.original_due,c.due_at,
           now()>=c.due_at,c.owner=$1,c.subject,c.revision,c.ordinal,$1=any(c.recipients),array(select m.display_name from members m where m.id=any(c.recipients) order by m.display_name,m.id)
           from review_reminder_targets t join live_review_clocks c using(clock,clock_revision,project_id) join projects p on p.id=c.project_id
           where t.recipient=$1 and project_space_member(p.id,$1) order by c.due_at,p.id,c.ordinal,c.clock`, u.ID)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ReviewReminder])
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}

func (s Server) extendReview(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected int
		Due      time.Time
		Reason   string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) || in.Due.IsZero() {
		return web.Fail(422, "请填写新的截止时间和延期原因")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Owner != u.ID {
			return web.Fail(403, "需要项目发起人调整审核期限")
		}
		if _, err = tx.Exec(ctx, "select sync_review_clocks($1)", p.ID); err != nil {
			return err
		}
		var old, original time.Time
		var rev int
		var label string
		clock := r.PathValue("clock")
		err = tx.QueryRow(ctx, "select due_at,original_due,clock_revision,case when kind='deliver' then '最终交付' else name||' · v'||version end from live_review_clocks where clock=$1 and project_id=$2", clock, p.ID).Scan(&old, &original, &rev, &label)
		if err != nil {
			return err
		}
		if rev != in.Expected {
			return web.Fail(409, "期限已变化，请刷新后核对")
		}
		if !in.Due.After(old) || !in.Due.After(time.Now()) {
			return web.Fail(422, "新的期限须晚于当前期限及当前时间")
		}
		if _, err = tx.Exec(ctx, "update review_clocks set due_at=$1,revision=revision+1 where id=$2", in.Due, clock); err != nil {
			return err
		}
		if err = Note(ctx, tx, p.ID, "system", label+" 的审核期限由 "+old.UTC().Format(time.RFC3339)+" 延至 "+in.Due.UTC().Format(time.RFC3339)+" · "+in.Reason, u.ID); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "review.deadline_extended", clock, map[string]any{"project": p.ID, "original": original, "previous": old, "due": in.Due, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

// Do not hold a transaction across the entire instance. If processing stops,
// the next scan resumes from current business state.
func (s Server) SyncReviewClocks(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `select id from projects where stage='review' union select project_id from review_clocks where closed_at is null order by 1`)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.DB.Exec(ctx, "select sync_review_clocks($1)", id); err != nil {
			return err
		}
	}
	return nil
}

type reviewPolicy struct {
	Hours      int    `json:"hours"`
	Lead       int    `json:"lead"`
	Escalation int    `json:"escalation"`
	Revision   int    `json:"revision"`
	Source     string `json:"source"`
}

func (s Server) reviewPolicy(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	team, project := r.PathValue("team"), r.PathValue("project")
	var input struct {
		Hours, Lead, Escalation, Expected int
		Reason                            string
	}
	write := r.Method == "PUT"
	if write {
		if err := web.Read(r, &input); err != nil {
			return err
		}
		if input.Hours < 1 || input.Hours > 8760 || input.Lead < 0 || input.Lead >= input.Hours || input.Escalation < 0 || input.Escalation > 8760 || web.Blank(input.Reason) {
			return web.Fail(422, "请核对审核时限、提醒时间和变更原因")
		}
	}
	ctx := r.Context()
	var policy reviewPolicy
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if project != "" {
			if err := tx.QueryRow(ctx, "select p.team_id"+access+"and p.id=$2", u.ID, project).Scan(&team); err != nil {
				return err
			}
		} else {
			var role string
			if err := tx.QueryRow(ctx, `select m.role from members m join team_members tm on tm.member_id=m.id
             where m.enabled and m.id=$1 and tm.team_id=$2 for share of tm,m`, u.ID, team).Scan(&role); err != nil {
				return err
			}
			if write && role != "admin" {
				return web.Fail(403, "需要团队内的实例管理员配置默认期限")
			}
		}
		// Serialize initial creation too, when there is no policy row to lock.
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended('review-policy:'||$1,0))", team); err != nil {
			return err
		}
		// Team-wide changes may snapshot several projects. Always take the team
		// policy lock first, then check current project authority under its row lock.
		if project != "" {
			var owner string
			if err := tx.QueryRow(ctx, "select p.owner"+access+"and p.id=$2 and p.team_id=$3 for no key update of p", u.ID, project, team).Scan(&owner); err != nil {
				return err
			}
			if write && owner != u.ID {
				return web.Fail(403, "需要项目发起人调整")
			}
		}
		err := tx.QueryRow(ctx, `select coalesce(p.hours,t.hours,24),coalesce(p.lead_hours,t.lead_hours,2),coalesce(p.escalation_hours,t.escalation_hours,4),
         case when $2='' then coalesce(t.revision,0) else coalesce(p.revision,0) end,
         case when p.project_id is not null then 'project' when t.team_id is not null then 'team' else 'default' end
         from (values(1)) root(n) left join review_policies t on t.team_id=$1 left join review_policies p on p.project_id=nullif($2,'')::uuid`, team, project).Scan(&policy.Hours, &policy.Lead, &policy.Escalation, &policy.Revision, &policy.Source)
		if err != nil || !write {
			return err
		}
		if input.Expected != policy.Revision {
			return web.Fail(409, "审核节奏已更新，请重新读取")
		}
		// Snapshot open items under the previous policy before changing defaults.
		if _, err = tx.Exec(ctx, `select sync_review_clocks(id) from projects where stage='review' and team_id=$1 and ($2='' or id=nullif($2,'')::uuid) order by id`, team, project); err != nil {
			return err
		}
		key, id := "team_id", team
		if project != "" {
			key, id = "project_id", project
		}
		_, err = tx.Exec(ctx, "insert into review_policies("+key+",hours,lead_hours,escalation_hours) values($1,$2,$3,$4) on conflict("+key+") do update set hours=excluded.hours,lead_hours=excluded.lead_hours,escalation_hours=excluded.escalation_hours,revision=review_policies.revision+1", id, input.Hours, input.Lead, input.Escalation)
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "review.policy", id, map[string]any{"scope": key, "prior": policy, "hours": input.Hours, "lead": input.Lead, "escalation": input.Escalation, "reason": input.Reason})
	})
	if err != nil {
		return err
	}
	if write {
		w.WriteHeader(204)
		return nil
	}
	return web.JSON(w, 200, policy)
}

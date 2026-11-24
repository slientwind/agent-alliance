package memory

import (
	"alliance/internal/web"
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

const RecallLimit = 32 << 10

type pinnedEntry struct {
	Kind     string
	ID       string
	Revision int
	Usable   bool
	Body     *string
	Scoped   bool
	Optional bool
}

type RuleUnavailable struct {
	ID       string
	Revision int
}

func (r *RuleUnavailable) Error() string { return r.Unwrap().Error() }
func (r *RuleUnavailable) Unwrap() error {
	return web.Fail(409, "此步骤采用的团队规则到期、停用或授权撤销")
}

// Recall reads only the run's explicitly pinned personal and team memory.
// Project files and passages remain the responsibility of the knowledge store.
func Recall(ctx context.Context, tx pgx.Tx, run string) (string, error) {
	text, _, err := RecallWithReferences(ctx, tx, run, 0, false)
	return text, err
}

// RecallWithReferences locks scoped records only when preparing an inference.
// Read-only projections recheck the already captured working set.
func RecallWithReferences(ctx context.Context, tx pgx.Tx, run string, step int, capture bool) (string, []Reference, error) {
	lockScoped := ""
	if capture {
		lockScoped = "for share of pref"
	}
	rows, err := tx.Query(ctx, `with scoped_preferences as materialized (
        select 'preference'::text kind,p.preference_id as id,p.revision,pref.body,
          p.revision=pref.revision and p.agent_scope is not distinct from pref.agent_scope and pref.enabled and pref.deleted_at is null
            and not pref.departure_guard and pref.body is not null and pref.expires_at>statement_timestamp() usable,
          true as scoped,true as optional
        from runs r join plan_scoped_preferences p on(p.project_id,p.plan_version)=(r.project_id,r.plan_version)
        join effective_run_nodes n on(n.run_id,n.ordinal,n.agent_id)=(r.id,$2,p.agent_scope)
        join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision) and a.tool is null
        join preferences pref on(pref.id,pref.member_id)=(p.preference_id,r.actor)
        where r.id=$1 and run_memory_can_use_personal(r.id)
        `+lockScoped+`
      )
      select kind,id,revision,usable,case when usable then body end,scoped,optional
      from (
        select 'preference'::text kind,p.preference_id as id,p.revision,pref.body,
            p.revision=pref.revision and pref.agent_scope is null and pref.enabled and pref.deleted_at is null and not pref.departure_guard
              and pref.member_id=r.actor and pref.body is not null and pref.expires_at>statement_timestamp() usable,
            false as scoped,r.skip_unavailable_preferences as optional
        from runs r join plan_preferences p on(p.project_id,p.plan_version)=(r.project_id,r.plan_version)
        join preferences pref on pref.id=p.preference_id
        where r.id=$1 and run_memory_can_use_personal(r.id)
        union all
        select * from scoped_preferences
        union all
        select 'rule'::text,p.rule_id,p.rule_revision,v.body,tr.team_id=project.team_id and rule_usable(p.rule_id,p.rule_revision,r.actor),false,false
        from runs r join plan_rules p on(p.project_id,p.plan_version)=(r.project_id,r.plan_version)
        join rule_versions v on(v.rule_id,v.revision)=(p.rule_id,p.rule_revision)
        join team_rules tr on tr.id=p.rule_id join projects project on project.id=r.project_id
        where r.id=$1 and $2=0 and cardinality(v.agent_scope)=0
        union all
        select 'rule'::text,p.rule_id,p.rule_revision,v.body,p.usable,p.scoped,false
        from effective_run_rules p join rule_versions v on(v.rule_id,v.revision)=(p.rule_id,p.rule_revision)
        where (p.run_id,p.ordinal)=($1,$2)
      ) pinned order by scoped,kind,id`, run, step)
	if err != nil {
		return "", nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pinnedEntry])
	if err != nil {
		return "", nil, err
	}
	text, err := formatRecall(items)
	if err != nil {
		return "", nil, err
	}
	refs := make([]Reference, 0, len(items))
	for _, entry := range items {
		if entry.Usable && entry.Body != nil {
			refs = append(refs, Reference{Kind: entry.Kind, ID: entry.ID, Revision: entry.Revision})
		}
	}
	return text, refs, nil
}

func formatRecall(items []pinnedEntry) (string, error) {
	var personal, forAgent, team []string
	size := 0
	for _, item := range items {
		if !item.Usable || item.Body == nil {
			if item.Kind == "rule" {
				if item.Scoped {
					return "", &RuleUnavailable{ID: item.ID, Revision: item.Revision}
				}
				return "", web.Fail(409, "已采用的团队规则到期、停用或授权撤销")
			}
			if item.Scoped || item.Optional {
				continue
			}
			return "", web.Fail(409, "已采用的个人偏好发生变化")
		}
		size += len(*item.Body) + 1
		if size > RecallLimit {
			return "", web.Fail(409, "本方案采用的记忆超过上下文容量，请减少个人偏好或联系规则维护人")
		}
		if item.Kind == "rule" {
			team = append(team, *item.Body)
		} else if item.Scoped {
			forAgent = append(forAgent, *item.Body)
		} else {
			personal = append(personal, *item.Body)
		}
	}
	var out strings.Builder
	if len(personal) != 0 {
		out.WriteString("\n用户明确采用的偏好\n")
		out.WriteString(strings.Join(personal, "\n"))
	}
	if len(forAgent) != 0 {
		out.WriteString("\n当前 Agent 的个人偏好（服从本次要求及团队规则。成果中无需复述偏好配置。）\n")
		out.WriteString(strings.Join(forAgent, "\n"))
	}
	if len(team) != 0 {
		out.WriteString("\n适用的已发布团队规则（优先于个人偏好）\n")
		out.WriteString(strings.Join(team, "\n"))
	}
	return out.String(), nil
}

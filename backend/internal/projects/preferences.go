package projects

import (
	"context"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func bindPlanPreference(ctx context.Context, tx pgx.Tx, project string, version int, actor, id string, revision int) error {
	var agent *string
	err := tx.QueryRow(ctx, `select agent_scope from preferences
        where id=$1 and member_id=$2 and revision=$3 and enabled and not departure_guard
          and body is not null and deleted_at is null and expires_at>now() for share`, id, actor, revision).Scan(&agent)
	if err == pgx.ErrNoRows {
		return web.Fail(409, "个人偏好已失效")
	}
	if err != nil {
		return err
	}
	if agent == nil {
		_, err := tx.Exec(ctx, "insert into plan_preferences values($1,$2,$3,$4)", project, version, id, revision)
		return err
	}
	tag, err := tx.Exec(ctx, `insert into plan_scoped_preferences(project_id,plan_version,preference_id,revision,agent_scope)
        select $1,$2,$3,$4,$5 where exists(
          select from nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
          where (n.project_id,n.plan_version,n.agent_id)=($1,$2,$5) and a.tool is null
          union all
          select from plan_tool_choices c join agent_revisions a on(a.agent_id,a.revision)=(c.alternative,c.alternative_revision)
          where (c.project_id,c.plan_version,c.alternative)=($1,$2,$5) and a.tool is null)`, project, version, id, revision, *agent)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return web.Fail(422, "此个人偏好限定的 Agent 没有纳入当前方案")
	}
	return nil
}

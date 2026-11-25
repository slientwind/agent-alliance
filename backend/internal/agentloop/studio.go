package agentloop

import (
	"context"
	"slices"

	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func applyStudioRelease(ctx context.Context, tx pgx.Tx, project, actor string, in *StartInput) error {
	_, err := uuid.Parse(in.StudioRelease)
	if err != nil || in.Direct || in.Capabilities != nil || in.Tier != "" {
		return web.Fail(422, "请选择一个已保存的助手")
	}
	var enabled []string
	var pinned []externalRef
	err = tx.QueryRow(ctx, `select spec->>'tier',array(select jsonb_array_elements_text(spec->'capabilities')),
	 dependencies->'externalAgents' from studio_releases where id=$1 and studio_release_usable(id,$2,$3) for share`,
		in.StudioRelease, actor, project).Scan(&in.Tier, &enabled, &pinned)
	if err == pgx.ErrNoRows {
		return web.Fail(409, "此助手已下架或当前对话没有使用权限")
	}
	if err != nil {
		return err
	}
	in.Capabilities = &enabled
	for _, selected := range in.External {
		if !slices.Contains(pinned, selected) {
			return web.Fail(409, "所选外部 Agent 没有包含在此助手的发布清单中")
		}
	}
	if in.External == nil && len(pinned) > 0 {
		in.External = pinned
	}
	return nil
}

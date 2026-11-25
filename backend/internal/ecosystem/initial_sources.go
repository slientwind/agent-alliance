package ecosystem

import (
	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/web"
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

type InitialSource struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	kind, endpoint string
	config         SourceConfig
}

var initialSources = []InitialSource{
	{ID: "mcp", Name: "MCP 官方目录", kind: MCPRegistry, endpoint: "https://registry.modelcontextprotocol.io/v0.1/servers"},
	{ID: "anthropic", Name: "Anthropic Skills 示例库", kind: SkillsGit, endpoint: "https://api.github.com/repos/anthropics/skills",
		config: SourceConfig{Ref: "main", Root: "skills"}},
}

func InitialSources() []InitialSource { return slices.Clone(initialSources) }

func ConfigureInitialSources(ctx context.Context, tx pgx.Tx, actor string, selected []string) error {
	selected = slices.Clone(selected)
	slices.Sort(selected)
	for _, name := range slices.Compact(selected) {
		i := slices.IndexFunc(initialSources, func(source InitialSource) bool { return source.ID == name })
		if i == -1 {
			return web.Fail(422, "请选择引导中提供的生态扫描源")
		}
		source := initialSources[i]
		if err := egress.AllowPublicEndpoint(ctx, tx, source.endpoint); err != nil {
			if errors.Is(err, egress.ErrBlocked) {
				return web.Fail(409, "所选扫描源的访问已被管理员停用")
			}
			return err
		}
		var id string
		err := tx.QueryRow(ctx, `insert into ecosystem_sources(name,kind,endpoint,source_config,cron,time_zone,created_by)
		    values($1,$2,$3,$4,'17 2 * * *','Asia/Shanghai',$5) on conflict(kind,endpoint) do nothing returning id`,
			source.Name, source.kind, source.endpoint, source.config, actor).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if err := database.Audit(ctx, tx, actor, "ecosystem.source.initial_setup", id, map[string]any{"preset": source.ID}); err != nil {
			return err
		}
	}
	return nil
}

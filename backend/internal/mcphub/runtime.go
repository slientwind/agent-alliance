package mcphub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"alliance/internal/harness"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Runtime struct {
	DB     *pgxpool.Pool
	Client Client
}

type RunTool struct {
	Tool
	Server    string
	Digest    string
	Recipient string
}

const allowedRunServers = ` from general_agent_mcp_servers binding
	join runs r on r.id=binding.run_id
	join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version)
	join members actor on actor.id=r.actor and actor.enabled and actor.kind='person'
	join project_members pm on (pm.project_id,pm.member_id)=(p.id,actor.id)
	join team_members tm on (tm.team_id,tm.member_id)=(r.charge_team,actor.id)
	join mcp_team_grants g on (g.server_id,g.team_id,g.revision)=(binding.server_id,r.charge_team,binding.grant_revision)
	join mcp_servers s on (s.id,s.revision)=(binding.server_id,binding.server_revision)
	where binding.run_id=$1 and r.status='running' and g.enabled and s.enabled`

// BindRun freezes the enterprise configuration and the team's authorization
// revisions. Every subsequent outbound request rechecks both current grants.
func BindRun(ctx context.Context, tx pgx.Tx, run, team string) error {
	_, err := tx.Exec(ctx, `insert into general_agent_mcp_servers(run_id,server_id,server_revision,grant_revision)
		select $1,s.id,s.revision,g.revision from mcp_team_grants g join mcp_servers s on s.id=g.server_id
		where g.team_id=$2 and g.enabled and s.enabled and s.tools<>'{}' order by s.id`, run, team)
	return err
}

func (runtime Runtime) openRunServer(ctx context.Context, run, server string) (*Session, error) {
	config := ServerConfig{ID: server}
	query := allowedRunServers + " and s.id=$2"
	err := runtime.DB.QueryRow(ctx, "select s.endpoint,s.transport,s.encrypted_bearer,s.tools,s.allow_private_http"+query, run, server).
		Scan(&config.Endpoint, &config.Transport, &config.EncryptedBearer, &config.AllowedTools, &config.AllowPrivateHTTP)
	if err != nil {
		return nil, ErrUnavailable
	}
	config.authorized = func(ctx context.Context) bool {
		return runtime.DB.QueryRow(ctx, "select 1"+query, run, server).Scan(new(int)) == nil
	}
	return runtime.Client.Open(ctx, config)
}

func (runtime Runtime) Discover(ctx context.Context, run string) []RunTool {
	ctx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	rows, err := runtime.DB.Query(ctx, "select s.id,s.endpoint"+allowedRunServers+" order by s.id limit 16", run)
	if err != nil {
		return nil
	}
	type directoryServer struct{ id, endpoint string }
	servers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (directoryServer, error) {
		var server directoryServer
		err := row.Scan(&server.id, &server.endpoint)
		return server, err
	})
	if err != nil {
		return nil
	}
	var tools []RunTool
	remaining := 48 << 10
	for _, server := range servers {
		if ctx.Err() != nil {
			break
		}
		endpoint, err := url.Parse(server.endpoint)
		if err != nil {
			continue
		}
		session, err := runtime.openRunServer(ctx, run, server.id)
		if err != nil {
			continue
		}
		discovered, err := session.Tools(ctx)
		session.Close()
		if err != nil {
			continue
		}
		for _, tool := range discovered {
			size := len(tool.Definition.Function.Parameters) + len(tool.Definition.Function.Description) + len(tool.Definition.Function.Name)
			if tool.Level == harness.L2 || size > remaining {
				continue
			}
			digest, err := DefinitionDigest(tool)
			if err == nil {
				tools = append(tools, RunTool{Tool: tool, Server: server.id, Digest: digest, Recipient: endpoint.Host})
				remaining -= size
			}
			if len(tools) == 40 {
				return tools
			}
		}
	}
	return tools
}

func (runtime Runtime) Call(ctx context.Context, run, server, alias, digest string, level harness.AutonomyLevel,
	arguments json.RawMessage, approve func(context.Context) error) (json.RawMessage, error) {
	session, err := runtime.openRunServer(ctx, run, server)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	result, err := session.Call(ctx, alias, arguments, func(ctx context.Context, tool Tool, _ json.RawMessage) error {
		current, err := DefinitionDigest(tool)
		if err != nil || current != digest || tool.Level != level {
			return errors.New("工具配置变更，需要重新选择")
		}
		if level != harness.L0 {
			if approve == nil {
				return ErrApproval
			}
			return approve(ctx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !session.server.authorized(ctx) {
		return nil, ErrUnavailable
	}
	return result, nil
}

func DefinitionDigest(tool Tool) (string, error) {
	data, err := json.Marshal(tool)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

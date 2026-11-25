package websearch

import (
	"context"

	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Runtime struct {
	DB     *pgxpool.Pool
	Client Client
}

func queryScope(run string) string { return "general-run/" + run + "/web-search-query" }

func BindRun(ctx context.Context, tx pgx.Tx, vault resources.Vault, run, query string) error {
	if query == "" {
		return nil
	}
	cipher, err := vault.Seal(query, queryScope(run))
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `insert into general_agent_web_search(run_id,service_revision,query_cipher)
	 select $1,revision,$2 from web_search_integration where enabled`, run, cipher)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return web.Fail(409, "企业联网搜索暂不可用")
	}
	return nil
}

func Available(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, run string) (bool, error) {
	var available bool
	err := db.QueryRow(ctx, `select general_web_search_usable($1) and not exists(
	 select from general_agent_actions where run_id=$1 and tool_name='web_search' and
	  state in ('running','completed','rejected'))`, run).Scan(&available)
	return available, err
}

func (runtime Runtime) Search(ctx context.Context, run string) ([]Result, error) {
	var config Config
	var queryCipher []byte
	err := runtime.DB.QueryRow(ctx, `select s.provider,s.endpoint,s.encrypted_key,w.query_cipher
	 from general_agent_web_search w join web_search_integration s on s.revision=w.service_revision
	 where w.run_id=$1 and general_web_search_usable($1)`, run).Scan(&config.Provider, &config.Endpoint, &config.Credential, &queryCipher)
	if err != nil {
		return nil, ErrUnavailable
	}
	query, err := runtime.Client.Vault.Open(queryCipher, queryScope(run))
	if err != nil {
		return nil, ErrUnavailable
	}
	return runtime.Client.Search(ctx, config, query, func(ctx context.Context) bool {
		var allowed bool
		err := runtime.DB.QueryRow(ctx, "select general_web_search_usable($1)", run).Scan(&allowed)
		return err == nil && allowed
	})
}

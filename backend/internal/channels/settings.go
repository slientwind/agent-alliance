package channels

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type botFields struct {
	App    string `json:"app"`
	Tenant string `json:"tenant"`
	Bot    string `json:"bot"`
}

type botSecrets struct {
	Secret  string `json:"secret"`
	Encrypt string `json:"encrypt"`
	Verify  string `json:"verify"`
}

type botSetting struct {
	Provider       string    `json:"provider"`
	Enabled        bool      `json:"enabled"`
	Fields         botFields `json:"fields"`
	Revision       int       `json:"revision"`
	HasCredentials bool      `json:"hasCredentials"`
}

type botRuntime struct {
	feishu   Feishu
	slack    Slack
	ding     DingTalk
	versions [3]int
	routes   *http.ServeMux
	cancel   context.CancelFunc
	context  context.Context
}

type Bots struct {
	DB       *pgxpool.Pool
	Vault    resources.Vault
	identity identity.Server
	parent   context.Context
	mu       sync.Mutex
	current  atomic.Pointer[botRuntime]
}

var botProviders = [...]string{"feishu", "slack", "dingtalk"}

func botScope(provider string) string { return "integration/im-bot/" + provider }

func (b *Bots) Routes(ctx context.Context, mux *http.ServeMux, auth identity.Server) {
	b.identity, b.parent = auth, ctx
	for _, provider := range botProviders {
		mux.Handle("/api/channels/"+provider+"/", http.HandlerFunc(b.serveHTTP))
	}
	mux.Handle("GET /api/admin/channels/feishu/review-health", http.HandlerFunc(b.serveHTTP))
	mux.Handle("GET /api/admin/integrations/bots", auth.Protect(true, b.list))
	mux.Handle("PUT /api/admin/integrations/bots/{provider}", auth.Protect(true, b.configure))
	b.refresh(ctx)
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.refresh(ctx)
			}
		}
	}()
}

func (b *Bots) serveHTTP(w http.ResponseWriter, r *http.Request) {
	state := b.current.Load()
	if state == nil {
		web.Handler(func(http.ResponseWriter, *http.Request) error {
			return web.Fail(http.StatusServiceUnavailable, "渠道配置暂不可用")
		}).ServeHTTP(w, r)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(state.context, cancel)
	defer stop()
	if state.context.Err() != nil {
		cancel()
	}
	state.routes.ServeHTTP(w, r.WithContext(ctx))
}

func (b *Bots) defaults() *botRuntime {
	return &botRuntime{feishu: NewFeishu(b.DB, b.Vault), slack: NewSlack(b.DB, b.Vault), ding: NewDingTalk(b.DB, b.Vault)}
}

func (s *botRuntime) configure(provider string, enabled bool, fields botFields, creds botSecrets) bool {
	if fields.Bot == "" && provider == "dingtalk" {
		fields.Bot = fields.App
	}
	if !enabled {
		creds = botSecrets{}
	}
	switch provider {
	case "feishu":
		s.feishu.App, s.feishu.Tenant, s.feishu.Bot = fields.App, fields.Tenant, fields.Bot
		s.feishu.Secret, s.feishu.Encrypt, s.feishu.Verify = creds.Secret, creds.Encrypt, creds.Verify
		return !enabled || s.feishu.enabled()
	case "slack":
		s.slack.team, s.slack.app, s.slack.signing, s.slack.bot = fields.Tenant, fields.App, creds.Secret, creds.Verify
		s.slack.key = "slack/" + fields.Tenant + "/" + fields.App
		return !enabled || s.slack.configured()
	case "dingtalk":
		s.ding.corp, s.ding.app, s.ding.robot, s.ding.secret = fields.Tenant, fields.App, fields.Bot, creds.Secret
		s.ding.key = "dingtalk/" + fields.Tenant + "/" + fields.App
		return !enabled || s.ding.configured()
	}
	return false
}

func (s *botRuntime) environmentSettings() []botSetting {
	return []botSetting{
		{Provider: "feishu", Enabled: s.feishu.enabled(), Fields: botFields{s.feishu.App, s.feishu.Tenant, s.feishu.Bot}, HasCredentials: s.feishu.Secret != ""},
		{Provider: "slack", Enabled: s.slack.configured(), Fields: botFields{s.slack.app, s.slack.team, ""}, HasCredentials: s.slack.signing != ""},
		{Provider: "dingtalk", Enabled: s.ding.configured(), Fields: botFields{s.ding.app, s.ding.corp, s.ding.robot}, HasCredentials: s.ding.secret != ""},
	}
}

func (b *Bots) list(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	items := b.defaults().environmentSettings()
	rows, err := b.DB.Query(r.Context(), `select provider,enabled,fields,revision,encrypted_credentials is not null from im_bot_settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item botSetting
		if err := rows.Scan(&item.Provider, &item.Enabled, &item.Fields, &item.Revision, &item.HasCredentials); err != nil {
			return err
		}
		items[botIndex(item.Provider)] = item
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, items)
}

func botIndex(provider string) int {
	for i, value := range botProviders {
		if value == provider {
			return i
		}
	}
	return -1
}

func decodeBotSecrets(vault resources.Vault, provider string, cipher []byte) (botSecrets, error) {
	var out botSecrets
	if len(cipher) == 0 {
		return out, nil
	}
	raw, err := vault.Open(cipher, botScope(provider))
	if err != nil {
		return out, err
	}
	err = json.Unmarshal([]byte(raw), &out)
	return out, err
}

func (b *Bots) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	provider := r.PathValue("provider")
	if botIndex(provider) < 0 {
		return web.Fail(404, "渠道不存在")
	}
	var in struct {
		Enabled     *bool       `json:"enabled"`
		Fields      botFields   `json:"fields"`
		Credentials *botSecrets `json:"credentials"`
		Expected    int         `json:"expected"`
	}
	if err := web.ReadLimited(w, r, &in, 16<<10); err != nil {
		return err
	}
	validField := func(raw string, max int) bool {
		return len(raw) <= max && strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
	}
	if in.Enabled == nil || in.Expected < 0 || !validField(in.Fields.App, 160) || !validField(in.Fields.Tenant, 160) || !validField(in.Fields.Bot, 256) {
		return web.Fail(422, "请核对机器人配置")
	}
	if in.Credentials != nil && (!validField(in.Credentials.Secret, 4096) || !validField(in.Credentials.Verify, 4096) || !validField(in.Credentials.Encrypt, 4096) ||
		(provider != "feishu" && in.Credentials.Encrypt != "") || (provider == "dingtalk" && in.Credentials.Verify != "")) {
		return web.Fail(422, "请核对机器人凭据")
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, b.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var before int
		var prior botFields
		var cipher []byte
		err := tx.QueryRow(ctx, `select fields,encrypted_credentials,revision from im_bot_settings where provider=$1 for update`, provider).
			Scan(&prior, &cipher, &before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if before != in.Expected {
			return web.Fail(409, "机器人配置已有更新，请刷新")
		}
		if prior != in.Fields {
			cipher = nil
		}
		if in.Credentials != nil {
			cipher = nil
			if *in.Credentials != (botSecrets{}) {
				raw, err := json.Marshal(in.Credentials)
				if err != nil {
					return err
				}
				cipher, err = b.Vault.Seal(string(raw), botScope(provider))
				if err != nil {
					return err
				}
			}
		}
		creds, err := decodeBotSecrets(b.Vault, provider, cipher)
		if err != nil {
			return err
		}
		if !b.defaults().configure(provider, *in.Enabled, in.Fields, creds) {
			return web.Fail(422, "请填写此渠道所需的标识和凭据，并配置 HTTPS 工作区地址")
		}
		err = tx.QueryRow(ctx, `insert into im_bot_settings(provider,enabled,fields,encrypted_credentials)
			values($1,$2,$3,$4) on conflict(provider) do update
			set enabled=excluded.enabled,fields=excluded.fields,encrypted_credentials=excluded.encrypted_credentials,revision=im_bot_settings.revision+1
			where im_bot_settings.revision=$5 returning revision`, provider, *in.Enabled, in.Fields, cipher, before).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "机器人配置已有更新，请刷新")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "integration.bot.configure", provider, map[string]any{"enabled": *in.Enabled, "revision": revision})
	})
	if err != nil {
		return err
	}
	if b.parent != nil {
		b.refresh(ctx)
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (b *Bots) load(ctx context.Context) (*botRuntime, error) {
	state := b.defaults()
	rows, err := b.DB.Query(ctx, "select provider,enabled,fields,encrypted_credentials,revision from im_bot_settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item botSetting
		var cipher []byte
		if err := rows.Scan(&item.Provider, &item.Enabled, &item.Fields, &cipher, &item.Revision); err != nil {
			return nil, err
		}
		creds, err := decodeBotSecrets(b.Vault, item.Provider, cipher)
		if err != nil || !state.configure(item.Provider, item.Enabled, item.Fields, creds) {
			state.configure(item.Provider, false, item.Fields, botSecrets{})
			slog.Warn("channel configuration unavailable", "provider", item.Provider, "revision", item.Revision)
		}
		state.versions[botIndex(item.Provider)] = item.Revision
	}
	return state, rows.Err()
}

func (b *Bots) refresh(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	read, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	next, err := b.load(read)
	old := b.current.Load()
	if err != nil {
		if old != nil && ctx.Err() == nil {
			b.current.Store(nil)
			old.cancel()
		}
		return
	}
	if old != nil && old.versions == next.versions {
		return
	}
	next.routes = http.NewServeMux()
	next.feishu.Routes(next.routes, b.identity)
	next.slack.Routes(next.routes, b.identity)
	next.ding.Routes(next.routes, b.identity)
	run, cancel := context.WithCancel(b.parent)
	next.cancel = cancel
	next.context = run
	if old != nil {
		old.cancel()
	}
	b.current.Store(next)
	go next.feishu.Notify(run)
	go next.feishu.ImportAttachments(run)
	go next.feishu.ReviewMonitor(run)
	go next.feishu.KnowledgeTaskMonitor(run)
	go next.feishu.MutationMonitor(run)
	go next.slack.Serve(run)
	go next.ding.Serve(run)
	go ServeCommentNotices(run, b.DB, next.feishu, next.ding)
	go serveEventNotices(run, b.DB, next.feishu, next.ding)
	go next.ding.mutationMonitor(run)
}

func (b *Bots) CommentConnections() (string, string) {
	if s := b.current.Load(); s != nil {
		return s.feishu.CommentConnection(), s.ding.CommentConnection()
	}
	return "", ""
}

func (b *Bots) AutomationBinding(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor string) (string, error) {
	if s := b.current.Load(); s != nil {
		return s.feishu.AutomationBinding(ctx, q, actor)
	}
	return "", nil
}

func (b *Bots) SendAutomationNotice(ctx context.Context, notice, actor, binding, project string) (string, error) {
	if s := b.current.Load(); s != nil {
		delivery, cancel := context.WithCancel(ctx)
		defer cancel()
		stop := context.AfterFunc(s.context, cancel)
		defer stop()
		if s.context.Err() != nil {
			cancel()
		}
		return s.feishu.SendAutomationNotice(delivery, notice, actor, binding, project)
	}
	return "", errors.New("notification channel unavailable")
}

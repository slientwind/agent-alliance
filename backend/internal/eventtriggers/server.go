package eventtriggers

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}

type Target struct {
	Channel string `json:"channel"`
	Member  string `json:"memberId"`
}

type Trigger struct {
	ID         string   `json:"id"`
	Space      string   `json:"spaceId"`
	Connector  string   `json:"connectorId,omitempty"`
	Name       string   `json:"name"`
	Filter     string   `json:"filter"`
	Dedup      string   `json:"dedupKey"`
	Cooldown   int      `json:"cooldownSeconds"`
	Fields     []string `json:"eventFields"`
	Studio     string   `json:"studioReleaseId,omitempty"`
	Targets    []Target `json:"notifyTargets"`
	AllowWrite bool     `json:"allowWritePreview"`
	Maintainer string   `json:"maintainerId"`
	Enabled    bool     `json:"enabled"`
	Revision   int      `json:"revision"`
}

const triggerColumns = `t.id,t.space_id,coalesce(t.connector_id::text,''),t.name,t.filter_cel,t.dedup_cel,t.cooldown_seconds,t.event_fields,
 coalesce(t.studio_release_id::text,''),t.notify_targets,t.allow_write_preview,t.maintainer,t.enabled,t.revision`

var identifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`)

func canonicalID(input string) bool {
	id, err := uuid.Parse(input)
	return err == nil && id != uuid.Nil && id.String() == input
}

func validText(input string, maximum int) bool {
	return input != "" && utf8.ValidString(input) && len(input) <= maximum && !strings.ContainsFunc(input, unicode.IsControl)
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	const root = "/api/spaces/{space}/event-triggers"
	mux.Handle("GET "+root, auth.Protect(false, s.list))
	mux.Handle("PUT "+root+"/{trigger}", auth.Protect(false, s.configure))
	mux.Handle("GET "+root+"/{trigger}/hits", auth.Protect(false, s.hits))
	mux.Handle("POST /api/webhooks/events/{token}", web.Handler(s.ingress))
}

func (s Server) list(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	space := r.PathValue("space")
	if !canonicalID(space) {
		return web.Fail(404, "找不到团队空间")
	}
	rows, err := s.DB.Query(r.Context(), `select `+triggerColumns+` from event_triggers t
	 where t.space_id=$1 and space_role(t.space_id,$2)='space_admin' order by t.name,t.id limit 100`, space, u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Trigger])
	if err != nil {
		return err
	}
	if items == nil {
		items = []Trigger{}
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, items)
}

func validateTrigger(t *Trigger) bool {
	t.Name, t.Filter, t.Dedup = strings.TrimSpace(t.Name), strings.TrimSpace(t.Filter), strings.TrimSpace(t.Dedup)
	if t.Filter == "" {
		t.Filter = "true"
	}
	if !validText(t.Name, 360) || utf8.RuneCountInString(t.Name) > 120 || t.Cooldown < 15 || t.Cooldown > 21600 ||
		(t.Connector != "" && !canonicalID(t.Connector)) || (t.Studio != "" && !canonicalID(t.Studio)) ||
		len(t.Fields) > 12 || len(t.Targets) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, field := range t.Fields {
		if !identifier.MatchString(field) || seen[field] {
			return false
		}
		seen[field] = true
	}
	for _, target := range t.Targets {
		key := target.Channel + "/" + target.Member
		if !canonicalID(target.Member) || (target.Channel != "feishu" && target.Channel != "dingtalk") || seen[key] {
			return false
		}
		seen[key] = true
	}
	if t.Fields == nil {
		t.Fields = []string{}
	}
	if t.Targets == nil {
		t.Targets = []Target{}
	}
	_, _, err := compileRules(t.Filter, t.Dedup)
	return err == nil
}

func (s Server) configure(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("trigger")
	if !canonicalID(space) || !canonicalID(id) {
		return web.Fail(404, "找不到业务事件触发器")
	}
	var in struct {
		Trigger
		Expected int    `json:"expectedVersion"`
		Secret   string `json:"secret,omitempty"`
		Rotate   bool   `json:"rotate,omitempty"`
	}
	if err := web.ReadLimited(w, r, &in, 16<<10); err != nil {
		return err
	}
	if in.Expected < 0 || !validateTrigger(&in.Trigger) || (in.Secret != "" && (!validText(in.Secret, 256) || len(in.Secret) < 32)) {
		return web.Fail(422, "请核对事件过滤、去重键、冷却时间和签名密钥")
	}
	if in.Maintainer == "" {
		in.Maintainer = u.ID
	}
	if !canonicalID(in.Maintainer) {
		return web.Fail(422, "请选择空间负责人")
	}
	var token, secret string
	var tokenHash, sealed []byte
	if in.Expected == 0 || in.Rotate {
		token = web.Token()
		tokenHash = web.Hash(token)
		secret = in.Secret
		if secret == "" {
			secret = web.Token()
		}
	} else if in.Secret != "" {
		secret = in.Secret
	}
	if secret != "" {
		var err error
		sealed, err = s.Vault.Seal(secret, signatureScope(id))
		if err != nil {
			return err
		}
	}
	ctx := r.Context()
	var version int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var allowed bool
		err := tx.QueryRow(ctx, `select s.kind='team' and space_role(s.id,$2)='space_admin'
		 and space_role(s.id,$3) in ('space_admin','member')
		 and ($4='' or exists(select from business_connectors c where (c.id,c.space_id)=(nullif($4,'')::uuid,s.id) and c.enabled))
		 and ($5='' or exists(select from studio_releases r where r.id=nullif($5,'')::uuid
		  and r.team_id=s.team_id and r.agent_scope='team' and r.state='published'))
		 from spaces s where s.id=$1 for share`, space, u.ID, in.Maintainer, in.Connector, in.Studio).Scan(&allowed)
		if err != nil || !allowed {
			return web.Fail(404, "空间、负责人或业务来源不可用")
		}
		for _, recipient := range in.Targets {
			err := tx.QueryRow(ctx, `select coalesce(space_role($1,$2) in ('space_admin','member'),false)`, space, recipient.Member).Scan(&allowed)
			if err != nil || !allowed {
				return web.Fail(422, "事件提醒只可发给当前团队空间成员")
			}
		}
		args := []any{id, space, in.Connector, in.Name, tokenHash, sealed, in.Filter, in.Dedup, in.Cooldown,
			in.Fields, in.Studio, in.Targets, in.AllowWrite, in.Maintainer, in.Enabled}
		if in.Expected == 0 {
			err = tx.QueryRow(ctx, `insert into event_triggers(id,space_id,connector_id,name,webhook_token_hash,hmac_cipher,filter_cel,dedup_cel,
			   cooldown_seconds,event_fields,studio_release_id,notify_targets,allow_write_preview,maintainer,enabled)
			 values($1,$2,nullif($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10,nullif($11,'')::uuid,$12,$13,$14,$15)
			 on conflict do nothing returning revision`, args...).Scan(&version)
		} else {
			err = tx.QueryRow(ctx, `update event_triggers set connector_id=nullif($3,'')::uuid,name=$4,webhook_token_hash=coalesce($5,webhook_token_hash),
			 hmac_cipher=coalesce($6,hmac_cipher),filter_cel=$7,dedup_cel=$8,cooldown_seconds=$9,event_fields=$10,
			 studio_release_id=nullif($11,'')::uuid,notify_targets=$12,allow_write_preview=$13,maintainer=$14,enabled=$15,revision=revision+1
			 where (id,space_id,revision)=($1,$2,$16) returning revision`, append(args, in.Expected)...).Scan(&version)
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "business.event.configure", id, map[string]any{"space": space, "revision": version})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(409, "事件触发器已更新，请刷新核对")
	}
	if err != nil {
		return err
	}
	response := map[string]any{"id": id, "revision": version}
	if token != "" {
		response["webhookPath"] = "/api/webhooks/events/" + token
	}
	if secret != "" {
		response["secret"] = secret
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, response)
}

func (s Server) hits(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("trigger")
	if !canonicalID(space) || !canonicalID(id) {
		return web.Fail(404, "找不到业务事件触发器")
	}
	var items []byte
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('id',b.id,'firstAt',b.first_at,
	 'lastAt',b.last_at,'readyAt',b.ready_at,'hits',b.hits,'status',b.status,'runId',b.run_id,'projectId',r.project_id,'resultCode',b.result_code)
	 order by b.last_at desc,b.id),'[]'::jsonb) from (
	   select b.* from event_trigger_batches b join event_triggers t on t.id=b.trigger_id
	    where (t.id,t.space_id)=($1,$2) and space_role(t.space_id,$3)='space_admin'
	    order by b.last_at desc,b.id limit 100)b left join runs r on r.id=b.run_id`, id, space, u.ID).Scan(&items)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(items)
	return err
}

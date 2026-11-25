package knowledgesync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const maxKnowledgeFile = 100 << 20

type collectionSettings struct {
	Origin    string `json:"origin,omitempty"`
	Account   string `json:"account,omitempty"`
	Container string `json:"container,omitempty"`
	Operator  string `json:"operator,omitempty"`
	Region    string `json:"region,omitempty"`
	Tenant    string `json:"tenant,omitempty"`
}

type collectionSecret struct {
	Token        string `json:"token,omitempty"`
	KeyID        string `json:"keyId,omitempty"`
	KeySecret    string `json:"keySecret,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
}

type collectionView struct {
	ID           string             `json:"id"`
	Provider     string             `json:"provider"`
	Root         string             `json:"root"`
	Name         string             `json:"name"`
	ACLMode      string             `json:"aclMode"`
	Settings     collectionSettings `json:"settings"`
	State        string             `json:"state"`
	Interval     int                `json:"intervalMinutes"`
	Revision     int                `json:"revision"`
	Enabled      bool               `json:"enabled"`
	Credential   bool               `json:"hasCredential"`
	LastSyncedAt *time.Time         `json:"lastSyncedAt,omitempty"`
}

var (
	remoteToken = regexp.MustCompile(`^[A-Za-z0-9_.!@=-]{1,256}$`)
	s3Bucket    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3Region    = regexp.MustCompile(`^[a-z]{2}(-[a-z0-9]+)+-[0-9]+$`)
)

func validRemoteID(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validRemoteToken(value string) bool {
	return value != "." && value != ".." && remoteToken.MatchString(value)
}

func validCollectionOrigin(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Path == "" &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

func (c collectionView) valid() bool {
	if !validRemoteID(c.Name, 480) || utf8.RuneCountInString(c.Name) > 120 || c.Interval < 5 || c.Interval > 1440 ||
		(c.ACLMode != "space" && c.ACLMode != "source") {
		return false
	}
	s := c.Settings
	switch c.Provider {
	case "manual":
		return c.Root == "" && s == (collectionSettings{}) && c.ACLMode == "space"
	case "feishu_wiki":
		return numericID.MatchString(c.Root) && documentID.MatchString(s.Account) && s == (collectionSettings{Account: s.Account})
	case "feishu_drive":
		return documentID.MatchString(c.Root) && documentID.MatchString(s.Account) && s == (collectionSettings{Account: s.Account})
	case "confluence":
		email, err := mail.ParseAddress(s.Account)
		return numericID.MatchString(c.Root) && validCollectionOrigin(s.Origin) && len(s.Origin) <= 300 &&
			err == nil && email.Address == s.Account && len(s.Account) <= 200 && s == (collectionSettings{Origin: s.Origin, Account: s.Account})
	case "dingtalk", "wecom":
		return validRemoteToken(c.Root) && validRemoteToken(s.Container) && validRemoteToken(s.Operator) &&
			documentID.MatchString(s.Account) && s == (collectionSettings{Operator: s.Operator, Container: s.Container, Account: s.Account})
	case "sharepoint":
		validLogin := (s.Tenant == "" && s.Account == "") || (validID(s.Tenant) && validID(s.Account))
		return validRemoteToken(c.Root) && validRemoteToken(s.Container) && validLogin &&
			s == (collectionSettings{Container: s.Container, Tenant: s.Tenant, Account: s.Account})
	case "s3":
		return (c.Root == "" || (validRemoteID(c.Root, 1024) && strings.HasSuffix(c.Root, "/"))) && s3Bucket.MatchString(s.Container) &&
			!strings.Contains(s.Container, "..") && s3Region.MatchString(s.Region) && (s.Origin == "" || validCollectionOrigin(s.Origin)) &&
			len(s.Origin) <= 300 && s == (collectionSettings{Origin: s.Origin, Container: s.Container, Region: s.Region})
	}
	return false
}

func (s collectionSecret) valid(provider string) bool {
	valid := func(value string) bool {
		return validRemoteID(value, 4096) && !strings.ContainsFunc(value, unicode.IsSpace)
	}
	switch provider {
	case "manual":
		return s == (collectionSecret{})
	case "s3":
		return s.Token == "" && valid(s.KeyID) && valid(s.KeySecret) && (s.SessionToken == "" || valid(s.SessionToken))
	default:
		return valid(s.Token) && s == (collectionSecret{Token: s.Token})
	}
}

func (s Server) collectionRoutes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/spaces/{space}/knowledge/collections", auth.Protect(false, s.listCollections))
	mux.Handle("PUT /api/spaces/{space}/knowledge/collections/{collection}", auth.Protect(false, s.saveCollection))
	mux.Handle("POST /api/spaces/{space}/knowledge/collections/{collection}/sync", auth.Protect(false, s.queueCollection))
}

func (s Server) listCollections(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到知识空间")
	}
	rows, err := s.DB.Query(r.Context(), `select id,sync_provider,remote_root_id,name,acl_mode,settings,sync_status,sync_interval_minutes,
	 revision,enabled,credential_cipher is not null,last_synced_at from kb_collections
	 where space_id=$1 and space_role($1,$2) in ('space_admin','member')
	 and ((enabled and acl_mode='space') or space_role($1,$2)='space_admin') order by name,id limit 250`, space, actor.ID)
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[collectionView])
	if err != nil {
		return err
	}
	if out == nil {
		out = []collectionView{}
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, out)
}

func collectionScope(id string) string { return "knowledge-collection:" + id }

func lockCollectionSpace(ctx context.Context, tx pgx.Tx, space, actor string) error {
	var organizationRole string
	if err := tx.QueryRow(ctx, `select role from members where id=$1 and enabled and departed_at is null
	 and kind='person' for share`, actor).Scan(&organizationRole); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(404, "找不到可管理的知识空间")
		}
		return err
	}
	var kind string
	var owner *string
	err := tx.QueryRow(ctx, "select kind,owner_member_id from spaces where id=$1 and archived_at is null for share", space).Scan(&kind, &owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	switch kind {
	case "personal":
		if owner != nil && *owner == actor {
			return nil
		}
	case "commons":
		if organizationRole == "admin" {
			return nil
		}
	case "team":
		var admin int
		err := tx.QueryRow(ctx, "select 1 from space_members where (space_id,member_id)=($1,$2) and role='space_admin' for share", space, actor).Scan(&admin)
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	return web.Fail(404, "找不到可管理的知识空间")
}

func (s Server) saveCollection(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("collection")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到知识目录")
	}
	var in struct {
		collectionView
		Secret   *collectionSecret `json:"secret"`
		Expected int               `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &in, 20<<10); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Interval == 0 {
		in.Interval = 15
	}
	if in.ACLMode == "" {
		in.ACLMode = "source"
		if in.Provider == "manual" {
			in.ACLMode = "space"
		}
	}
	if !in.valid() || in.Expected < 0 || (in.Secret != nil && !in.Secret.valid(in.Provider)) {
		return web.Fail(422, "目录、同步间隔、权限模式或连接凭据无效")
	}
	var cipher []byte
	if in.Secret != nil && in.Provider != "manual" {
		raw, err := json.Marshal(in.Secret)
		if err != nil {
			return err
		}
		cipher, err = s.Vault.Seal(string(raw), collectionScope(id))
		if err != nil {
			return err
		}
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockCollectionSpace(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		if in.Expected == 0 {
			if cipher == nil && in.Provider != "manual" {
				return web.Fail(422, "请配置目录连接凭据")
			}
			err := tx.QueryRow(ctx, `insert into kb_collections(id,space_id,name,sync_provider,remote_root_id,settings,credential_cipher,
			 sync_interval_minutes,acl_mode,enabled,sync_status,maintainer)
			 values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,case when not $10 then 'paused' when $4='manual' then 'ready' else 'queued' end,$11)
			 on conflict do nothing returning revision`, id, space, in.Name, in.Provider, in.Root, in.Settings, cipher,
				in.Interval, in.ACLMode, in.Enabled, actor.ID).Scan(&revision)
			if errors.Is(err, pgx.ErrNoRows) {
				return web.Fail(409, "知识目录已存在")
			}
			if err != nil {
				return err
			}
		} else {
			// Bind remote identity to the collection for the entire document history.
			err := tx.QueryRow(ctx, `update kb_collections set name=$3,sync_interval_minutes=$4,acl_mode=$5,enabled=$6,
			 sync_status=case when not $6 then 'paused' when sync_provider='manual' then 'ready' else 'queued' end,
			 credential_cipher=coalesce($7,credential_cipher),revision=revision+1,lease=null,scan_id=null,next_sync_at=clock_timestamp()
			 where (id,space_id,revision,sync_provider,remote_root_id,settings)=($1,$2,$8,$9,$10,$11::jsonb) returning revision`,
				id, space, in.Name, in.Interval, in.ACLMode, in.Enabled, cipher, in.Expected, in.Provider, in.Root, in.Settings).Scan(&revision)
			if errors.Is(err, pgx.ErrNoRows) {
				return web.Fail(409, "目录已变更，请刷新。更换来源需创建新目录")
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "delete from kb_sync_pages where collection_id=$1", id); err != nil {
				return err
			}
			if cipher != nil {
				if _, err := tx.Exec(ctx, "update kb_documents set acl_verified_at=null,source_state='withdrawn' where collection_id=$1", id); err != nil {
					return err
				}
			}
		}
		return database.Audit(ctx, tx, actor.ID, "knowledge.collection.configure", id, map[string]any{"space": space, "provider": in.Provider, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"id": id, "revision": revision})
}

func (s Server) queueCollection(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("collection")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到知识目录")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockCollectionSpace(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update kb_collections set next_sync_at=clock_timestamp(),sync_status='queued'
		 where (id,space_id)=($1,$2) and enabled and sync_provider<>'manual' and (lease is null or next_sync_at<=clock_timestamp())`, id, space)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "目录不可同步或正在同步")
		}
		return database.Audit(ctx, tx, actor.ID, "knowledge.collection.queue", id, map[string]string{"space": space})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusAccepted)
	return nil
}

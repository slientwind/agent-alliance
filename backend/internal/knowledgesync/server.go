package knowledgesync

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB        *pgxpool.Pool
	Vault     resources.Vault
	Objects   knowledge.Objects
	Egress    egress.Policy
	Inference knowledge.KBInference
	fetch     func(context.Context, providerRequest) (string, error)
	directory func(context.Context, collectionView, collectionSecret) (remoteDirectory, error)
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/admin/integrations/knowledge", auth.Protect(true, s.list))
	mux.Handle("GET /api/admin/integrations/knowledge/targets", auth.Protect(true, s.targets))
	mux.Handle("PUT /api/admin/integrations/knowledge/{id}", auth.Protect(true, s.configure))
	mux.Handle("POST /api/admin/integrations/knowledge/{id}/sync", auth.Protect(true, s.queue))
	s.collectionRoutes(mux, auth)
	s.lifecycleRoutes(mux, auth)
	mux.Handle("GET /api/spaces/{space}/knowledge/search", auth.Protect(false, s.searchDocuments))
}

type sourceView struct {
	ID            string     `json:"id"`
	Provider      string     `json:"provider"`
	Name          string     `json:"name"`
	Settings      settings   `json:"settings"`
	Project       string     `json:"project"`
	ProjectName   string     `json:"projectName"`
	HasCredential bool       `json:"hasCredential"`
	Enabled       bool       `json:"enabled"`
	Revision      int        `json:"revision"`
	State         string     `json:"state"`
	Verdict       string     `json:"verdict"`
	Fetched       *time.Time `json:"fetchedAt,omitempty"`
	Version       *int       `json:"version,omitempty"`
}

func (s Server) list(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select k.id,k.provider,k.name,k.settings,k.project_id,p.title,
		k.encrypted_credentials is not null,k.enabled,k.revision,k.status,k.verdict,k.fetched_at,k.published_version
		from enterprise_knowledge_sources k join projects p on p.id=k.project_id where not personal_project(p.id) order by k.name,k.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := make([]sourceView, 0)
	for rows.Next() {
		var item sourceView
		if err := rows.Scan(&item.ID, &item.Provider, &item.Name, &item.Settings, &item.Project, &item.ProjectName, &item.HasCredential,
			&item.Enabled, &item.Revision, &item.State, &item.Verdict, &item.Fetched, &item.Version); err != nil {
			return err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) targets(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var out []byte
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('id',id,'title',title) order by title,id),'[]')
		from projects p where p.owner=$1 and not personal_project(p.id) and exists(select from project_members pm where (pm.project_id,pm.member_id)=(p.id,p.owner))`, actor.ID).Scan(&out)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(out)
	return err
}

type connectionInput struct {
	Name     string   `json:"name"`
	Provider string   `json:"provider"`
	Settings settings `json:"settings"`
	Project  string   `json:"project"`
	Secret   *string  `json:"secret"`
	Enabled  *bool    `json:"enabled"`
	Expected int      `json:"expected"`
}

func validID(raw string) bool {
	id, err := uuid.Parse(raw)
	return err == nil && id != uuid.Nil && id.String() == raw
}

func sourceScope(id string) string { return "integration/knowledge/" + id }

func (s Server) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := r.PathValue("id")
	if !validID(id) {
		return web.Fail(422, "知识源编号无效")
	}
	var in connectionInput
	if err := web.ReadLimited(w, r, &in, 16<<10); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Settings.Origin = strings.TrimRight(strings.TrimSpace(in.Settings.Origin), "/")
	if in.Name == "" || len(in.Name) > 200 || strings.ContainsFunc(in.Name, unicode.IsControl) || !validID(in.Project) ||
		!in.Settings.valid(in.Provider) || in.Enabled == nil || in.Expected < 0 || (in.Secret != nil && *in.Secret != "" && !credentialValid(in.Provider, *in.Secret)) {
		return web.Fail(422, "请核对知识源、目标项目和凭据")
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		if err := lockTarget(ctx, tx, in.Project, actor.ID, *in.Enabled || in.Expected == 0); err != nil {
			return err
		}
		var before int
		var identity connectionInput
		var source *string
		var cipher []byte
		err := tx.QueryRow(ctx, `select provider,settings,project_id,source_id,revision,encrypted_credentials
			from enterprise_knowledge_sources where id=$1 for update`, id).Scan(&identity.Provider, &identity.Settings, &identity.Project, &source, &before, &cipher)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if before != in.Expected {
			return web.Fail(409, "知识源配置已更新，请刷新")
		}
		if before > 0 && (identity.Provider != in.Provider || identity.Settings != in.Settings || identity.Project != in.Project) {
			return web.Fail(409, "更换文档或目标项目时，请新增知识源")
		}
		if in.Secret != nil {
			cipher = nil
			if *in.Secret != "" {
				cipher, err = s.Vault.Seal(*in.Secret, sourceScope(id))
				if err != nil {
					return err
				}
			}
		}
		state := "disabled"
		if *in.Enabled {
			if len(cipher) == 0 {
				return web.Fail(422, "启用知识源需要可用凭据")
			}
			state = "pending"
		} else if source != nil {
			if err := s.Objects.RevokeSyncedText(ctx, tx, in.Project, *source, actor.ID); err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `insert into enterprise_knowledge_sources(id,provider,name,settings,project_id,configured_by,encrypted_credentials,enabled,status)
			values($1,$2,$3,$4,$5,$6,$7,$8,$9) on conflict(id) do update
			set name=excluded.name, configured_by=excluded.configured_by, encrypted_credentials=excluded.encrypted_credentials,
			    enabled=excluded.enabled,status=excluded.status,lease=null,next_at=now(),verdict='',
			    source_id=case when excluded.enabled then enterprise_knowledge_sources.source_id end,
			    published_version=case when excluded.enabled then enterprise_knowledge_sources.published_version end,
			    content_hash=case when excluded.enabled then enterprise_knowledge_sources.content_hash end,
			    fetched_at=case when excluded.enabled then enterprise_knowledge_sources.fetched_at end,
			    revision=enterprise_knowledge_sources.revision+1
			where enterprise_knowledge_sources.revision=$10 returning revision`, id, in.Provider, in.Name, in.Settings, in.Project, actor.ID,
			cipher, *in.Enabled, state, before).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "知识源配置已更新，请刷新")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "integration.knowledge.configure", id, map[string]any{"enabled": *in.Enabled, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func lockTarget(ctx context.Context, tx pgx.Tx, project, actor string, requireOwner bool) error {
	var owner string
	var member bool
	err := tx.QueryRow(ctx, `select p.owner,exists(select from project_members pm join members m on m.id=pm.member_id
		where (pm.project_id,pm.member_id)=(p.id,$2) and m.enabled and m.kind='person')
		from projects p where p.id=$1 and not personal_project(p.id) for update`, project, actor).Scan(&owner, &member)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && requireOwner && (owner != actor || !member)) {
		return web.Fail(404, "请选择由你负责的项目")
	}
	return err
}

func (s Server) queue(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := r.PathValue("id")
	if !validID(id) {
		return web.Fail(422, "知识源编号无效")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update enterprise_knowledge_sources set status='pending',next_at=now(),verdict=''
			where id=$1 and enabled and lease is null`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "知识源正在同步或尚未启用")
		}
		return database.Audit(ctx, tx, actor.ID, "integration.knowledge.queue", id, map[string]any{})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]string{"state": "pending"})
}

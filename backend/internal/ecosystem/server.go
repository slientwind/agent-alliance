package ecosystem

import (
	"encoding/json"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/egress"
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

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/admin/ecosystem/sources", auth.Protect(true, s.sources))
	mux.Handle("POST /api/admin/ecosystem/sources", auth.Protect(true, s.saveSource))
	mux.Handle("PUT /api/admin/ecosystem/sources/{id}", auth.Protect(true, s.saveSource))
	mux.Handle("POST /api/admin/ecosystem/sources/{id}/scan", auth.Protect(true, s.scanNow))
	mux.Handle("GET /api/admin/ecosystem/catalog", auth.Protect(true, s.catalogQueue))
	mux.Handle("GET /api/admin/ecosystem/updates", auth.Protect(true, s.updateNotices))
	mux.Handle("PUT /api/admin/ecosystem/updates/{id}/acknowledge", auth.Protect(true, s.acknowledgeUpdate))
	mux.Handle("POST /api/admin/ecosystem/catalog/{id}/approve", auth.Protect(true, s.approve))
	mux.Handle("POST /api/admin/ecosystem/approved/{id}/revoke", auth.Protect(true, s.revokeApproval))
	mux.Handle("GET /api/ecosystem/catalog", auth.Protect(false, s.marketCatalog))
	mux.Handle("PUT /api/ecosystem/catalog/{id}/request", auth.Protect(false, s.requestAdmission))
	mux.Handle("PUT /api/ecosystem/bindings/{id}", auth.Protect(false, s.setBinding))
	mux.Handle("POST /api/admin/ecosystem/catalog/{id}/return-requests", auth.Protect(true, s.returnRequests))
}

func (s Server) sources(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var out json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_strip_nulls(jsonb_build_object(
	 'id',s.id,'name',s.name,'kind',s.kind,'endpoint',s.endpoint,'config',s.source_config,'cron',s.cron,'timeZone',s.time_zone,
	 'enabled',s.enabled,'revision',s.revision,'hasCredential',s.credential is not null,'schedule',s.schedule_sync,
	 'lastScan',(select jsonb_build_object('status',c.status,'found',c.found,'added',c.added,'detail',c.detail,
	  'startedAt',c.started_at,'finishedAt',c.finished_at) from ecosystem_scans c where c.source_id=s.id
	  order by c.started_at desc,c.id desc limit 1)))
	 from ecosystem_sources s order by s.name,s.id limit 250))`).Scan(&out)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) saveSource(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in sourceInput
	if err := web.ReadLimited(w, r, &in, 16<<10); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	ctx := r.Context()
	if *in.Enabled && !(egress.Policy{DB: s.DB}).Allowed(ctx, in.Endpoint, false) {
		return web.Fail(422, "请先为扫描源的主机添加企业访问许可")
	}
	id := r.PathValue("id")
	status, revision := http.StatusOK, 0
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var priorKind, priorAddress string
		var credential []byte
		if id == "" {
			if in.Expected != 0 || in.KeepCredential {
				return web.Fail(422, "请按新扫描源填写设置")
			}
			id = uuid.NewString()
		} else {
			if _, err := uuid.Parse(id); err != nil {
				return web.Fail(404, "找不到扫描源")
			}
			if err := tx.QueryRow(ctx, `select revision,kind,endpoint,credential from ecosystem_sources where id=$1 for update`, id).
				Scan(&revision, &priorKind, &priorAddress, &credential); err != nil {
				return err
			}
			if revision != in.Expected {
				return web.Fail(409, "扫描源设置已更新，请刷新后继续")
			}
			if in.KeepCredential && (priorKind != in.Kind || priorAddress != in.Endpoint) {
				return web.Fail(409, "仓库地址或类型已变化，请重新填写凭据")
			}
		}
		if !in.KeepCredential {
			credential = nil
			if in.Credential != "" {
				var err error
				credential, err = s.Vault.Seal(in.Credential, credentialScope(id, in.Endpoint))
				if err != nil {
					return err
				}
			}
		}
		if in.Kind == PrivateGit && *in.Enabled && len(credential) == 0 {
			return web.Fail(422, "请提供用于读取私有仓库的凭据")
		}
		if revision == 0 {
			status = http.StatusCreated
			if err := tx.QueryRow(ctx, `insert into ecosystem_sources(id,name,kind,endpoint,source_config,cron,time_zone,enabled,credential,created_by)
			 values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) returning revision`,
				id, in.Name, in.Kind, in.Endpoint, in.Config, in.Cron, in.TimeZone, in.Enabled, credential, actor.ID).
				Scan(&revision); err != nil {
				return err
			}
		} else if err := tx.QueryRow(ctx, `update ecosystem_sources set name=$2,kind=$3,endpoint=$4,source_config=$5,cron=$6,
		 time_zone=$7,enabled=$8,credential=$9,revision=revision+1,schedule_sync='pending',schedule_next_at=now(),
		 catalog_cursor='',updated_at=now() where id=$1 returning revision`,
			id, in.Name, in.Kind, in.Endpoint, in.Config, in.Cron, in.TimeZone, in.Enabled, credential).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "ecosystem.source.save", id, map[string]any{
			"kind": in.Kind, "revision": revision, "enabled": *in.Enabled,
		})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, ScanRef{ID: id, Revision: revision})
}

func (s Server) scanNow(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct{ Expected int }
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	sourceID, key := r.PathValue("id"), r.Header.Get("Idempotency-Key")
	if _, err := uuid.Parse(sourceID); err != nil || in.Expected < 1 || len(key) < 1 || len(key) > 128 {
		return web.Fail(422, "请核对扫描源设置后重试")
	}
	ctx := r.Context()
	var id string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var matches bool
		err := tx.QueryRow(ctx, `select id,(source_id,source_revision)=($3,$4) from ecosystem_scan_requests where (actor,request_key)=($1,$2)`,
			actor.ID, key, sourceID, in.Expected).Scan(&id, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "这个提交编号已用于其他扫描")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		err = tx.QueryRow(ctx, `insert into ecosystem_scan_requests(source_id,source_revision,actor,request_key)
		 select s.id,s.revision,$3,$4 from ecosystem_sources s where (s.id,s.revision)=($1,$2) and s.enabled for share
		 on conflict(actor,request_key) do nothing returning id`,
			sourceID, in.Expected, actor.ID, key).Scan(&id)
		if err == pgx.ErrNoRows {
			err = tx.QueryRow(ctx, `select id,(source_id,source_revision)=($3,$4)
			 from ecosystem_scan_requests where (actor,request_key)=($1,$2)`, actor.ID, key, sourceID, in.Expected).Scan(&id, &matches)
			if err == pgx.ErrNoRows {
				return web.Fail(409, "扫描源已停用或设置已变化")
			}
			if err == nil && !matches {
				return web.Fail(409, "这个提交编号已用于其他扫描")
			}
			return err
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "ecosystem.scan.request", sourceID, map[string]string{"request": id})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]string{"id": id})
}

package ecosystem

import (
	"encoding/json"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s Server) updateNotices(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_build_object(
	 'id',n.id,'name',c.name,'source',src.name,'catalog',c.id,'priorVersion',old.upstream_version,
	 'version',c.upstream_version,'summary',n.summary,'discoveredAt',n.discovered_at)
	 from ecosystem_update_notices n join ecosystem_catalog c on c.id=n.new_catalog
	 join ecosystem_sources src on src.id=c.source_id join enterprise_approved_packages p on p.id=n.prior_package
	 join ecosystem_catalog old on old.id=p.catalog_id
	 where n.acknowledged_at is null and p.revoked_at is null and not exists(
	  select from enterprise_approved_packages next where next.catalog_id=n.new_catalog)
	 order by n.discovered_at desc,n.id limit 100))`).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func (s Server) acknowledgeUpdate(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		return web.Fail(404, "找不到版本提醒")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update ecosystem_update_notices set acknowledged_by=$2,acknowledged_at=clock_timestamp()
		 where id=$1 and acknowledged_at is null`, id, actor.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "ecosystem.update.acknowledge", id, map[string]string{})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

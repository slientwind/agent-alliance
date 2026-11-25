package studio

import (
	"net/http"
	"os"
	"slices"

	"alliance/internal/capability"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) capabilities(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	d, err := getDraft(ctx, s.DB, id, actor.ID, false)
	if err != nil {
		return err
	}
	var search, external bool
	err = s.DB.QueryRow(ctx, `select exists(select from web_search_integration where enabled),
	 exists(select from external_agent_admissions x join agents a on a.id=x.agent_id
	  where external_admission_can_use(x.agent_id,x.revision,$1) and
	   (x.revision=a.published_revision or x.revision=(select max(r.revision) from agent_revisions r where r.agent_id=a.id)))`,
		actor.ID).Scan(&search, &external)
	if err != nil {
		return err
	}
	vision, err := llmgateway.EnabledTier(ctx, s.DB, llmgateway.Vision)
	if err != nil {
		return err
	}
	image, err := llmgateway.EnabledTier(ctx, s.DB, llmgateway.Image)
	if err != nil {
		return err
	}
	available := map[string]bool{
		capability.Knowledge: true,
		capability.Office:    os.Getenv("CONNECTOR_URL") != "" && os.Getenv("CONNECTOR_TOKEN") != "",
		capability.Images:    vision || image,
		capability.Search:    search,
		capability.External:  external,
	}
	type option struct {
		capability.Item
		Enabled   bool `json:"enabled"`
		Available bool `json:"available"`
	}
	opts := capability.Items()
	out := make([]option, 0, len(opts))
	for _, item := range opts {
		out = append(out, option{item, slices.Contains(d.Spec.Capabilities, item.ID), available[item.ID]})
	}
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) setCapabilities(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, err := studioID(r)
	if err != nil {
		return err
	}
	var in struct {
		Expected     int
		Capabilities []string
	}
	if err := web.ReadLimited(w, r, &in, 2048); err != nil {
		return err
	}
	if in.Expected < 1 || in.Capabilities == nil || !capability.Valid(in.Capabilities) {
		return web.Fail(422, "请选择助手需要的能力")
	}
	ctx := r.Context()
	var d draft
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		d, err = getDraft(ctx, tx, id, actor.ID, true)
		if err != nil {
			return err
		}
		if d.Revision != in.Expected {
			return web.Fail(409, "草稿已更新，请刷新后再修改能力")
		}
		if slices.Equal(d.Spec.Capabilities, in.Capabilities) {
			return nil
		}
		d.Revision++
		d.Spec.Capabilities = in.Capabilities
		if _, err := tx.Exec(ctx, "update studio_drafts set revision=$2,spec=$3,updated_at=clock_timestamp() where id=$1",
			id, d.Revision, d.Spec); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "studio.capabilities", id, map[string]any{
			"revision": d.Revision, "capabilities": d.Spec.Capabilities,
		})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, d)
}

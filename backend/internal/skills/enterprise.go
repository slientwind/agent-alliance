package skills

import (
	"context"
	"encoding/json"
	"net/http"

	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type enterpriseQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type EnterpriseBinding struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Hash    string `json:"-"`
}

const skillCatalog = ` join ecosystem_catalog c on c.id=p.catalog_id and c.kind='skill'`
const boundSkill = ` from user_skill_bindings b join enterprise_approved_packages p on b.approved_package_id=p.id` + skillCatalog + `
 where b.member_id=$1 and b.enabled and p.revoked_at is null and p.review_digest is not null`
const bindingColumns = `select p.id,coalesce(nullif(p.manifest->>'name',''),c.name),c.upstream_version,p.content_hash`

func EnterpriseBindings(ctx context.Context, q enterpriseQuery, actor string) ([]EnterpriseBinding, error) {
	return readEnterpriseBindings(ctx, q, bindingColumns+boundSkill+" order by 2,p.id", actor)
}

func PinEnterpriseBindings(ctx context.Context, tx pgx.Tx, run, actor string) error {
	_, err := tx.Exec(ctx, `insert into general_agent_ecosystem_skills(run_id,package_id,binding_revision)
	 select $2,p.id,b.revision`+boundSkill, actor, run)
	return err
}

func PinnedEnterpriseBindings(ctx context.Context, q enterpriseQuery, run string) ([]EnterpriseBinding, error) {
	return readEnterpriseBindings(ctx, q, bindingColumns+` from general_agent_ecosystem_skills x
	 join enterprise_approved_packages p on x.package_id=p.id`+skillCatalog+
		` where x.run_id=$1 and general_ecosystem_skills_usable(x.run_id) order by 2,p.id`, run)
}

func readEnterpriseBindings(ctx context.Context, q enterpriseQuery, query, id string) ([]EnterpriseBinding, error) {
	rows, err := q.Query(ctx, query, id)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[EnterpriseBinding])
	if items == nil {
		items = []EnterpriseBinding{}
	}
	return items, err
}

func (c Catalog) enterprise(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	var entries json.RawMessage
	err := c.DB.QueryRow(r.Context(), `select to_jsonb(array(select jsonb_strip_nulls(jsonb_build_object(
	 'id',p.id,'catalog',c.id,'scope','enterprise','name',coalesce(nullif(p.manifest->>'name',''),c.name),
	 'description',p.manifest->>'description','version',c.upstream_version,'sha256',p.content_hash,
	 'source',jsonb_build_object('kind',s.kind,'name',s.name,'homepage',case when s.kind<>'private_git' then c.homepage end),
	 'inspection',jsonb_build_object('status',report.value->>'status','license',report.value->>'license','licenseTag',report.value->>'licenseTag',
	  'findings',array(select distinct f->>'code' from jsonb_array_elements(coalesce(report.value->'findings','[]'::jsonb)) f order by 1)),
	 'loaded',coalesce(b.enabled,false),'bindingRevision',coalesce(b.revision,0)))
	 from enterprise_approved_packages p`+skillCatalog+` join ecosystem_sources s on s.id=c.source_id
	 left join ecosystem_candidate_reviews r on r.catalog_id=c.id and ecosystem_review_digest(c.id)=p.review_digest
	 cross join lateral (select coalesce(p.manifest->'review',r.report) value) report
	 left join user_skill_bindings b on (b.member_id,b.approved_package_id)=($1,p.id)
	 where p.revoked_at is null and p.review_digest is not null and (not $2 or b.enabled)
	 order by b.enabled desc nulls last,p.approved_at desc,p.id limit 200))`, member.ID, r.URL.Query().Get("loaded") == "1").Scan(&entries)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, entries)
}

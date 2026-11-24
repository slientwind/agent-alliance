package services

import (
	"context"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type ServiceDeliveryItem struct {
	Item  int    `json:"item"`
	Step  int    `json:"step"`
	Label string `json:"label"`
}

type ServiceDeliveryContract struct {
	AllowsPartial bool                  `json:"allowsPartial"`
	Items         []ServiceDeliveryItem `json:"items"`
}

type publicationPlan struct {
	team        string
	PlanVersion int                     `json:"planVersion"`
	Delivery    ServiceDeliveryContract `json:"deliveryContract"`
}

// The same read is used for preview and publication. The transaction holds the
// project lock while copying the confirmed nodes and public requirements.
func readPublicationPlan(ctx context.Context, db resources.Query, project, actor string) (publicationPlan, error) {
	var plan publicationPlan
	err := db.QueryRow(ctx, `select p.team_id,p.plan_version,
 jsonb_build_object('allowsPartial',coalesce(c.allow_partial,false),'items',array(
   select jsonb_build_object('item',q.item,'step',q.ordinal,'label',q.label)
    from plan_delivery_requirements q where (q.project_id,q.plan_version)=(p.id,p.plan_version) order by q.item))
 from projects p join plans v on(v.project_id,v.version)=(p.id,p.plan_version)
 join team_members tm on (tm.team_id,tm.member_id)=(p.team_id,$2)
 left join plan_delivery_contracts c on (c.project_id,c.plan_version)=(p.id,p.plan_version)
 where p.id=$1 and p.owner=$2 and v.confirmed_by is not null for share of p,v,tm`, project, actor).
		Scan(&plan.team, &plan.PlanVersion, &plan.Delivery)
	return plan, err
}

func (s Server) publicationSource(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	var plan publicationPlan
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var err error
		plan, err = readPublicationPlan(ctx, tx, r.PathValue("project"), u.ID)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, plan)
}

func savePublishedDelivery(ctx context.Context, tx pgx.Tx, service string, version int, project string, plan publicationPlan) error {
	_, err := tx.Exec(ctx, `insert into service_delivery_requirements
 select $1,$2,item,ordinal,label from plan_delivery_requirements where (project_id,plan_version)=($3,$4)`,
		service, version, project, plan.PlanVersion)
	return err
}

func pinServiceDelivery(ctx context.Context, tx pgx.Tx, project, service string, version int, allowPartial bool) error {
	_, err := tx.Exec(ctx, `with requirements as (
 select item,ordinal,label from service_delivery_requirements where (service_id,version)=($2,$3)
 ), contract as (
 insert into plan_delivery_contracts(project_id,plan_version,allow_partial)
 select $1::uuid,1,$4 where exists(select from requirements) returning project_id,plan_version
 )
 insert into plan_delivery_requirements(project_id,plan_version,item,ordinal,label)
 select c.project_id,c.plan_version,q.item,q.ordinal,q.label from contract c cross join requirements q`,
		project, service, version, allowPartial)
	return err
}

// A signed run can see its own checklist and review state. The project may
// later have another run, so never expose its candidate or assessor details.
const serviceRunDeliverySQL = `(with snapshot as materialized (
 select project_delivery_coverage(r.project_id,r.plan_version,$2) as coverage
 ) select jsonb_build_object('allowPartial',coverage->'allowPartial','items',array(
 select jsonb_build_object('item',item->'item','step',item->'step','label',item->'label',
  'state',case when item->>'run'=r.id::text then item->>'state' else 'unchecked' end)
 from jsonb_array_elements(coverage->'items') item order by (item->>'item')::integer))
 from snapshot where coverage is not null)`

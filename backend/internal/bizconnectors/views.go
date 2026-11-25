package bizconnectors

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type ViewField struct {
	Source string `json:"source,omitempty"`
	Label  string `json:"label"`
	Type   string `json:"type"`
}

type Metric struct {
	Label   string `json:"label"`
	Formula string `json:"formula"`
}

type Relationship struct {
	Dimension        string `json:"dimension"`
	RelatedView      string `json:"relatedView"`
	RelatedDimension string `json:"relatedDimension"`
}

type SemanticView struct {
	ID            string               `json:"id"`
	Space         string               `json:"spaceId"`
	Connector     string               `json:"connectorId,omitempty"`
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Relation      string               `json:"relation,omitempty"`
	Operation     string               `json:"operation,omitempty"`
	Dimensions    map[string]ViewField `json:"dimensions"`
	Metrics       map[string]Metric    `json:"metrics"`
	Relationships []Relationship       `json:"relationships"`
	Time          string               `json:"timeDimension,omitempty"`
	Defaults      map[string]any       `json:"defaultFilters,omitempty"`
	RLS           map[string]string    `json:"rls,omitempty"`
	Sensitivity   string               `json:"sensitivity"`
	Enabled       bool                 `json:"enabled"`
	Revision      int                  `json:"revision"`
}

const viewColumns = `v.id,v.space_id,v.connector_id,v.view_name,v.business_desc,v.relation_name,v.operation_name,
 v.allowed_dimensions,v.verified_metrics_json,v.relationships,v.time_field,v.defaults,v.rls_column_mapping,v.sensitivity,v.enabled,v.revision`

func viewMetadata(v SemanticView) SemanticView {
	v.Relation, v.Operation, v.Defaults, v.RLS = "", "", nil, nil
	for key, field := range v.Dimensions {
		field.Source = ""
		v.Dimensions[key] = field
	}
	return v
}

func (s Server) Views(ctx context.Context, space, actor string, admin bool) ([]SemanticView, error) {
	if !validID(space) || !validID(actor) {
		return nil, ErrBadQuery
	}
	rows, err := s.DB.Query(ctx, `select `+viewColumns+` from semantic_data_views v
	 join business_connectors c on c.id=v.connector_id join spaces source on source.id=v.space_id
	 where (($3 and v.space_id=$1 and space_role($1,$2)='space_admin')
	  or (not $3 and business_view_readable(v.id,$1,$2,v.revision,c.revision)))
	 order by (source.id=$1) desc,v.view_name,v.id limit 60`, space, actor, admin)
	if err != nil {
		return nil, err
	}
	views, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SemanticView])
	if err != nil {
		return nil, err
	}
	if !admin {
		for i := range views {
			views[i] = viewMetadata(views[i])
		}
	}
	if views == nil {
		views = []SemanticView{}
	}
	return views, nil
}

func (s Server) listViews(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到业务空间")
	}
	views, err := s.Views(r.Context(), space, actor.ID, r.URL.Query().Get("manage") == "true")
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, views)
}

func validateView(v *SemanticView, c Connector) bool {
	v.Name, v.Description = strings.TrimSpace(v.Name), strings.TrimSpace(v.Description)
	if !validText(v.Name, 360) || len([]rune(v.Name)) > 120 || !validText(v.Description, 1600) ||
		len(v.Dimensions) < 1 || len(v.Dimensions) > 28 || len(v.Metrics) > 14 || len(v.Relationships) > 14 ||
		!slices.Contains([]string{"public", "internal", "confidential", "secret"}, v.Sensitivity) {
		return false
	}
	if c.Driver == "readonly_sql" {
		if !slices.Contains(c.Settings.AllowedRelations, v.Relation) || v.Operation != "" {
			return false
		}
	} else {
		op, err := readOperation(c, v.Operation)
		if err != nil || v.Relation != "" {
			return false
		}
		for _, field := range op.Parameters {
			if field.Required {
				return false
			}
		}
	}
	for key, field := range v.Dimensions {
		if !operationPattern.MatchString(key) || !validText(field.Label, 240) ||
			!slices.Contains([]string{"string", "number", "integer", "boolean", "date"}, field.Type) ||
			(c.Driver == "readonly_sql" && !identifierPattern.MatchString(field.Source)) ||
			(c.Driver != "readonly_sql" && !validText(field.Source, 120)) {
			return false
		}
	}
	if v.Time != "" && v.Dimensions[v.Time].Type != "date" {
		return false
	}
	for key, metric := range v.Metrics {
		if !operationPattern.MatchString(key) || v.Dimensions[key].Label != "" || !validText(metric.Label, 240) {
			return false
		}
		_, canonical, _, err := viewFormula(metric.Formula, v.Dimensions)
		if err != nil {
			return false
		}
		metric.Formula = canonical
		v.Metrics[key] = metric
	}
	if !validViewFilters(v.Dimensions, v.Defaults) {
		return false
	}
	for key, source := range v.RLS {
		if (key != "employee_no" && key != "dept_id") || !validText(source, 120) ||
			(c.Driver == "readonly_sql" && !identifierPattern.MatchString(source)) {
			return false
		}
	}
	if v.Metrics == nil {
		v.Metrics = map[string]Metric{}
	}
	if v.Relationships == nil {
		v.Relationships = []Relationship{}
	}
	if v.Defaults == nil {
		v.Defaults = map[string]any{}
	}
	if v.RLS == nil {
		v.RLS = map[string]string{}
	}
	return true
}

func (s Server) configureView(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, view := r.PathValue("space"), r.PathValue("view")
	if !validID(space) || !validID(view) {
		return web.Fail(404, "找不到业务数据视图")
	}
	var input struct {
		SemanticView
		Expected int `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &input, 96<<10); err != nil {
		return err
	}
	if !validID(input.Connector) || input.Expected < 0 {
		return web.Fail(422, "请选择空间内的连接器")
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := requireSpaceAdmin(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		var connector Connector
		if err := tx.QueryRow(ctx, `select driver,settings,operations from business_connectors
		  where (id,space_id)=($1,$2) for share`, input.Connector, space).Scan(&connector.Driver, &connector.Settings, &connector.Operations); err != nil {
			return err
		}
		if !validateView(&input.SemanticView, connector) {
			return web.Fail(422, "请核对业务维度、连接器、时间字段和指标公式")
		}
		for _, relationship := range input.Relationships {
			field := input.Dimensions[relationship.Dimension]
			if field.Label == "" || !validID(relationship.RelatedView) || relationship.RelatedView == view {
				return web.Fail(422, "请核对关联维度")
			}
			var kind string
			if err := tx.QueryRow(ctx, `select coalesce(allowed_dimensions->($3::text)->>'type','') from semantic_data_views
			  where (id,space_id)=($1,$2) for share`, relationship.RelatedView, space, relationship.RelatedDimension).Scan(&kind); err != nil || kind != field.Type {
				return web.Fail(422, "关联维度须属于本空间且类型一致")
			}
		}
		args := []any{view, space, input.Connector, input.Name, input.Description, input.Relation, input.Operation, input.Dimensions,
			input.Metrics, input.Relationships, input.Time, input.Defaults, input.RLS, input.Sensitivity, actor.ID, input.Enabled}
		if input.Expected == 0 {
			if err := tx.QueryRow(ctx, `insert into semantic_data_views(id,space_id,connector_id,view_name,business_desc,relation_name,operation_name,
			  allowed_dimensions,verified_metrics_json,relationships,time_field,defaults,rls_column_mapping,sensitivity,verified_by,enabled)
			  values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) on conflict do nothing returning revision`, args...).Scan(&revision); err != nil {
				return err
			}
		} else if err := tx.QueryRow(ctx, `update semantic_data_views set view_name=$4,business_desc=$5,relation_name=$6,operation_name=$7,
		  allowed_dimensions=$8,verified_metrics_json=$9,relationships=$10,time_field=$11,defaults=$12,rls_column_mapping=$13,sensitivity=$14,
		  verified_by=$15,enabled=$16,revision=revision+1 where (id,space_id,connector_id,revision)=($1,$2,$3,$17) returning revision`,
			append(args, input.Expected)...).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "business.view.configure", view, map[string]any{"space": space, "revision": revision})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(409, "连接器或视图已变更，请刷新重试")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"id": view, "revision": revision})
}

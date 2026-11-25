package bizconnectors

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/xwb1989/sqlparser"
)

type ViewQuery struct {
	View       string         `json:"viewId"`
	Dimensions []string       `json:"dimensions,omitempty"`
	Metrics    []string       `json:"metrics,omitempty"`
	Filters    map[string]any `json:"filters,omitempty"`
	From       string         `json:"from,omitempty"`
	Until      string         `json:"until,omitempty"`
	Limit      int            `json:"limit,omitempty"`
}

type QueryProvenance struct {
	View           string            `json:"viewId"`
	ViewName       string            `json:"viewName"`
	Source         string            `json:"source"`
	Provider       string            `json:"provider"`
	From           string            `json:"from,omitempty"`
	Until          string            `json:"until,omitempty"`
	Access         string            `json:"access"`
	Filters        []string          `json:"filters"`
	Formulas       map[string]string `json:"formulas"`
	Rows           int               `json:"rows"`
	Truncated      bool              `json:"truncated,omitempty"`
	Sensitivity    string            `json:"sensitivity"`
	ViewRevision   int               `json:"-"`
	SourceRevision int               `json:"-"`
	ActorOnly      bool              `json:"-"`
}

type ViewResult struct {
	Rows       []map[string]any `json:"rows"`
	Provenance QueryProvenance  `json:"provenance"`
}

func dateValue(value any) string {
	var parsed time.Time
	var err error
	switch v := value.(type) {
	case time.Time:
		parsed = v
	case string:
		for _, layout := range []string{time.DateOnly, time.RFC3339, time.DateTime} {
			parsed, err = time.Parse(layout, v)
			if err == nil {
				return parsed.Format(time.DateOnly)
			}
		}
		return ""
	case json.Number:
		stamp, err := v.Int64()
		if err != nil || stamp <= 0 {
			return ""
		}
		parsed = time.Unix(stamp, 0)
	default:
		return ""
	}
	return parsed.UTC().Format(time.DateOnly)
}

func filterValue(kind string, value any) bool {
	if kind == "date" {
		text, ok := value.(string)
		return ok && dateValue(text) == text && len(text) == 10
	}
	if kind == "number" || kind == "integer" {
		return scalar(kind, value) && numericMetric(value) != nil
	}
	return scalar(kind, value)
}

func validViewFilters(fields map[string]ViewField, filters map[string]any) bool {
	if len(filters) > 12 {
		return false
	}
	for key, value := range filters {
		if !filterValue(fields[key].Type, value) {
			return false
		}
	}
	return true
}

type viewPlan struct {
	formulas   map[string]ast.Expr
	aggregates []metricAggregate
	filters    map[string]any
	limit      int
}

func makeViewPlan(v SemanticView, q ViewQuery, provenance *QueryProvenance) (viewPlan, error) {
	p := viewPlan{formulas: map[string]ast.Expr{}, filters: map[string]any{}, limit: 20}
	if (len(q.Dimensions) == 0 && len(q.Metrics) == 0) || len(q.Dimensions) > 8 || len(q.Metrics) > 8 ||
		q.Limit < 0 || q.Limit > 40 || !validViewFilters(v.Dimensions, q.Filters) {
		return p, ErrBadQuery
	}
	if q.Limit > 0 {
		p.limit = q.Limit
	}
	for i, key := range q.Dimensions {
		if _, ok := v.Dimensions[key]; !ok || slices.Contains(q.Dimensions[:i], key) {
			return p, ErrBadQuery
		}
	}
	for key, value := range v.Defaults {
		p.filters[key] = value
	}
	for key, value := range q.Filters {
		if _, exists := p.filters[key]; exists {
			return p, ErrBadQuery
		}
		p.filters[key] = value
	}
	for key := range p.filters {
		provenance.Filters = append(provenance.Filters, v.Dimensions[key].Label)
	}
	sort.Strings(provenance.Filters)
	if q.From != "" || q.Until != "" {
		from, first := time.Parse(time.DateOnly, q.From)
		until, last := time.Parse(time.DateOnly, q.Until)
		if v.Time == "" || first != nil || last != nil || !from.Before(until) || until.Sub(from) > 366*24*time.Hour {
			return p, ErrBadQuery
		}
	}
	for _, key := range q.Metrics {
		metric, ok := v.Metrics[key]
		if !ok || p.formulas[key] != nil {
			return p, ErrBadQuery
		}
		expression, formula, aggregates, err := viewFormula(metric.Formula, v.Dimensions)
		if err != nil {
			return p, err
		}
		provenance.Formulas[key], p.formulas[key] = formula, expression
		for _, aggregate := range aggregates {
			if !slices.Contains(p.aggregates, aggregate) {
				p.aggregates = append(p.aggregates, aggregate)
			}
		}
	}
	if len(p.aggregates)+len(q.Dimensions) > 40 {
		return p, ErrBadQuery
	}
	return p, nil
}

func (s Server) QueryView(ctx context.Context, space, actor string, query ViewQuery) (ViewResult, error) {
	var result ViewResult
	if !validID(query.View) || !validID(space) || !validID(actor) {
		return result, ErrBadQuery
	}
	rows, err := s.DB.Query(ctx, `select `+viewColumns+` from semantic_data_views v join business_connectors c on c.id=v.connector_id
	 where v.id=$1 and business_view_readable(v.id,$2,$3,v.revision,c.revision)`, query.View, space, actor)
	if err != nil {
		return result, err
	}
	view, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[SemanticView])
	if err != nil {
		return result, err
	}
	c, secret, err := s.load(ctx, view.Space, actor, view.Connector, false)
	if err != nil {
		return result, err
	}
	// Employee-bound views must use verified identity and predicates before fetching data.
	if c.AuthMode != "service" || len(view.RLS) != 0 {
		return result, ErrUnavailable
	}
	provenance := QueryProvenance{View: view.ID, ViewName: view.Name, Source: c.Name, Provider: c.Provider,
		From: query.From, Until: query.Until, Access: "按当前空间成员权限", Filters: []string{}, Formulas: map[string]string{},
		Sensitivity: view.Sensitivity, ViewRevision: view.Revision, SourceRevision: c.Revision}
	plan, err := makeViewPlan(view, query, &provenance)
	if err != nil {
		return result, err
	}
	var readable bool
	err = s.DB.QueryRow(ctx, "select business_view_readable($1,$2,$3,$4,$5)", view.ID, space, actor, view.Revision, c.Revision).Scan(&readable)
	if err != nil || !readable {
		return result, ErrUnavailable
	}
	if c.Driver == "readonly_sql" {
		result.Rows, err = s.queryWarehouseView(ctx, c, secret, view, query, plan)
	} else {
		result.Rows, err = s.queryEndpointView(ctx, c, secret, view, query, plan)
	}
	if err != nil {
		return ViewResult{}, err
	}
	err = s.DB.QueryRow(ctx, "select business_view_readable($1,$2,$3,$4,$5)", view.ID, space, actor, view.Revision, c.Revision).Scan(&readable)
	if err != nil || !readable {
		return ViewResult{}, ErrUnavailable
	}
	provenance.Truncated = len(result.Rows) > plan.limit
	result.Rows = result.Rows[:min(len(result.Rows), plan.limit)]
	provenance.Rows = len(result.Rows)
	result.Provenance = provenance
	return result, nil
}

func quotedField(name string) string { return "`" + name + "`" }
func quotedRelation(name string) string {
	return "`" + strings.ReplaceAll(name, ".", "`.`") + "`"
}

func viewLiteral(value any) string {
	if text, ok := value.(string); ok {
		return sqlparser.String(sqlparser.NewStrVal([]byte(text)))
	}
	return fmt.Sprint(value)
}

func (s Server) queryWarehouseView(ctx context.Context, c Connector, secret Credentials, view SemanticView, query ViewQuery, plan viewPlan) ([]map[string]any, error) {
	if !approvedRelation(view.Relation) {
		return nil, ErrBadQuery
	}
	selection, groups, predicates := []string{}, []string{}, []string{}
	for _, key := range query.Dimensions {
		column := quotedField(view.Dimensions[key].Source)
		selection = append(selection, column+" AS "+quotedField(key))
		groups = append(groups, column)
	}
	for i, part := range plan.aggregates {
		selection = append(selection, part.op+"("+quotedField(view.Dimensions[part.field].Source)+") AS _measure_"+strconv.Itoa(i))
	}
	keys := make([]string, 0, len(plan.filters))
	for key := range plan.filters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		predicates = append(predicates, quotedField(view.Dimensions[key].Source)+" = "+viewLiteral(plan.filters[key]))
	}
	if query.From != "" {
		field := quotedField(view.Dimensions[view.Time].Source)
		predicates = append(predicates, field+" >= "+viewLiteral(query.From), field+" < "+viewLiteral(query.Until))
	}
	statement := "SELECT " + strings.Join(selection, ",") + " FROM " + quotedRelation(view.Relation)
	if len(predicates) != 0 {
		statement += " WHERE " + strings.Join(predicates, " AND ")
	}
	if len(groups) != 0 {
		if len(plan.aggregates) != 0 {
			statement += " GROUP BY " + strings.Join(groups, ",")
		}
		statement += " ORDER BY " + strings.Join(groups, ",")
	}
	raw, err := s.read(ctx, c, secret, ReadRequest{SQL: statement, Limit: plan.limit + 1})
	if err != nil {
		return nil, err
	}
	for _, row := range raw.Rows {
		values := map[metricAggregate]*big.Rat{}
		for i, part := range plan.aggregates {
			alias := "_measure_" + strconv.Itoa(i)
			if row[alias] != nil {
				values[part] = numericMetric(row[alias])
				if values[part] == nil {
					return nil, ErrUnavailable
				}
			}
			delete(row, alias)
		}
		for key, expression := range plan.formulas {
			row[key] = formattedMetric(formulaValue(expression, values))
		}
	}
	return raw.Rows, nil
}

func (s Server) queryEndpointView(ctx context.Context, c Connector, secret Credentials, view SemanticView, query ViewQuery, plan viewPlan) ([]map[string]any, error) {
	op, err := readOperation(c, view.Operation)
	if err != nil {
		return nil, err
	}
	args := make(map[string]any)
	if query.From != "" && c.Category == "commerce" {
		from, _ := time.ParseInLocation(time.DateOnly, query.From, vendorChinaZone)
		until, _ := time.ParseInLocation(time.DateOnly, query.Until, vendorChinaZone)
		args["from"], args["until"] = from.Unix(), until.Unix()
	}
	pageSize := maxBusinessRows
	if c.Category == "commerce" {
		pageSize = 50
	} else if c.Provider == "fxiaoke" {
		pageSize = 100
	}
	raw, err := s.read(ctx, c, secret, ReadRequest{Operation: op.Name, Arguments: args, Limit: pageSize})
	if err != nil {
		return nil, err
	}
	filtered := len(plan.filters) != 0 || query.From != "" || len(plan.formulas) != 0
	if (filtered || len(raw.Rows) <= plan.limit) && (len(raw.Rows) >= pageSize || raw.Truncated) {
		return nil, ErrUnavailable
	}
	type viewGroup struct {
		values map[string]any
		rows   []map[string]any
	}
	groups := map[string]*viewGroup{}
	order := []string{}
	out := []map[string]any{}
	for _, row := range raw.Rows {
		match := true
		for key, value := range plan.filters {
			field := view.Dimensions[key]
			if field.Type == "date" {
				match = match && dateValue(row[field.Source]) == value
			} else {
				match = match && fmt.Sprint(row[field.Source]) == fmt.Sprint(value)
			}
		}
		if query.From != "" {
			date := dateValue(row[view.Dimensions[view.Time].Source])
			match = match && date >= query.From && date < query.Until
		}
		if !match {
			continue
		}
		projected := map[string]any{}
		for _, key := range query.Dimensions {
			field := view.Dimensions[key]
			value := row[field.Source]
			if date := dateValue(value); field.Type == "date" && date != "" {
				value = date
			}
			if value != nil && !filterValue(field.Type, value) {
				return nil, ErrUnavailable
			}
			projected[key] = value
		}
		if len(plan.formulas) == 0 {
			out = append(out, projected)
			continue
		}
		encoded, err := json.Marshal(projected)
		if err != nil {
			return nil, ErrUnavailable
		}
		group := string(encoded)
		if groups[group] == nil {
			groups[group] = &viewGroup{values: projected}
			order = append(order, group)
		}
		groups[group].rows = append(groups[group].rows, row)
	}
	if len(plan.formulas) != 0 && len(query.Dimensions) == 0 && len(groups) == 0 {
		groups["{}"] = &viewGroup{values: map[string]any{}}
		order = append(order, "{}")
	}
	sort.Strings(order)
	for _, group := range order {
		item := groups[group]
		values := map[metricAggregate]*big.Rat{}
		for _, part := range plan.aggregates {
			value, err := aggregateRows(item.rows, view.Dimensions[part.field].Source, part.op)
			if err != nil {
				return nil, err
			}
			values[part] = value
		}
		for key, expression := range plan.formulas {
			item.values[key] = formattedMetric(formulaValue(expression, values))
		}
		out = append(out, item.values)
	}
	return out, nil
}

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

type MutationSpec struct {
	PreviewOperation   string            `json:"previewOperation"`
	WriteOperation     string            `json:"writeOperation"`
	ReconcileOperation string            `json:"reconcileOperation"`
	RecordParam        string            `json:"recordParam"`
	ReconcileParam     string            `json:"reconcileParam"`
	ReconcileKeyField  string            `json:"reconcileKeyField"`
	ReceiptField       string            `json:"receiptField"`
	RevisionField      string            `json:"revisionField"`
	FieldLabels        map[string]string `json:"fields"`
	RLS                map[string]string `json:"rls,omitempty"`
}

type MutationPolicy struct {
	ID        string       `json:"id"`
	Space     string       `json:"spaceId"`
	Connector string       `json:"connectorId"`
	Label     string       `json:"label"`
	Spec      MutationSpec `json:"spec"`
	Enabled   bool         `json:"enabled"`
	Revision  int          `json:"revision"`
}

type MutationDefinition struct {
	ID     string            `json:"id"`
	Label  string            `json:"label"`
	Fields map[string]string `json:"fields"`
}

const mutationPolicyCols = `f.id,f.space_id,f.connector_id,f.label,f.spec,f.enabled,f.revision`

func policyOperations(c Connector, spec MutationSpec) (preview, write, reconcile Operation, err error) {
	if c.Driver != "openapi_rest" || !slices.Contains([]string{"erp", "crm", "commerce", "internal"}, c.Category) {
		return preview, write, reconcile, ErrBadQuery
	}
	preview, err = readOperation(c, spec.PreviewOperation)
	if err != nil || preview.Method != http.MethodGet || preview.Parameters[spec.RecordParam].Type != "string" {
		return preview, write, reconcile, ErrBadQuery
	}
	reconcile, err = readOperation(c, spec.ReconcileOperation)
	if err != nil || reconcile.Method != http.MethodGet || reconcile.Parameters[spec.ReconcileParam].Type != "string" || reconcile.Name == preview.Name {
		return preview, write, reconcile, ErrBadQuery
	}
	for _, item := range c.Operations {
		if item.Name == spec.WriteOperation && item.Risk == "write" &&
			slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodPatch}, item.Method) {
			write = item
			break
		}
	}
	if write.Name == "" || write.Parameters[spec.RecordParam].Type != "string" {
		return preview, write, reconcile, ErrBadQuery
	}
	for _, entry := range []struct {
		op   Operation
		name string
	}{{preview, spec.RecordParam}, {reconcile, spec.ReconcileParam}} {
		if field := entry.op.Parameters[entry.name]; !field.Required || (field.Location != "path" && field.Location != "query") {
			return preview, write, reconcile, ErrBadQuery
		}
		for name, field := range entry.op.Parameters {
			if field.Required && name != entry.name {
				return preview, write, reconcile, ErrBadQuery
			}
		}
	}
	return preview, write, reconcile, nil
}

func verifyWritePolicy(p MutationPolicy, c Connector) error {
	spec := p.Spec
	_, write, _, err := policyOperations(c, spec)
	if err != nil || !validText(p.Label, 320) || !identifierPattern.MatchString(spec.ReceiptField) || !identifierPattern.MatchString(spec.ReconcileKeyField) ||
		!identifierPattern.MatchString(spec.RevisionField) || len(spec.FieldLabels) == 0 || len(spec.FieldLabels) > 12 {
		return ErrBadQuery
	}
	if c.AuthMode == "obo" {
		if len(spec.RLS) != 2 || spec.RLS["employee_no"] == spec.RLS["dept_id"] ||
			!identifierPattern.MatchString(spec.RLS["employee_no"]) || !identifierPattern.MatchString(spec.RLS["dept_id"]) {
			return ErrBadQuery
		}
	} else if len(spec.RLS) != 0 {
		return ErrBadQuery
	}
	for name, field := range write.Parameters {
		_, exposed := spec.FieldLabels[name]
		if field.Required && name != spec.RecordParam && !exposed {
			return ErrBadQuery
		}
	}
	for name, label := range spec.FieldLabels {
		if !validText(label, 140) || name == spec.RecordParam || name == spec.RevisionField ||
			name == spec.RLS["employee_no"] || name == spec.RLS["dept_id"] || write.Parameters[name].Location != "body" {
			return ErrBadQuery
		}
	}
	return nil
}

func (s Server) VisibleMutations(ctx context.Context, run, actor string) ([]MutationDefinition, error) {
	rows, err := s.DB.Query(ctx, `select w.id,w.label,w.spec->'fields' from business_mutation_policies w
	 join business_connectors c on c.id=w.connector_id join projects p on p.space_id=w.space_id
	 join runs r on r.project_id=p.id and r.actor=$2
	 where r.id=$1 and w.enabled and c.enabled and space_role(w.space_id,$2) in ('member','space_admin') and event_run_write_allowed(r.id,c.id)
	  and (c.auth_mode='service' or exists(select from business_obo_grants g where g.connector_id=c.id and
	   g.member_id=$2 and business_obo_grant_usable(c.id,$2,g.grant_id)))
	 order by w.label,w.id limit 12`, run, actor)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[MutationDefinition])
}

func (s Server) listMutationPolicies(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到业务空间")
	}
	rows, err := s.DB.Query(r.Context(), `select `+mutationPolicyCols+` from business_mutation_policies f
	 where f.space_id=$1 and space_role(f.space_id,$2)='space_admin' order by f.label,f.id limit 100`, space, actor.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[MutationPolicy])
	if err != nil {
		return err
	}
	if items == nil {
		items = []MutationPolicy{}
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, items)
}

func (s Server) configureMutationPolicy(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("policy")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到写回配置")
	}
	var input struct {
		MutationPolicy
		Expected int `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &input, 16<<10); err != nil {
		return err
	}
	input.Label = strings.TrimSpace(input.Label)
	if !validID(input.Connector) || input.Expected < 0 {
		return web.Fail(422, "请核对写回连接器和版本")
	}
	ctx := r.Context()
	var version int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := requireSpaceAdmin(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		var c Connector
		err := tx.QueryRow(ctx, `select c.category,c.driver,c.auth_mode,c.operations from business_connectors c
		 where (c.id,c.space_id)=($1,$2) for share`, input.Connector, space).Scan(&c.Category, &c.Driver, &c.AuthMode, &c.Operations)
		if err != nil {
			return err
		}
		if err := verifyWritePolicy(input.MutationPolicy, c); err != nil {
			return web.Fail(422, "请核对原状态查询、可写字段、下游版本和回执查询")
		}
		if input.Expected == 0 {
			err = tx.QueryRow(ctx, `insert into business_mutation_policies(id,connector_id,space_id,label,spec,enabled,maintainer)
			 values($1,$2,$3,$4,$5,$6,$7) on conflict do nothing returning revision`, id, input.Connector, space, input.Label, input.Spec, input.Enabled, actor.ID).Scan(&version)
		} else {
			err = tx.QueryRow(ctx, `update business_mutation_policies set label=$4,spec=$5,enabled=$6,revision=revision+1
			 where (id,connector_id,space_id,revision)=($1,$2,$3,$7) returning revision`, id, input.Connector, space, input.Label, input.Spec, input.Enabled, input.Expected).Scan(&version)
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "business.mutation.configure", id, map[string]int{"revision": version})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(409, "写回配置已变化，请刷新核对")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"id": id, "revision": version})
}

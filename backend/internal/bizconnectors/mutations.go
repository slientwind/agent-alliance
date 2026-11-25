package bizconnectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type MutationRequest struct {
	Policy string         `json:"policyId"`
	Record string         `json:"recordKey"`
	Values map[string]any `json:"values"`
}

type MutationRef struct {
	Run            string
	Turn, Position int
}

type mutationPayload struct {
	Record   string         `json:"record"`
	Before   map[string]any `json:"before"`
	After    map[string]any `json:"after"`
	Version  string         `json:"recordVersion"`
	OBOGrant string         `json:"oboGrant,omitempty"`
}

type mutation struct {
	ID, Run, Project, Space, Connector, Policy, Key, Label, State, Actor, Approver, Receipt, Code string
	Revision, PolicyRevision, ConnectorRevision, Attempts                                         int
	Expires                                                                                       time.Time
	Cipher                                                                                        []byte
}

type MutationResult struct {
	ID      string `json:"mutationId"`
	Status  string `json:"status"`
	Receipt string `json:"externalReceiptNo,omitempty"`
	Code    string `json:"resultCode,omitempty"`
}

type MutationCard struct {
	MutationResult
	Run       string         `json:"runId"`
	Label     string         `json:"label"`
	System    string         `json:"systemName,omitempty"`
	Record    string         `json:"recordKey,omitempty"`
	Before    map[string]any `json:"before,omitempty"`
	After     map[string]any `json:"after,omitempty"`
	Revision  int            `json:"revision"`
	CanDecide bool           `json:"canDecide"`
}

func (s Server) MutationNotice(ctx context.Context, id, reviewer string) (MutationCard, error) {
	rows, err := s.DB.Query(ctx, `select `+mutationCols+` from pending_system_mutations a join runs r on r.id=a.run_id
	 join projects p on p.id=r.project_id and p.space_id=a.space_id and p.final_reviewer=a.approver_id
	 join project_members pm on pm.project_id=p.id and pm.member_id=$2
	 where a.id=$1 and a.approver_id=$2 and space_role(p.space_id,$2) in ('member','space_admin')
	  and a.status='pending' and a.expires_at>statement_timestamp() and r.status='running' and general_run_inputs_usable(r.id)`, id, reviewer)
	if err != nil {
		return MutationCard{}, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[mutation])
	if err != nil {
		return MutationCard{}, err
	}
	payload, err := s.openMutation(m)
	if err != nil {
		return MutationCard{}, err
	}
	if _, _, _, _, err := s.mutationConfig(ctx, m, payload); err != nil {
		return MutationCard{}, err
	}
	return MutationCard{MutationResult: m.result(), Run: m.Run, Label: m.Label, Record: payload.Record,
		Before: payload.Before, After: payload.After, Revision: m.Revision, CanDecide: true}, nil
}

func (s Server) listMutations(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	project := r.PathValue("id")
	if !validID(project) {
		return web.Fail(404, "找不到业务写回")
	}
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `select `+mutationCols+` from pending_system_mutations a join runs r on r.id=a.run_id
	 join projects p on p.id=r.project_id and p.space_id=a.space_id join project_members pm on pm.project_id=p.id and pm.member_id=$2
	 where p.id=$1 and $2 in (r.actor,a.approver_id) and space_role(p.space_id,$2) in ('member','space_admin')
	 order by a.created_at desc limit 80`, project, actor.ID)
	if err != nil {
		return err
	}
	mutations, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mutation])
	if err != nil {
		return err
	}
	items := make([]MutationCard, 0, len(mutations))
	for _, m := range mutations {
		item := MutationCard{MutationResult: m.result(), Run: m.Run, Label: m.Label, Revision: m.Revision}
		payload, err := s.openMutation(m)
		if err != nil {
			return err
		}
		var allowed, pending bool
		var system string
		err = s.DB.QueryRow(ctx, `select exists(select from business_connectors c join business_mutation_policies f on f.connector_id=c.id
		 where (c.id,c.revision,f.id,f.revision)=($1,$2,$3,$4) and c.enabled and f.enabled
		  and ($5='' or coalesce(business_obo_grant_usable($1,$6,nullif($5,'')::uuid),false))),
		 exists(select from projects p join runs r on r.project_id=p.id where r.id=$7 and r.status='running'
		 and p.final_reviewer=$8 and general_run_inputs_usable(r.id)), coalesce((select name from business_connectors where id=$1),'')`, m.Connector, m.ConnectorRevision, m.Policy, m.PolicyRevision,
			payload.OBOGrant, m.Actor, m.Run, actor.ID).Scan(&allowed, &pending, &system)
		if err != nil {
			return err
		}
		if allowed {
			item.System, item.Record, item.Before, item.After = system, payload.Record, payload.Before, payload.After
		} else {
			item.Receipt = ""
		}
		item.CanDecide = allowed && pending && m.Approver == actor.ID && m.State == "pending" && m.Expires.After(time.Now())
		items = append(items, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, items)
}

func (m mutation) result() MutationResult {
	return MutationResult{ID: m.ID, Status: m.State, Receipt: m.Receipt, Code: m.Code}
}

const mutationCols = `a.id,a.run_id,r.project_id,a.space_id,a.connector_id,a.policy_id,a.idempotency_key,a.action_name,
 a.status,r.actor,a.approver_id,coalesce(a.external_receipt_no,''),coalesce(a.result_code,''),a.revision,a.policy_revision,
 a.connector_revision,a.reconciled_attempts,a.expires_at,a.payload_cipher`

func (s Server) openMutation(m mutation) (mutationPayload, error) {
	var payload mutationPayload
	body, err := s.Vault.Open(m.Cipher, "business-mutation/"+m.ID)
	if err != nil {
		return payload, err
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	err = decoder.Decode(&payload)
	return payload, err
}

func mutationByRef(ctx context.Context, db *Server, ref MutationRef) (mutation, error) {
	rows, err := db.DB.Query(ctx, `select `+mutationCols+` from pending_system_mutations a join runs r on r.id=a.run_id
	 where (a.run_id,a.turn,a.position)=($1,$2,$3)`, ref.Run, ref.Turn, ref.Position)
	if err != nil {
		return mutation{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[mutation])
}

func (s Server) PrepareMutation(ctx context.Context, ref MutationRef, input MutationRequest) (MutationResult, error) {
	if !validID(input.Policy) || !pathValuePattern.MatchString(input.Record) || len(input.Values) == 0 || len(input.Values) > 12 {
		return MutationResult{}, ErrBadQuery
	}
	if saved, err := mutationByRef(ctx, &s, ref); err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return saved.result(), err
	}
	encodedRequest, err := json.Marshal(input)
	if err != nil {
		return MutationResult{}, err
	}
	fingerprint := web.Hash(ref.Run + "/" + string(encodedRequest))
	findDuplicate := func() (MutationResult, error) {
		var existing MutationResult
		err := s.DB.QueryRow(ctx, `select id,coalesce(external_receipt_no,'') from pending_system_mutations
		 where (run_id,policy_id,proposal_fingerprint)=($1,$2,$3)`, ref.Run, input.Policy, fingerprint).Scan(&existing.ID, &existing.Receipt)
		existing.Status = "duplicate"
		return existing, err
	}
	if duplicate, err := findDuplicate(); err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return duplicate, err
	}
	var actor, approver, project string
	var p MutationPolicy
	err = s.DB.QueryRow(ctx, `select r.actor,p.final_reviewer,p.id,`+mutationPolicyCols+` from runs r
	 join projects p on p.id=r.project_id join business_mutation_policies f on f.space_id=p.space_id
	 where (r.id,f.id)=($1,$2) and r.status='running' and general_run_inputs_usable(r.id) and event_run_write_allowed(r.id,f.connector_id)
	  and space_role(f.space_id,r.actor) in ('member','space_admin') and f.enabled
	  and space_role(f.space_id,p.final_reviewer) in ('member','space_admin')
	  and exists(select from project_members m where (m.project_id,m.member_id)=(p.id,p.final_reviewer))`, ref.Run, input.Policy).
		Scan(&actor, &approver, &project, &p.ID, &p.Space, &p.Connector, &p.Label, &p.Spec, &p.Enabled, &p.Revision)
	if err != nil {
		return MutationResult{}, err
	}
	c, credentials, err := s.load(ctx, p.Space, actor, p.Connector, false)
	if err != nil || verifyWritePolicy(p, c) != nil {
		return MutationResult{}, ErrUnavailable
	}
	var scope rowIdentity
	if c.AuthMode == "obo" {
		scope, credentials, err = s.loadOBO(ctx, c, actor, credentials)
		if err != nil {
			return MutationResult{}, err
		}
	}
	_, write, _, _ := policyOperations(c, p.Spec)
	values := make(map[string]any, len(input.Values)+1)
	values[p.Spec.RecordParam] = input.Record
	for key, value := range input.Values {
		if p.Spec.FieldLabels[key] == "" || !scalar(write.Parameters[key].Type, value) {
			return MutationResult{}, ErrBadQuery
		}
		values[key] = value
	}
	if err := validateArguments(write, values); err != nil {
		return MutationResult{}, err
	}
	payload := mutationPayload{Record: input.Record, After: input.Values, OBOGrant: scope.Grant}
	payload.Before, payload.Version, err = s.mutationSnapshot(ctx, c, credentials, p.Spec, scope, payload)
	if err != nil {
		return MutationResult{}, err
	}
	id := uuid.NewString()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return MutationResult{}, err
	}
	cipher, err := s.Vault.Seal(string(encoded), "business-mutation/"+id)
	if err != nil {
		return MutationResult{}, err
	}
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into pending_system_mutations(id,run_id,turn,position,space_id,connector_id,policy_id,
		  policy_revision,connector_revision,approver_id,action_name,payload_cipher,proposal_fingerprint,obo_grant_id)
		 select $1,r.id,$3,$4,p.space_id,c.id,f.id,f.revision,c.revision,p.final_reviewer,f.label,$11,$13,nullif($12,'')::uuid
		 from runs r join projects p on p.id=r.project_id join business_mutation_policies f on f.space_id=p.space_id
		 join business_connectors c on c.id=f.connector_id
		 where (r.id,f.id,c.id,f.revision,c.revision,r.actor,p.final_reviewer)=($2,$5,$6,$7,$8,$9,$10)
		  and r.status='running' and general_run_inputs_usable(r.id) and f.enabled and c.enabled and event_run_write_allowed(r.id,c.id)
		  and space_role(p.space_id,r.actor) in ('member','space_admin')
		  and space_role(p.space_id,p.final_reviewer) in ('member','space_admin')
		  and ($12='' or business_obo_grant_usable(c.id,r.actor,nullif($12,'')::uuid))
		 on conflict do nothing`, id, ref.Run, ref.Turn, ref.Position, p.ID, c.ID,
			p.Revision, c.Revision, actor, approver, cipher, scope.Grant, fingerprint)
		if err != nil || tag.RowsAffected() != 1 {
			return err
		}
		if _, err := tx.Exec(ctx, "update projects set revision=revision+1,updated_at=now() where id=$1", project); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor, "business.mutation.preview", id, map[string]string{"run": ref.Run, "policy": p.ID})
	})
	if err != nil {
		return MutationResult{}, err
	}
	saved, err := mutationByRef(ctx, &s, ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return findDuplicate()
	}
	return saved.result(), err
}

func (s Server) mutationSnapshot(ctx context.Context, c Connector, credentials Credentials, spec MutationSpec, scope rowIdentity, input mutationPayload) (map[string]any, string, error) {
	result, err := s.read(ctx, c, credentials, ReadRequest{Operation: spec.PreviewOperation,
		Arguments: map[string]any{spec.RecordParam: input.Record}, Limit: 2})
	if err != nil || len(result.Rows) != 1 || result.Truncated {
		return nil, "", ErrUnavailable
	}
	row := result.Rows[0]
	if scope.Grant != "" && (!matchesIdentityField(row[spec.RLS["employee_no"]], scope.Employee) ||
		!matchesIdentityField(row[spec.RLS["dept_id"]], scope.Department)) {
		return nil, "", ErrUnavailable
	}
	version := toOAuthValue(row[spec.RevisionField])
	if !validText(version, 160) {
		return nil, "", ErrUnavailable
	}
	projected := make(map[string]any, len(input.After))
	for key := range input.After {
		value, found := row[key]
		if !found || !validMutationField(value) {
			return nil, "", ErrUnavailable
		}
		projected[key] = value
	}
	return projected, version, nil
}

func validMutationField(value any) bool {
	switch v := value.(type) {
	case nil, bool:
		return true
	case string:
		return v == "" || validText(v, 4096)
	case json.Number:
		return numericMetric(v) != nil
	}
	return false
}

func sameMutationValues(a, b map[string]any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func DecideMutation(ctx context.Context, tx pgx.Tx, project, id, actor, medium string, expected int, allow bool) error {
	if !validID(id) || !validID(actor) || expected < 1 || (medium != "web" && medium != "feishu" && medium != "dingtalk") {
		return web.Fail(422, "审批信息不完整")
	}
	var current, run, linkedProject string
	var decider *string
	var revision int
	err := tx.QueryRow(ctx, `select a.status,a.revision,a.approved_by,a.run_id,p.id from pending_system_mutations a join runs r on r.id=a.run_id
	 join projects p on p.id=r.project_id join project_members pm on pm.project_id=p.id and pm.member_id=$3
	 where a.id=$2 and (nullif($1,'') is null or p.id=nullif($1,'')::uuid) and a.approver_id=$3 and p.final_reviewer=$3
	 and space_role(a.space_id,$3) in ('space_admin','member') and p.space_id=a.space_id for update of a`, project, id, actor).
		Scan(&current, &revision, &decider, &run, &linkedProject)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可审批的业务写回")
	}
	if err != nil {
		return err
	}
	state := "denied"
	if allow {
		state = "approved"
	}
	if decider != nil && *decider == actor && revision > expected && (current == state ||
		allow && (current == "executing" || current == "reconciling" || current == "succeeded" || current == "unconfirmed" || current == "failed")) {
		return nil
	}
	if current != "pending" || expected != revision {
		return web.Fail(409, "审批已更新，请刷新核对")
	}
	tag, err := tx.Exec(ctx, `update pending_system_mutations a set status=$4,approved_by=$3,decided_at=clock_timestamp(),
	 decision_channel=$5,revision=revision+1 where a.id=$2 and a.run_id=$1 and a.expires_at>clock_timestamp()
	 and exists(select from runs where id=$1 and status='running' and general_run_inputs_usable(id))`, run, id, actor, state, medium)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return web.Fail(409, "任务或确认时限已变化")
	}
	if _, err := tx.Exec(ctx, "update projects set revision=revision+1,updated_at=now() where id=$1", linkedProject); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "business.mutation."+state, id, map[string]string{"run": run, "channel": medium})
}

func (s Server) decideMutation(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	project, value := r.PathValue("id"), r.PathValue("mutation")
	if !validID(project) || !validID(value) {
		return web.Fail(404, "找不到可审批的业务写回")
	}
	var input struct {
		Expected int   `json:"expected"`
		Accept   *bool `json:"accept"`
	}
	if err := web.ReadLimited(w, r, &input, 1024); err != nil {
		return err
	}
	if input.Accept == nil {
		return web.Fail(422, "请选择确认或拒绝")
	}
	ctx := r.Context()
	if err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		return DecideMutation(ctx, tx, project, value, actor.ID, "web", input.Expected, *input.Accept)
	}); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

package bizconnectors

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"alliance/internal/database"
	"github.com/jackc/pgx/v5"
)

const mutationExecutionPermission = `exists(
 select from runs r join projects p on p.id=r.project_id join business_mutation_policies f on f.id=a.policy_id
 join business_connectors c on c.id=a.connector_id
 join project_members pm on pm.project_id=p.id and pm.member_id=a.approver_id
 where r.id=a.run_id and r.status='running' and general_run_inputs_usable(r.id) and event_run_write_allowed(r.id,c.id)
  and p.space_id=a.space_id and p.final_reviewer=a.approved_by and a.approver_id=a.approved_by
  and f.enabled and f.revision=a.policy_revision and c.enabled and c.revision=a.connector_revision
  and space_role(a.space_id,r.actor) in ('space_admin','member')
  and space_role(a.space_id,a.approver_id) in ('space_admin','member')
  and ($2='' or business_obo_grant_usable(c.id,r.actor,nullif($2,'')::uuid))
)`

func (s Server) StepMutation(ctx context.Context, ref MutationRef) (MutationResult, error) {
	m, err := mutationByRef(ctx, &s, ref)
	if err != nil {
		return MutationResult{}, err
	}
	switch m.State {
	case "pending", "approved":
		if !m.Expires.After(time.Now()) {
			if err := s.finishMutation(ctx, m, "expired", "approval_expired", ""); err != nil {
				return MutationResult{}, err
			}
		} else if m.State == "approved" {
			if err := s.performMutation(ctx, m); err != nil {
				return MutationResult{}, err
			}
		}
	case "reconciling", "executing":
		if m.Attempts >= 6 {
			if err := s.finishMutation(ctx, m, "unconfirmed", "receipt_pending", ""); err != nil {
				return MutationResult{}, err
			}
		}
	}
	m, err = mutationByRef(ctx, &s, ref)
	return m.result(), err
}

func (s Server) mutationConfig(ctx context.Context, m mutation, payload mutationPayload) (MutationPolicy, Connector, Credentials, rowIdentity, error) {
	var p MutationPolicy
	var scope rowIdentity
	rows, err := s.DB.Query(ctx, `select `+mutationPolicyCols+` from business_mutation_policies f
	 where (f.id,f.space_id,f.revision) = ($1,$2,$3) and f.enabled`, m.Policy, m.Space, m.PolicyRevision)
	if err != nil {
		return p, Connector{}, Credentials{}, scope, err
	}
	p, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[MutationPolicy])
	if err != nil {
		return p, Connector{}, Credentials{}, scope, err
	}
	c, credentials, err := s.load(ctx, m.Space, m.Actor, m.Connector, false)
	if err != nil || c.Revision != m.ConnectorRevision || verifyWritePolicy(p, c) != nil {
		return p, c, Credentials{}, scope, ErrUnavailable
	}
	if c.AuthMode == "obo" {
		scope, credentials, err = s.loadOBO(ctx, c, m.Actor, credentials)
		if err != nil || scope.Grant != payload.OBOGrant {
			return p, c, Credentials{}, scope, ErrUnavailable
		}
	}
	return p, c, credentials, scope, nil
}

func (s Server) performMutation(ctx context.Context, m mutation) error {
	payload, err := s.openMutation(m)
	if err != nil {
		return err
	}
	tag, err := s.DB.Exec(ctx, `update pending_system_mutations a set status='executing',attempt_at=clock_timestamp(),
	 next_check_at=clock_timestamp()+interval '45 seconds',revision=revision+1 where id=$1 and status='approved' and expires_at>clock_timestamp()
	 and `+mutationExecutionPermission, m.ID, payload.OBOGrant)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return s.finishMutation(ctx, m, "failed", "permission_changed", "")
	}
	m.State, m.Revision = "executing", m.Revision+1
	p, c, credentials, scope, err := s.mutationConfig(ctx, m, payload)
	if err != nil {
		return s.finishMutation(ctx, m, "failed", "permission_changed", "")
	}
	current, version, err := s.mutationSnapshot(ctx, c, credentials, p.Spec, scope, payload)
	if err != nil || version != payload.Version || !sameMutationValues(current, payload.Before) {
		return s.finishMutation(ctx, m, "failed", "record_changed", "")
	}
	_, op, _, err := policyOperations(c, p.Spec)
	if err != nil {
		return s.finishMutation(ctx, m, "failed", "permission_changed", "")
	}
	args := make(map[string]any, len(payload.After)+1)
	for key, value := range payload.After {
		args[key] = value
	}
	args[p.Spec.RecordParam] = payload.Record
	request, err := restRequest(ctx, c, credentials, op, args)
	if err != nil {
		return s.finishMutation(ctx, m, "failed", "invalid_proposal", "")
	}
	request.Header.Set("Idempotency-Key", m.Key)
	request.Header.Set("If-Match", payload.Version)
	var allowed bool
	err = s.DB.QueryRow(ctx, `select `+mutationExecutionPermission+` from pending_system_mutations a
	 where a.id=$1 and a.status='executing' and a.revision=$3`, m.ID, payload.OBOGrant, m.Revision).Scan(&allowed)
	if err != nil || !allowed {
		return s.finishMutation(ctx, m, "failed", "permission_changed", "")
	}
	response, err := s.businessHTTP(ctx, request)
	if err != nil {
		return s.finishMutation(ctx, m, "reconciling", "receipt_pending", "")
	}
	value, err := boundedBusinessJSON(response)
	if err != nil {
		return s.finishMutation(ctx, m, "reconciling", "receipt_pending", "")
	}
	result, err := readBusinessRows(value, op.ResultPath, 2)
	if err == nil && len(result.Rows) == 1 && !result.Truncated {
		if receipt := toOAuthValue(result.Rows[0][p.Spec.ReceiptField]); validText(receipt, 180) {
			return s.finishMutation(ctx, m, "succeeded", "", receipt)
		}
	}
	return s.finishMutation(ctx, m, "reconciling", "receipt_pending", "")
}

func (s Server) finishMutation(ctx context.Context, m mutation, state, code, receipt string) error {
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update pending_system_mutations set status=$4,result_code=nullif($5,''),external_receipt_no=nullif($6,''),
		 next_check_at=case when $4 in ('reconciling','unconfirmed') then clock_timestamp()+interval '10 seconds' else null end,
		 revision=revision+1 where (id,status,revision)=($1,$2,$3)`, m.ID, m.State, m.Revision, state, code, receipt)
		if err != nil || tag.RowsAffected() != 1 {
			return err
		}
		if _, err := tx.Exec(ctx, "update projects set revision=revision+1,updated_at=now() where id=$1", m.Project); err != nil {
			return err
		}
		return database.Audit(ctx, tx, m.Approver, "business.mutation."+state, m.ID, map[string]string{"run": m.Run, "outcome": code})
	})
}

func (s Server) reconcileMutation(ctx context.Context) (bool, error) {
	rows, err := s.DB.Query(ctx, `with due as (
	 select a.id from pending_system_mutations a where a.status in ('executing','reconciling','unconfirmed')
	  and a.next_check_at<=clock_timestamp() and a.attempt_at>clock_timestamp()-interval '7 days'
	 order by a.next_check_at,a.id for update skip locked limit 1
	), leased as (
	 update pending_system_mutations a set next_check_at=clock_timestamp()+interval '1 minute',
	  reconciled_attempts=reconciled_attempts+1 from due where a.id=due.id returning a.*
	)
	select `+mutationCols+` from leased a join runs r on r.id=a.run_id`)
	if err != nil {
		return false, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[mutation])
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	payload, err := s.openMutation(m)
	if err != nil {
		return true, err
	}
	p, c, credentials, scope, err := s.mutationConfig(ctx, m, payload)
	if err != nil {
		return true, nil
	}
	rowsResult, err := s.read(ctx, c, credentials, ReadRequest{Operation: p.Spec.ReconcileOperation, Limit: 2,
		Arguments: map[string]any{p.Spec.ReconcileParam: m.Key}})
	if err != nil || len(rowsResult.Rows) != 1 || rowsResult.Truncated {
		return true, nil
	}
	row := rowsResult.Rows[0]
	if !matchesIdentityField(row[p.Spec.ReconcileKeyField], m.Key) || scope.Grant != "" &&
		(!matchesIdentityField(row[p.Spec.RLS["employee_no"]], scope.Employee) || !matchesIdentityField(row[p.Spec.RLS["dept_id"]], scope.Department)) {
		return true, nil
	}
	if receipt := toOAuthValue(row[p.Spec.ReceiptField]); validText(receipt, 180) &&
		slices.Contains([]string{"executing", "reconciling", "unconfirmed"}, m.State) {
		return true, s.finishMutation(ctx, m, "succeeded", "", receipt)
	}
	return true, nil
}

func (s Server) ServeMutationReconciliation(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		for range 15 {
			busy, err := s.reconcileMutation(ctx)
			if err != nil && ctx.Err() == nil {
				slog.Warn("business write reconciliation failed", "error", err)
			}
			if !busy || err != nil {
				break
			}
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

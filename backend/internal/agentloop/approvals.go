package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const approvalScope = ` from general_agent_tool_approvals q
	join general_agent_actions a using(run_id,turn,position)
	join runs r on r.id=q.run_id
	join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version)
	where p.id=$1 and r.status in ('running','paused') and a.state='queued'
	and general_mcp_binding_current(r.id,a.mcp_server) and general_tool_approver(r.id,q.level,$2)`

type toolApproval struct {
	ID          string                `json:"id"`
	Level       harness.AutonomyLevel `json:"level"`
	Revision    int                   `json:"revision"`
	Tool        string                `json:"tool"`
	Recipient   string                `json:"recipient"`
	PlanVersion int                   `json:"planVersion"`
	Arguments   json.RawMessage       `json:"arguments,omitempty"`
}

func (s Server) toolApprovals(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	project := r.PathValue("id")
	if _, err := uuid.Parse(project); err != nil {
		return web.Fail(404, "找不到这段对话")
	}
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `select q.id,q.level,q.revision,a.mcp_tool_label,a.mcp_recipient,p.plan_version,
	 q.run_id,q.turn,q.position,case when r.actor=$2 then a.args_cipher end`+
		approvalScope+` and q.state='pending' order by q.created_at,q.id`, project, actor.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	approvals := make([]toolApproval, 0)
	for rows.Next() {
		var approval toolApproval
		var ref ToolRef
		var cipher []byte
		err := rows.Scan(&approval.ID, &approval.Level, &approval.Revision, &approval.Tool, &approval.Recipient, &approval.PlanVersion,
			&ref.RunID, &ref.Turn, &ref.Position, &cipher)
		if err != nil {
			return err
		}
		if cipher != nil {
			arguments, err := s.Vault.Open(cipher, actionScope(ref)+"/arguments")
			if err != nil {
				return err
			}
			approval.Arguments = json.RawMessage(arguments)
		}
		approvals = append(approvals, approval)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, approvals)
}

func (s Server) decideToolApproval(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	project, approval := r.PathValue("id"), r.PathValue("approval")
	for _, id := range []string{project, approval} {
		if _, err := uuid.Parse(id); err != nil {
			return web.Fail(404, "找不到这项确认")
		}
	}
	var in struct {
		Expected int   `json:"expected"`
		Accept   *bool `json:"accept"`
	}
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	if in.Expected < 1 || in.Accept == nil {
		return web.Fail(422, "请选择继续或跳过")
	}
	state := "denied"
	if *in.Accept {
		state = "approved"
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var run, prior, decider string
		var revision int
		err := tx.QueryRow(ctx, `select r.id,q.state,coalesce(q.decided_by::text,''),q.revision`+approvalScope+` and q.id=$3`,
			project, actor.ID, approval).Scan(&run, &prior, &decider, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(404, "这项确认已失效或不可处理")
		}
		if err != nil {
			return err
		}
		if prior == state && decider == actor.ID && revision == in.Expected+1 {
			return nil
		}
		d, err := loadRun(ctx, tx, run)
		if err != nil {
			return err
		}
		if !d.authorized || d.terminal() {
			return web.Fail(409, "任务已变化，请刷新对话")
		}
		tag, err := tx.Exec(ctx, `update general_agent_tool_approvals set state=$4,decided_by=$2,
		 decided_at=now(),revision=revision+1,next_wake_at=now()
		 where id=(select q.id`+approvalScope+` and q.id=$3 and q.state='pending' and q.revision=$5)`,
			project, actor.ID, approval, state, in.Expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "这项确认已更新，请刷新对话")
		}
		if err := touchApprovalProject(ctx, tx, project); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "general_tool."+state, approval, map[string]string{"run": run})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func touchApprovalProject(ctx context.Context, tx pgx.Tx, project string) error {
	_, err := tx.Exec(ctx, "update projects set revision=revision+1,updated_at=now() where id=$1", project)
	return err
}

func approvalState(ctx context.Context, tx pgx.Tx, ref ToolRef, d runData, task toolWork) (string, error) {
	var state string
	tag, err := tx.Exec(ctx, `insert into general_agent_tool_approvals(run_id,turn,position,level,fingerprint)
	 values($1,$2,$3,$4,$5) on conflict do nothing`, ref.RunID, ref.Turn, ref.Position, task.level, task.fingerprint)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 1 {
		return "pending", touchApprovalProject(ctx, tx, d.project)
	}
	err = tx.QueryRow(ctx, `select state from general_agent_tool_approvals where (run_id,turn,position)=($1,$2,$3)
	 and level=$4 and fingerprint=$5`, ref.RunID, ref.Turn, ref.Position, task.level, task.fingerprint).Scan(&state)
	return state, err
}

func (a Activities) verifyApproval(ctx context.Context, ref ToolRef, level harness.AutonomyLevel, fingerprint string) error {
	var approved bool
	err := a.DB.QueryRow(ctx, `select exists(select from general_agent_tool_approvals q
	 join general_agent_actions a using(run_id,turn,position)
	 join runs r on r.id=q.run_id
	 where (q.run_id,q.turn,q.position)=($1,$2,$3) and q.level=$4 and q.fingerprint=$5 and a.fingerprint=$5
	  and r.status='running' and a.state='running' and q.state='approved' and
	  general_tool_approver(r.id,q.level,q.decided_by))`,
		ref.RunID, ref.Turn, ref.Position, level, fingerprint).Scan(&approved)
	if err != nil {
		return err
	}
	if !approved {
		return web.Fail(409, "工具授权已失效")
	}
	return nil
}

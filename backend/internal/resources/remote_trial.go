package resources

import (
	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"bytes"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

func (c Catalog) saveTrial(ctx context.Context, id string, m Model, expected string, reply Reply, callErr error, lease string) (string, string, error) {
	status, body := "failed", reply.Text
	receipt := reply.Receipt
	if callErr != nil {
		status = "unknown"
		body = "调用结果待核对"
		var invalid *ToolInputError
		if errors.As(callErr, &invalid) {
			status, body = "failed", invalid.Reason
		} else if errors.As(callErr, &receipt) {
			switch receipt.State {
			case "canceled", "failed", "rejected", "completed":
				status = "failed"
			default:
				status = "remote"
			}
			body = "远端状态 · " + receipt.State
		} else if lease != "" {
			status = "remote"
			body = "查询未取得有效回执，仍可核对原任务"
		}
	} else if strings.Contains(body, expected) {
		status = "passed"
	}
	if errors.Is(callErr, ErrImageFormat) || errors.Is(callErr, ErrSlideContract) || errors.Is(callErr, ErrOfficeFile) {
		status, body = "failed", callErr.Error()
	}
	if IsCredentialRejection(callErr) {
		status = "failed"
		body = "供应商拒绝此凭据"
	}
	var task, correlation, state, tokens any
	if receipt != nil {
		task = receipt.Task
		correlation = receipt.Context
		state = receipt.State
	}
	if callErr == nil && !m.External && m.Tool == nil && !ProducesPNG(m.OutputOperation) {
		tokens = reply.Tokens
	}
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var agent string
		if err := tx.QueryRow(ctx, `select a.id from validations v join agents a on a.id=v.agent_id
            where v.id=$1 for no key update of a`, id).Scan(&agent); err != nil {
			return err
		}
		discardFile := false
		if status == "passed" {
			matches, err := builder.TrialMatches(ctx, tx, id)
			if err != nil {
				return err
			}
			if !matches {
				status, body, discardFile = "failed", "试运行期间 Agent 定义已变化，请核对修订后复验", true
			}
		}
		if err := ObserveKeyRejection(ctx, tx, m, callErr); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `update validations set status=case when $1='passed' and $2<>expected and exists(select from agent_checks c where c.id=validations.check_id and c.match_kind='exact') then 'failed' else $1 end,evidence=$2,tokens=$3,remote_task=coalesce($4,remote_task),remote_context=coalesce($5,remote_context),remote_status=coalesce($6,remote_status),ended_at=case when $1='remote' then null else now() end,follow_lease=null,follow_until=null where id=$7 and follow_lease is not distinct from nullif($8,'')::uuid returning status`, status, body, tokens, task, correlation, state, id, lease).Scan(&status)
		if err == pgx.ErrNoRows {
			err = web.Fail(409, "此回执已由另一次核对接管")
		}
		if err != nil || callErr != nil || reply.File == nil || discardFile {
			return err
		}
		hash, _, err := knowledge.ConfiguredObjects().Put(ctx, tx, bytes.NewReader(reply.File.Data), 6<<20)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into validation_files values($1,$2,$3,$4)", id, hash, reply.File.Name, reply.File.Mime)
		return err
	})

	return status, body, err
}
func (c Catalog) trialReceipt(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Action string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Action != "check" && in.Action != "cancel" {
		return web.Fail(422, "请选择查询或取消原试运行")
	}
	ctx := r.Context()
	agent, id := r.PathValue("id"), r.PathValue("trial")
	var receipt RemoteWait
	var rev int
	var lease, expected string
	var model Model
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var busy, cancelled bool
		err := tx.QueryRow(ctx, `select revision,expected,remote_task,coalesce(remote_context,''),coalesce(follow_until>now(),false),cancel_requested from validations where id=$1 and agent_id=$2 and actor=$3 and status='remote' for update`, id, agent, u.ID).Scan(&rev, &expected, &receipt.Task, &receipt.Context, &busy, &cancelled)
		if err != nil {
			return err
		}
		if busy || (cancelled && in.Action == "cancel") {
			return web.Fail(409, "已有查询或取消请求，请查看原回执")
		}
		matches, err := builder.TrialMatches(ctx, tx, id)
		if err != nil {
			return err
		}
		if !matches {
			return web.Fail(409, "原试运行的远端配置已变化，请由维护人核对原回执")
		}
		model, _, err = c.Resolve(ctx, tx, u.ID, agent, rev)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `update validations set follow_lease=gen_random_uuid(),follow_until=now()+interval '35 seconds',cancel_requested=cancel_requested or $2 where id=$1 returning follow_lease`, id, in.Action == "cancel").Scan(&lease)
	})
	if err != nil {
		return err
	}
	reply, callErr := model.CheckRemote(ctx, receipt, in.Action == "cancel")
	status, body, err := c.saveTrial(context.WithoutCancel(ctx), id, model, expected, reply, callErr, lease)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"id": id, "status": status, "output": body})
}

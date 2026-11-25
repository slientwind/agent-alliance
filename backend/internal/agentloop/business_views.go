package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"alliance/internal/bizconnectors"
	"alliance/internal/capability"
	"alliance/internal/harness"
	"alliance/internal/llmgateway"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const businessViewList = "list_business_views"
const businessViewQuery = "query_business_view"

var businessTools = []registeredTool{
	{businessViewList, "列出本次任务可用的已核定业务数据视图、维度、指标与关联键。", `{"type":"object","properties":{},"additionalProperties":false}`, harness.ReadOnly, capability.Knowledge},
	{businessViewQuery, "按已核定的语义视图编号和字段取数。日期采用 YYYY-MM-DD，until 不包含当日。仅使用已列出的维度和指标。", `{"type":"object","properties":{"viewId":{"type":"string","format":"uuid"},"dimensions":{"type":"array","maxItems":8,"items":{"type":"string"}},"metrics":{"type":"array","maxItems":8,"items":{"type":"string"}},"filters":{"type":"object","maxProperties":12},"from":{"type":"string","format":"date"},"until":{"type":"string","format":"date"},"limit":{"type":"integer","minimum":1,"maximum":40}},"required":["viewId"],"additionalProperties":false}`, harness.ReadOnly, capability.Knowledge},
}

func (a Activities) addBusinessTools(ctx context.Context, ref TurnRef, data runData, request *llmgateway.Request) error {
	if a.Business == nil || !capability.Allows(data.capabilities, capability.Knowledge) {
		return nil
	}
	var available bool
	err := a.DB.QueryRow(ctx, `select exists(select from runs r join projects p on p.id=r.project_id
	 join semantic_data_views v on true join business_connectors c on c.id=v.connector_id
	 where r.id=$1 and business_view_readable(v.id,p.space_id,r.actor,v.revision,c.revision))`, ref.RunID).Scan(&available)
	if err != nil || !available {
		return err
	}
	for _, tool := range businessTools {
		request.Tools = append(request.Tools, tool.definition())
	}
	return nil
}

func (a Activities) executeBusinessTool(ctx context.Context, ref ToolRef, task toolWork) (resources.Reply, *bizconnectors.QueryProvenance, error) {
	if a.Business == nil {
		return resources.Reply{}, nil, policy.ErrDenied
	}
	var space string
	err := a.DB.QueryRow(ctx, `select p.space_id from runs r join projects p on p.id=r.project_id
	 where r.id=$1 and r.actor=$2 and r.status='running' and general_run_inputs_usable(r.id)`, ref.RunID, task.actor).Scan(&space)
	if err != nil {
		return resources.Reply{}, nil, err
	}
	if task.name == businessViewList {
		if err := decodeToolArgs(task.args, new(struct{})); err != nil {
			return resources.Reply{}, nil, harness.ErrToolArguments
		}
		views, err := a.Business.Views(ctx, space, task.actor, false)
		if err != nil {
			return resources.Reply{}, nil, err
		}
		visible := views[:0]
		for _, view := range views {
			if view.Sensitivity == "public" || view.Sensitivity == "internal" {
				visible = append(visible, view)
			}
		}
		views = visible
		if len(views) > 12 {
			views = views[:12]
		}
		for {
			body, err := json.Marshal(views)
			if err != nil || len(body) <= 8192 || len(views) == 0 {
				return resources.Reply{Text: string(body)}, nil, err
			}
			views = views[:len(views)-1]
		}
	}
	var input bizconnectors.ViewQuery
	decoder := json.NewDecoder(strings.NewReader(task.args))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&input); err != nil {
		return resources.Reply{}, nil, harness.ErrToolArguments
	}
	value, err := a.Business.QueryView(ctx, space, task.actor, input)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return resources.Reply{}, nil, policy.ErrDenied
	case errors.Is(err, bizconnectors.ErrBadQuery):
		return resources.Reply{}, nil, harness.ErrToolArguments
	case err != nil:
		return resources.Reply{}, nil, err
	}
	for {
		body, err := json.Marshal(value)
		if err != nil || len(body) <= 8192 {
			return resources.Reply{Text: string(body)}, &value.Provenance, err
		}
		if len(value.Rows) == 0 {
			return resources.Reply{}, nil, harness.ErrContextCapacity
		}
		value.Rows = value.Rows[:len(value.Rows)-1]
		value.Provenance.Rows, value.Provenance.Truncated = len(value.Rows), true
	}
}

func (a Activities) recordBusinessProvenance(ctx context.Context, tx pgx.Tx, ref ToolRef, data runData, provenance bizconnectors.QueryProvenance) error {
	tag, err := tx.Exec(ctx, `insert into business_query_provenance(run_id,turn,position,view_id,view_revision,connector_revision,restricted_to_actor,provenance,obo_grant_id)
	 select r.id,$2,$3,v.id,$5,$6,$8,$9,nullif($10,'')::uuid from runs r join projects p on p.id=r.project_id join semantic_data_views v on v.id=$4
	 where r.id=$1 and r.actor=$7 and business_view_readable(v.id,p.space_id,$7,$5,$6)
	 and ($10='' or business_obo_grant_usable(v.connector_id,$7,nullif($10,'')::uuid))
	 on conflict do nothing`, ref.RunID, ref.Turn, ref.Position, provenance.View, provenance.ViewRevision, provenance.SourceRevision, data.actor, provenance.ActorOnly, provenance, provenance.OBOGrant)
	if err == nil && tag.RowsAffected() != 1 {
		return policy.ErrDenied
	}
	return err
}

func (a Activities) attachBusinessProtection(ctx context.Context, ref TurnRef, data runData, call *llmgateway.CallIdentity) error {
	if ref.Turn < 2 {
		return nil
	}
	rows, err := a.DB.Query(ctx, `select x.call_id,b.provenance->>'sensitivity'
	 from business_query_provenance b join general_agent_actions x using(run_id,turn,position)
	 where b.run_id=$1 and b.turn<$2 order by b.turn,b.position`, ref.RunID, ref.Turn)
	if err != nil {
		return err
	}
	type protection struct{ ID, Sensitivity string }
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[protection])
	if err != nil || len(items) == 0 {
		return err
	}
	labels := map[string]policy.Sensitivity{}
	for _, item := range items {
		labels[item.ID] = policy.Sensitivity(item.Sensitivity)
	}
	before := call.Prepare
	call.Prepare = func(ctx context.Context, candidate llmgateway.Candidate, input llmgateway.Request) (llmgateway.PreparedRequest, error) {
		prepared := llmgateway.PreparedRequest{Request: input}
		var err error
		if before != nil {
			prepared, err = before(ctx, candidate, input)
			if err != nil {
				return prepared, err
			}
		}
		prepared.Request.Messages = append([]resources.Message(nil), prepared.Request.Messages...)
		for i := range prepared.Request.Messages {
			message := &prepared.Request.Messages[i]
			label, found := labels[message.ToolCallID]
			if message.Role != "tool" || !found {
				continue
			}
			guard := policy.ExternalDLP{DB: a.DB, Vault: a.Vault, Run: ref.RunID, Actor: data.actor, Endpoint: candidate.Endpoint,
				Exchange: fmt.Sprintf("business/%d/%s/%d", ref.Turn, candidate.ModelID, i), Classification: label}
			safe, err := guard.ProtectExternal(ctx, "business/view", []string{message.Content})
			if errors.Is(err, resources.ErrExternalContentBlocked) {
				message.Content = `{"status":"rejected","text":"此业务视图的密级限制了模型读取。"}`
				continue
			}
			if err != nil {
				return prepared, err
			}
			message.Content = safe[0]
		}
		prepared.Request.EmitText = nil
		proceed := prepared.Authorize
		prepared.Authorize = func(ctx context.Context) error {
			if proceed != nil {
				if err := proceed(ctx); err != nil {
					return err
				}
			}
			var readable bool
			if err := a.DB.QueryRow(ctx, "select business_run_evidence_readable($1,$2) and general_run_inputs_usable($1)", ref.RunID, data.actor).Scan(&readable); err != nil {
				return err
			}
			if !readable {
				return web.Fail(409, "业务视图授权已变化")
			}
			return nil
		}
		return prepared, nil
	}
	return nil
}

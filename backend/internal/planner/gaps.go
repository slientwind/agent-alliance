package planner

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
)

func (s Server) bindGap(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Exact                                                        bool
		Expected, BindingRevision, Revision                          int
		Agent, Mode, Example, ExpectedOutput, Reviewer, Instructions string
		Tool                                                         *resources.Tool
		Draft                                                        resources.DraftConfig
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Exact && in.Mode != "draft" {
		return web.Fail(422, "逐字验收需要新建文本模型草稿")
	}
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil || slot < 0 {
		return web.Fail(422, "缺口编号无效")
	}
	ctx := r.Context()
	project, request := r.PathValue("id"), r.PathValue("request")
	var agent string
	var version int
	err = database.WithTx(ctx, s.Catalog.DB, func(tx pgx.Tx) error {
		var suggestion Suggestion
		var current, planned int
		var stage, team string
		err := tx.QueryRow(ctx, `select q.result,p.revision,q.project_revision,p.stage,p.team_id from projects p join current_planning_requests q on q.project_id=p.id where p.id=$1 and p.owner=$2 and q.actor=$2 and exists(select from project_members pm where pm.project_id=p.id and pm.member_id=$2) and q.id=$3 and q.current_status='ready' for update of p`, project, u.ID, request).Scan(&suggestion, &current, &planned, &stage, &team)
		if err != nil {
			return err
		}
		if current != in.Expected || current != planned+1 || stage != "brief" && stage != "plan" || slot >= len(suggestion.Missing) {
			return web.Fail(409, "建议已变化，请重新核对任务")
		}
		var binding int
		if err = tx.QueryRow(ctx, "select coalesce(max(revision),0) from gap_bindings where request_id=$1 and slot=$2", request, slot).Scan(&binding); err != nil {
			return err
		}
		if binding != in.BindingRevision {
			return web.Fail(409, "此缺口已有新的能力选择")
		}
		switch in.Mode {
		case "draft", "tool", "image":
			if in.Draft.ID != "" || in.Draft.Expected != 0 || web.Blank(in.Example) || web.Blank(in.ExpectedOutput) {
				return web.Fail(422, "新草稿需要试运行样例与验收片段")
			}
			if in.Mode == "tool" {
				if in.Tool == nil || in.Draft.Resource != "" || in.Draft.Key != "" || in.Draft.ResourceRevision != 0 {
					return web.Fail(422, "请指定内置工具操作")
				}
				agent, version, err = s.Catalog.SaveToolDraftTx(ctx, tx, u.ID, resources.ToolDraft{
					Team: team, Name: in.Draft.Name, Description: in.Draft.Instructions, Tool: *in.Tool,
				})
			} else {
				agent, version, err = s.Catalog.SaveDraftTx(ctx, tx, u.ID, in.Draft)
			}
			if err != nil {
				return err
			}
			if in.Mode != "tool" {
				var image bool
				if err = tx.QueryRow(ctx, `select agent_file_format(v)='png' from agent_revisions v where v.agent_id=$1 and v.revision=$2`, agent, version).Scan(&image); err != nil {
					return err
				}
				if image && in.Mode == "draft" {
					return web.Fail(422, "生图草稿需要样图要求和看图验收人")
				}
				if !image && in.Mode == "image" {
					return web.Fail(422, "样图检查需要能生成 PNG 的模型修订")
				}
			}
			if in.Exact {
				err = resources.SetChecksTx(ctx, tx, u.ID, agent, version, 0, []resources.CheckDefinition{{
					Name: "本步输出逐字核对", Sample: in.Example, Expected: in.ExpectedOutput, Match: "exact",
				}})
				if err != nil {
					return err
				}
			}
			if in.Mode == "image" {
				if web.Blank(in.Reviewer) || in.Tool != nil {
					return web.Fail(422, "请指定样图验收人和生图模型")
				}
				err = resources.SetChecksTx(ctx, tx, u.ID, agent, version, 0, []resources.CheckDefinition{{
					Name: in.ExpectedOutput, Sample: in.Example, Reviewer: in.Reviewer, Expected: "PNG 图像", Match: "contains",
				}})
				if err != nil {
					return err
				}
			}
		case "revise":
			// The binding determines the Agent and the accepted sample. A caller
			// cannot switch either while claiming to edit the instructions.
			if in.Agent != "" || in.Tool != nil || in.Draft != (resources.DraftConfig{}) || in.Example != "" || in.ExpectedOutput != "" || in.Reviewer != "" {
				return web.Fail(422, "调整职责时沿用本缺口的 Agent、样例和模型配置")
			}
			var base int
			err = tx.QueryRow(ctx, `select g.agent_id,g.agent_revision,g.example_input,g.expected_output
 from gap_bindings g join agents a on a.id=g.agent_id join team_members tm on tm.team_id=a.team_id and tm.member_id=$3
 where g.request_id=$1 and g.slot=$2 and g.actor=$3 and g.agent_revision=$4
 and g.example_input<>'' and g.expected_output<>'' and a.published_revision is distinct from g.agent_revision
 for update of a for share of tm`, request, slot, u.ID, in.Revision).Scan(&agent, &base, &in.Example, &in.ExpectedOutput)
			if err != nil {
				return err
			}
			version, err = s.Catalog.ReviseInstructionsTx(ctx, tx, u.ID, agent, base, in.Instructions)
			if err != nil {
				return err
			}
		case "reuse":
			agent, version = in.Agent, in.Revision
			choices, err := candidates(ctx, tx, u.ID, project, "", "", "")
			if err != nil {
				return err
			}
			ready := false
			for _, c := range choices {
				if c.ID == agent && c.Revision == version && c.State == "ready" {
					ready = true
					break
				}
			}
			if !ready {
				return web.Fail(409, "此版本的使用条件已变化，请重新比较")
			}
			if _, _, err = s.Catalog.ResolveRun(ctx, tx, project, u.ID, agent, version); err != nil {
				return err
			}
		default:
			return web.Fail(422, "请选择创建草稿或复用已有能力")
		}
		if needed := suggestion.formatForGap(suggestion.Missing[slot]); needed != "" {
			var format string
			var inputOK bool
			if err = tx.QueryRow(ctx, `select agent_file_format(v),agent_accepts_planning_inputs(v,q.input_sources)
				from agent_revisions v cross join planning_requests q where v.agent_id=$1 and v.revision=$2 and q.id=$3`, agent, version, request).Scan(&format, &inputOK); err != nil {
				return err
			}
			if !inputOK {
				return web.Fail(422, "分组汇总需要 CSV 资料，请选择适合本次输入的能力")
			}
			if format != needed {
				return web.Fail(422, "该修订的产出类型不符，请选择能够生成"+outputName(needed)+"的能力")
			}
		}
		if _, err = tx.Exec(ctx, `insert into gap_bindings(request_id,slot,revision,agent_id,agent_revision,actor,example_input,expected_output) values($1,$2,$3,$4,$5,$6,$7,$8)
  on conflict(request_id,slot) do update set revision=excluded.revision,agent_id=excluded.agent_id,agent_revision=excluded.agent_revision,actor=excluded.actor,example_input=excluded.example_input,expected_output=excluded.expected_output`, request, slot, binding+1, agent, version, u.ID, in.Example, in.ExpectedOutput); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "planning.gap."+in.Mode, request, map[string]any{"slot": slot, "agent": agent, "revision": version})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"agent": agent, "revision": version})
}

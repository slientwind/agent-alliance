package studio

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s Server) fromRun(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct{ Run string }
	if err := web.ReadLimited(w, r, &in, 2048); err != nil {
		return err
	}
	if _, err := uuid.Parse(in.Run); err != nil {
		return web.Fail(422, "请选择一条已完成的答复")
	}
	ctx := r.Context()
	var result draft
	status := http.StatusOK
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var team, brief string
		var tier llmgateway.Tier
		err := tx.QueryRow(ctx, `select p.team_id,left(plan.brief,3000),coalesce(g.tier,'default')
		 from runs r join projects p on p.id=r.project_id and p.owner=$2
		 join project_members pm on (pm.project_id,pm.member_id)=(p.id,$2)
		 join team_members tm on (tm.team_id,tm.member_id)=(p.team_id,$2)
		 join members u on u.id=$2 and u.enabled and u.kind='person'
		 join plans plan on (plan.project_id,plan.version)=(r.project_id,r.plan_version)
		 left join general_agent_runs g on g.run_id=r.id
		 where r.id=$1 and r.actor=$2 and r.status='completed' for share of p,r,tm,u`,
			in.Run, actor.ID).Scan(&team, &brief, &tier)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `select id,team_id,revision,spec from studio_drafts d
		 where (d.owner,d.source_run)=($1,$2)`, actor.ID, in.Run).Scan(&result.ID, &result.Team, &result.Revision, &result.Spec)
		if err == nil {
			_, err = getDraft(ctx, tx, result.ID, actor.ID, false)
			return err
		}
		if err != pgx.ErrNoRows {
			return err
		}
		actions, err := reusableRunActions(ctx, tx, in.Run)
		if err != nil {
			return err
		}
		result = draft{Team: team, Spec: distillSpec(brief, tier, actions)}
		err = tx.QueryRow(ctx, `insert into studio_drafts(owner,team_id,spec,source_run)
		 values($1,$2,$3,$4) on conflict(owner,source_run) where source_run is not null do nothing returning id,revision`,
			actor.ID, result.Team, result.Spec, in.Run).Scan(&result.ID, &result.Revision)
		if err == pgx.ErrNoRows {
			return tx.QueryRow(ctx, `select d.id,d.team_id,d.revision,d.spec `+draftAccess+`
			 where (d.owner,d.source_run)=($1,$2)`, actor.ID, in.Run).Scan(&result.ID, &result.Team, &result.Revision, &result.Spec)
		}
		if err != nil {
			return err
		}
		status = http.StatusCreated
		return database.Audit(ctx, tx, actor.ID, "studio.from_run", result.ID, map[string]string{"sourceRun": in.Run})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, result)
}

func reusableRunActions(ctx context.Context, tx pgx.Tx, run string) ([]string, error) {
	rows, err := tx.Query(ctx, `with steps as (
	 select a.turn,a.position,case when a.external_agent is not null then 'external'
	  when a.mcp_server is null then a.tool_name else '' end operation from general_agent_actions a
	 where a.run_id=$1 and a.state='completed' and
	  (a.external_agent is null or general_external_binding_current(a.run_id,a.external_agent))
	 union all
	 select n.ordinal,0,case when ar.card is not null then 'external'
	  when n.lookup_phase<>'' then 'read_sources'
	  when mr.operation in ('vision','image-edit','png') then 'image'
	  else coalesce(ar.tool->>'operation','') end
	 from runs r join nodes n on (n.project_id,n.plan_version)=(r.project_id,r.plan_version)
	 join invocations i on (i.run_id,i.ordinal)=(r.id,n.ordinal) and i.status='completed'
	 join agent_revisions ar on (ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision)
	 left join model_revisions mr on (mr.resource_id,mr.revision)=(ar.resource_id,ar.resource_revision)
	 where r.id=$1 and not exists(select from general_agent_runs g where g.run_id=r.id)
	)
	select operation from steps where operation<>'' order by turn,position limit 32`, run)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

type reusableAction struct {
	capability, step string
}

var reusableActions = map[string]reusableAction{
	"read_sources":  {"knowledge", "查阅本次已授权的资料并标明出处。"},
	"extract":       {"knowledge", "从本次已授权的资料提取与任务有关的部分。"},
	"web_search":    {"search", "在获准联网后查找公开信息，记录来源。"},
	"aggregate_csv": {"office", "核对表格字段，再按本次要求分组并计算。"},
	"aggregate":     {"office", "核对表格字段，再按本次要求分组并计算。"},
	"create_docx":   {"office", "按用户指定的结构生成文档并检查段落。"},
	"docx":          {"office", "按用户指定的结构生成文档并检查段落。"},
	"create_xlsx":   {"office", "把结果整理成电子表格，检查列头与格式。"},
	"xlsx":          {"office", "把结果整理成电子表格，检查列头与格式。"},
	"create_pptx":   {"office", "将已确认的结构整理成幻灯片，逐页核对。"},
	"pptx":          {"office", "将已确认的结构整理成幻灯片，逐页核对。"},
	"image":         {"images", "根据用户授权读取或生成图像，并检查视觉结果。"},
	"external":      {"external", "按需委派给本次获准使用的外部 Agent，并核对其返回。"},
}

// The brief selects a generic task category. No project names, excerpts,
// attachment identifiers, tool arguments or model replies enter the draft.
func distillSpec(brief string, tier llmgateway.Tier, operations []string) Spec {
	spec := emptySpec()
	spec.Tier = tier
	spec.Name, spec.Purpose = "工作助手", "根据每次提供的要求交付一份可复核的结果。"
	categories := []struct {
		terms         []string
		name, purpose string
	}{
		{[]string{"翻译", "translate", "translation"}, "翻译助手", "按目标语言和读者要求翻译，核对专有名词和语气。"},
		{[]string{"会议", "纪要", "meeting", "minutes"}, "会议纪要助手", "整理讨论主题、明确决议和后续行动。"},
		{[]string{"报价", "对账", "费用", "invoice", "quote", "reconcil"}, "业务核对助手", "核对明细与汇总，标出缺失项并给出追问清单。"},
		{[]string{"报告", "周报", "总结", "report", "summar"}, "报告整理助手", "梳理结论与根据，按要求汇总进展、问题与后续行动。"},
		{[]string{"改写", "润色", "rewrite", "draft"}, "文稿助手", "按读者和语气要求整理文稿，保持事实与表述一致。"},
	}
	brief = strings.ToLower(brief)
	for _, category := range categories {
		if slices.ContainsFunc(category.terms, func(term string) bool { return strings.Contains(brief, term) }) {
			spec.Name, spec.Purpose = category.name, category.purpose
			break
		}
	}
	spec.Instructions = spec.Purpose + " 先确认本次目标及交付格式。仅使用当前获准的资料和能力，说明无法验证的部分。"
	spec.Steps = append(spec.Steps, "确认目标、输出要求和本次可用资料。")
	for _, operation := range operations {
		action, known := reusableActions[operation]
		if !known || slices.Contains(spec.Steps, action.step) {
			continue
		}
		spec.Steps = append(spec.Steps, action.step)
		if !slices.Contains(spec.Capabilities, action.capability) {
			spec.Capabilities = append(spec.Capabilities, action.capability)
		}
	}
	spec.Steps = append(spec.Steps, "整理最终答复并列出需用户确认的事项。")
	spec.Checks = []string{"确认答复符合本次要求。", "核对数字、出处、格式和待确认项。"}
	return spec
}

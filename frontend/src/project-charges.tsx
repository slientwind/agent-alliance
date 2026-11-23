import type { BudgetDraft } from "./budget-request-form";
import type { DraftField } from "./project-drafts";
import { BudgetRequests } from "./budget-requests";
import type { Charge, Project } from "./client";
import { BudgetEditor } from "./budget";
export function ProjectCharges({
  project: p,
  draft,
  refresh,
}: {
  project: Project;
  draft: DraftField<BudgetDraft>;
  refresh: () => Promise<void>;
}) {
  const summary = p.costSummary;
  const groups = new Map<string, Charge[]>();
  for (const call of p.costDetails || []) {
    const name = call.scope === "column_suggestion"
      ? "汇总字段建议"
      : call.step
        ? "步骤 " + call.step + " · " + call.agent
      : call.scope === "planning"
        ? "规划助手"
        : call.scope === "validation"
          ? "项目试运行"
          : call.scope;
    const group = groups.get(name) || [];
    group.push(call);
    groups.set(name, group);
  }
  return (
    <details className="card">
      <summary>
        项目费用 · 已记账 ¥{summary.recorded}
        {draft.value && " · 有预算申请草稿"}
        {p.budget.pending > 0 && " · " + p.budget.pending + " 项追加待审批"}
        {summary.unknown > 0 && " · " + summary.unknown + " 笔未知"}
      </summary>
      <p>
        共 {summary.calls} 次调用 · 剩余预占 ¥{summary.reserved} ·
        团队承担（CNY）
      </p>
      <p>
        项目额度{" "}
        {p.budget.limit === null ? "未设金额阈值" : "¥" + p.budget.limit} ·
        提醒线 {p.budget.warningPercent}%
      </p>
      {p.budget.warning && (
        <p role="status">
          <strong>
            {p.budget.exhausted
              ? "项目已达到预算停止阈值。"
              : "项目已达到预算提醒阈值。"}
          </strong>
        </p>
      )}
      <BudgetRequests
        key={p.id}
        path={"projects/" + p.id}
        project
        draft={draft}
        active={
          !["delivered", "adopted", "cancelled", "failed"].includes(p.stage)
        }
        refresh={refresh}
      />
      {p.budget.canManage && (
        <BudgetEditor
          path={"projects/" + p.id + "/budget"}
          settings={p.budget}
          refresh={refresh}
        />
      )}
      {Array.from(groups, ([name, calls]) => (
        <details key={name}>
          <summary>
            {name} · {calls.length} 次调用
          </summary>
          {calls.map((c) => (
            <section key={c.id}>
              <strong>
                {c.sourceName || c.model || c.agent || "调用记录"}
                {c.sourceRevision !== null && " v" + c.sourceRevision}
              </strong>
              {c.attempt != null && calls.length > 1 && (
                <small>第 {c.attempt} 次调用</small>
              )}
              {c.scope === "revision" && " · 局部改稿"}
              <small>
                {new Date(c.time).toLocaleString()} · {c.payer}
              </small>
              <p>
                预计 {c.estimate === null ? "未知" : "¥" + c.estimate} · 已记账
                ¥{c.recorded} · {c.settled ? "已结清" : "待结算"}
                {c.tokens !== null && " · " + c.tokens + " token"}
              </p>
            </section>
          ))}
        </details>
      ))}
    </details>
  );
}

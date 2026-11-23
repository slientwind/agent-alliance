import { useEffect, useState } from "react";
import { BudgetRequestForm, type BudgetDraft } from "./budget-request-form";
import type { DraftField } from "./project-drafts";
import { api } from "./client";

import {
  targetName,
  quantity,
  type Receipt,
  type BudgetRequestList,
} from "./budget-request-types";
const labels = {
  pending: "待处理",
  approved: "已批准",
  rejected: "已拒绝",
  withdrawn: "已撤回",
  closed: "随项目关闭",
};

export function BudgetRequests({
  path,
  project = false,
  active = true,
  draft,
  refresh,
}: {
  path: string;
  project?: boolean;
  active?: boolean;
  draft?: DraftField<BudgetDraft>;
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [data, setData] = useState<BudgetRequestList | null>(null),
    [epoch, bump] = useState(0),
    [busy, setBusy] = useState(false),
    [message, setMessage] = useState("");
  useEffect(() => {
    if (!open) {
      setData(null);
      return;
    }
    let live = true;
    async function load() {
      try {
        const result = await api<BudgetRequestList>(path + "/budget-requests");
        if (live) setData(result);
      } catch (e) {
        if (live) {
          setData(null);
          setMessage((e as Error).message);
        }
      }
    }
    void load();
    return () => {
      live = false;
    };
  }, [path, open, epoch]);
  async function act(fn?: () => Promise<void>, success?: string) {
    if (busy) return;
    setBusy(true);
    try {
      await fn?.();
      if (success) setMessage(success);
      await refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      bump((v) => v + 1);
      setBusy(false);
    }
  }
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>{project ? "预算追加申请" : "预算审批与记录"}</summary>
      {open && (
        <>
          <button type="button" onClick={() => void act()} disabled={busy}>
            刷新申请记录
          </button>
          {data && (
            <>
              {project && draft && (active || draft.value) && (
                <BudgetRequestForm
                  path={path}
                  data={data}
                  draft={draft}
                  active={active}
                  busy={busy}
                  submit={(fn) => act(fn, "已提交预算申请。")}
                />
              )}
              {data.items.length === 0 && <p>暂无申请记录。</p>}
              {data.items.map((r) => (
                <BudgetReceipt
                  key={r.id}
                  receipt={r}
                  project={project}
                  busy={busy}
                  act={act}
                />
              ))}
            </>
          )}
          {message && <p role="status">{message}</p>}
        </>
      )}
    </details>
  );
}

export function BudgetReceipt({
  receipt: r,
  project = false,
  busy,
  act,
}: {
  receipt: Receipt;
  project?: boolean;
  busy: boolean;
  act: (fn: () => Promise<void>, success: string) => Promise<void>;
}) {
  return (
    <section>
      <strong>
        {labels[r.state]} · 追加 {quantity(r.scope, r.amount)}
      </strong>
      <small>
        {targetName(r.scope, r.resourceName || r.team)} · {r.requester} ·{" "}
        {new Date(r.created).toLocaleString()}
      </small>
      {!project && <small>项目 {r.projectTitle || r.project}</small>}
      <p>{r.reason}</p>
      <p>剩余工作与新增需求 · {r.work}</p>
      <p>替代方案 · {r.alternative}</p>
      <small>
        {r.resourceName
          ? "本项目使用该资源"
          : r.scope === "project"
            ? "项目"
            : "本项目使用该团队资源"}
        已记账与预占 ¥{r.projectUsed} · {r.projectUnknown} 笔未知费用待核对
        {r.scope === "resource_calls" &&
          " · 本项目已调用 " + r.projectCalls + " 次"}
      </small>
      {r.state === "pending" && <p>待 {r.owner || "指定预算负责人"} 处理</p>}
      {r.decision && (
        <p>
          {r.decidedBy || "系统"} · {r.decision} ·{" "}
          {new Date(r.decidedAt!).toLocaleString()}
          {r.applied !== null &&
            " · 批准后额度 " + quantity(r.scope, r.applied)}
        </p>
      )}
      {project && r.canResume && (
        <button
          type="button"
          disabled={busy}
          onClick={() =>
            void act(async () => {
              await api(
                "runs/" + r.run + "/control",
                "POST",
                {
                  action: "resume",
                  expected: r.controlRevision,
                  reason: "预算追加已批准，复查原任务条件",
                },
                { "Idempotency-Key": "budget-resume-" + r.id },
              );
            }, "已提交原任务恢复请求。")
          }
        >
          复查并继续原任务
        </button>
      )}
      {r.canDecide && (
        <p>
          申请时额度 {quantity(r.scope, r.baseline)} · 当前额度{" "}
          {r.currentLimit === null
            ? "未设或已退出承担范围"
            : quantity(r.scope, r.currentLimit)}
          {r.afterApproval !== null &&
            " · 批准后 " + quantity(r.scope, r.afterApproval)}
          {r.changed && <strong> · 额度已调整，请按最新额度复核</strong>}
        </p>
      )}
      {(r.canDecide || r.canWithdraw) && (
        <form
          key={r.id + ":" + r.revision}
          onSubmit={(e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            const action = (
              (e.nativeEvent as SubmitEvent).submitter as HTMLButtonElement
            )?.value;
            if (!action) return;
            void act(async () => {
              await api("budget-requests/" + r.id + "/decision", "POST", {
                action,
                expected: r.revision || 0,
                reason: f.get("note"),
              });
            }, "已更新申请记录。");
          }}
        >
          <label>
            处理依据
            <input name="note" required />
          </label>
          <small>处理依据将随结论提供给项目成员。</small>
          <div className="actions">
            {r.canApprove && (
              <button value="approve" disabled={busy}>
                批准追加
              </button>
            )}
            {r.canDecide && (
              <button value="reject" disabled={busy}>
                拒绝申请
              </button>
            )}
            {r.canWithdraw && (
              <button value="withdraw" disabled={busy}>
                撤回申请
              </button>
            )}
          </div>
        </form>
      )}
    </section>
  );
}

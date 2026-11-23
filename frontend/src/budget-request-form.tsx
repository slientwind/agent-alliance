import { api, RequestError } from "./client";
import {
  targetName,
  type BudgetRequestList,
  type Target,
} from "./budget-request-types";
import type { DraftField } from "./project-drafts";

export type BudgetDraft = {
  intent: string;
  attempted: boolean;
  target: string;
  targetRevision: number;
  run: string;
  runRevision: number;
  amount: string;
  reason: string;
  work: string;
  alternative: string;
};
function forgetIntent(intent: string) {
  for (const slot of Object.keys(sessionStorage)) {
    if (
      slot.startsWith("alliance.budget.") &&
      sessionStorage.getItem(slot) === intent
    )
      sessionStorage.removeItem(slot);
  }
}
function targetKey(t: Target) {
  return t.scope + (t.resource || t.team);
}

export function BudgetRequestForm({
  path,
  data,
  draft,
  active,
  busy,
  submit,
}: {
  path: string;
  data: BudgetRequestList;
  draft: DraftField<BudgetDraft>;
  active: boolean;
  busy: boolean;
  submit: (fn: () => Promise<void>) => Promise<void>;
}) {
  const first = data.targets[0];
  const value = draft.value ?? {
    intent: "",
    attempted: false,
    target: first ? targetKey(first) : "",
    targetRevision: first?.revision ?? 0,
    run: "",
    runRevision: 0,
    amount: "",
    reason: "",
    work: "",
    alternative: "",
  };
  const target = data.targets.find((t) => targetKey(t) === value.target);
  const run = data.runs.find((r) => r.id === value.run);
  const missingRun = Boolean(value.run && !run);
  const stale =
    target?.revision !== value.targetRevision ||
    (value.run !== "" && run?.revision !== value.runRevision);
  const ready = active && target && !missingRun && !stale;
  const patch = (change: Partial<BudgetDraft>) =>
    draft.change({
      ...value,
      ...change,
      intent: value.intent || crypto.randomUUID(),
    });
  if (!first && !draft.value)
    return <p>当前相关预算均未设置金额或次数上限。</p>;
  return (
    <details>
      <summary>填写追加申请</summary>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (!ready || busy || !draft.value) return;
          const submitted = draft.value;
          const body = {
            run: value.run,
            runRevision: value.runRevision,
            scope: target.scope,
            team: target.team,
            ...(target.resource ? { resource: target.resource } : {}),
            amount: value.amount,
            expected: value.targetRevision,
            reason: value.reason,
            work: value.work,
            alternative: value.alternative,
          };
          void submit(async () => {
            // Keep the same intent across transport failures without storing the business text.
            const digest = await crypto.subtle.digest(
              "SHA-256",
              new TextEncoder().encode(JSON.stringify([path, body])),
            );
            const slot =
              "alliance.budget." +
              Array.from(new Uint8Array(digest), (n) =>
                n.toString(16).padStart(2, "0"),
              ).join("");
            const key = submitted.attempted
              ? submitted.intent
              : sessionStorage.getItem(slot) || submitted.intent;
            // An edited or rebased draft is still one application until explicitly discarded.
            const pending = submitted.attempted
              ? submitted
              : { ...submitted, intent: key, attempted: true };
            if (pending !== submitted) draft.change(pending, submitted);
            sessionStorage.setItem(slot, key);
            try {
              await api(path + "/budget-requests", "POST", body, {
                "Idempotency-Key": key,
              });
            } catch (error) {
              if (!(error instanceof RequestError) || error.status >= 500)
                throw new Error(
                  "尚未收到明确回执，请刷新申请记录；重试将沿用本次申请标识。",
                );
              if (
                error.status === 409 &&
                error.message === "请求标识已用于其他预算申请"
              )
                throw new Error(
                  "这份草稿已有其他版本提交成功，请核对申请记录。如需另行申请，请丢弃草稿后重新填写。",
                );
              throw error;
            }
            forgetIntent(key);
            draft.change(undefined, pending);
          });
        }}
      >
        {!active && <p role="status">项目已结束。可以查看或丢弃本页草稿。</p>}
        <label>
          追加到
          <select
            name="target"
            disabled={!active || busy}
            value={value.target}
            onChange={(e) => {
              const next = data.targets.find(
                (t) => targetKey(t) === e.target.value,
              );
              if (next)
                patch({
                  target: targetKey(next),
                  targetRevision: next.revision,
                  amount: "",
                });
            }}
          >
            {!target && (
              <option value={value.target} disabled>
                原预算范围已不可用
              </option>
            )}
            {data.targets.map((t) => (
              <option key={targetKey(t)} value={targetKey(t)}>
                {targetName(t.scope, t.name)} · {t.owner || "待指定预算负责人"}
              </option>
            ))}
          </select>
        </label>
        {(data.runs.length > 0 || value.run) && (
          <label>
            关联预算阻塞任务
            <select
              name="run"
              value={value.run}
              disabled={!active || busy}
              onChange={(e) => {
                const next = data.runs.find((r) => r.id === e.target.value);
                patch({
                  run: next?.id ?? "",
                  runRevision: next?.revision ?? 0,
                });
              }}
            >
              <option value="">仅申请额度</option>
              {missingRun && (
                <option value={value.run} disabled>
                  原任务已离开预算阻塞状态
                </option>
              )}
              {data.runs.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.change ? "改稿任务" : "项目运行"} ·{" "}
                  {new Date(r.created).toLocaleString()}
                </option>
              ))}
            </select>
          </label>
        )}
        <label>
          {value.target.startsWith("resource_calls")
            ? "追加调用次数"
            : "追加金额（CNY）"}
          <input
            name="amount"
            inputMode={
              value.target.startsWith("resource_calls") ? "numeric" : "decimal"
            }
            required
            readOnly={!active || busy}
            value={value.amount}
            onChange={(e) => patch({ amount: e.target.value })}
          />
        </label>
        <label>
          额度不足原因
          <input
            name="reason"
            required
            readOnly={!active || busy}
            value={value.reason}
            onChange={(e) => patch({ reason: e.target.value })}
          />
        </label>
        <label>
          剩余工作与新增需求
          <textarea
            name="work"
            required
            readOnly={!active || busy}
            value={value.work}
            onChange={(e) => patch({ work: e.target.value })}
          />
        </label>
        <label>
          可选替代方案
          <input
            name="alternative"
            required
            readOnly={!active || busy}
            value={value.alternative}
            onChange={(e) => patch({ alternative: e.target.value })}
          />
        </label>
        <small>以上说明将提供给项目成员及承担团队的预算负责人。</small>
        {active && stale && (
          <p role="status">
            {!target || missingRun ? (
              "原预算范围或关联任务已不可用，请重新选择。"
            ) : (
              <>
                预算或任务已更新。预算修订 {value.targetRevision} →{" "}
                {target.revision}
                {run &&
                  "，任务修订 " + value.runRevision + " → " + run.revision}
                。
                <button
                  type="button"
                  disabled={busy}
                  onClick={() =>
                    patch({
                      targetRevision: target.revision,
                      runRevision: run?.revision ?? 0,
                    })
                  }
                >
                  已核对当前预算与任务
                </button>
              </>
            )}
          </p>
        )}
        <div className="actions">
          {active && <button disabled={busy || !ready}>提交预算申请</button>}
          {draft.value && (
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                if (draft.value) forgetIntent(draft.value.intent);
                draft.change(undefined, draft.value);
              }}
            >
              丢弃申请草稿
            </button>
          )}
        </div>
      </form>
    </details>
  );
}

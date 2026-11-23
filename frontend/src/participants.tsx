import type { ReactNode } from "react";
import { HandoffQueueActions } from "./reviewer-handoffs";
import type { Member, Project } from "./client";
import { api } from "./client";
export function Participants({
  children,
  project: p,
  user,
  busy,
  perform,
}: {
  children: ReactNode;
  project: Project;
  user: Member;
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
}) {
  const endpoint = "projects/" + p.id + "/participants";
  const locked = ["delivered", "adopted", "cancelled", "failed"].includes(
    p.stage,
  );

  return (
    <details
      className="card"
      open={
        p.handoffs.some(
          (h) => h.state === "pending" && h.successor === user.id,
        ) || undefined
      }
    >
      <summary>项目协作 · {p.participants.length} 人</summary>
      {children}
      <ul>
        {p.participants.map((m) => (
          <li key={m.id}>
            {m.name} ·{" "}
            {m.id === p.reviewer
              ? "最终确认人"
              : m.canReview
                ? "可审核"
                : "参与者"}
            {m.id === p.owner && " · 发起人"}
            {m.canViewCosts && " · 可查费用明细"}
            {!m.enabled && " · 已停用"}
          </li>
        ))}
      </ul>
      {p.owner === user.id &&
        p.participants.some((m) => m.id !== p.owner && m.enabled) && (
          <details>
            <summary>费用明细授权</summary>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const data = new FormData(e.currentTarget);
                void perform(() =>
                  api("projects/" + p.id + "/cost-access", "PATCH", {
                    member: data.get("member"),
                    allowed: data.get("mode") === "grant",
                    expected: p.revision,
                    reason: data.get("reason"),
                  }),
                );
              }}
            >
              <label>
                项目成员
                <select name="member">
                  {p.participants
                    .filter((m) => m.id !== p.owner && m.enabled)
                    .map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name}
                      </option>
                    ))}
                </select>
              </label>
              <label>
                费用权限
                <select name="mode">
                  <option value="grant">开放任务和调用明细</option>
                  <option value="revoke">仅查看项目汇总</option>
                </select>
              </label>
              <label>
                变更原因
                <input required name="reason" />
              </label>
              <button disabled={busy}>确认费用授权</button>
            </form>
          </details>
        )}
      {!locked && p.owner === user.id && (
        <details>
          <summary>邀请成员或调整审核权限</summary>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const form = e.currentTarget,
                data = new FormData(form);
              void perform(async () => {
                await api(endpoint, "POST", {
                  expected: p.revision,
                  email: data.get("email"),
                  review: data.get("review") === "on",
                  reason: data.get("reason"),
                });
                form.reset();
              });
            }}
          >
            <label>
              企业成员邮箱
              <input type="email" name="email" required />
            </label>
            <label>
              <input type="checkbox" name="review" />
              允许审核项目成果
            </label>
            <label>
              授权说明
              <input name="reason" required />
            </label>
            <button disabled={busy}>保存项目授权</button>
          </form>
        </details>
      )}
      <HandoffQueueActions
        p={p}
        user={user}
        busy={busy}
        perform={perform}
        locked={locked}
      />
    </details>
  );
}

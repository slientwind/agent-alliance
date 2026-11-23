import { useState } from "react";
import { api, type Project, type Member } from "./client";

const labels: Record<string, string> = {
  pending: "待接任人接受",
  accepted: "已接受",
  declined: "已谢绝",
  withdrawn: "已撤回",
  superseded: "已失效",
  closed: "项目已结束",
};

export function ResponsibilityQueue({ open }: { open: (id: string) => void }) {
  const [items, setItems] = useState<
      {
        project: string;
        title: string;
        offered: boolean;
        unavailable: boolean;
      }[]
    >([]),
    [message, setMessage] = useState("");
  async function refresh() {
    try {
      const rows = await api<typeof items>("projects/reviewer-handoffs");
      setItems(rows);
      setMessage(rows.length ? "" : "当前没有待处理的确认权交接。");
    } catch (e) {
      setItems([]);
      setMessage((e as Error).message);
    }
  }
  return (
    <details
      onToggle={(e) => {
        if (e.currentTarget.open) void refresh();
      }}
    >
      <summary>确认权交接待办</summary>
      {items.map((x) => (
        <button
          className="list"
          key={x.project}
          onClick={() => open(x.project)}
        >
          <strong>{x.offered ? "待我接受" : "原确认人当前无法履职"}</strong> ·{" "}
          {x.title}
        </button>
      ))}
      {message && <p role="status">{message}</p>}
      <button onClick={() => void refresh()}>刷新交接待办</button>
    </details>
  );
}

export function HandoffQueueActions({
  p,
  user,
  perform,
  busy,
  locked,
}: {
  p: Project;
  user: Member;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
  busy: boolean;
  locked: boolean;
}) {
  const pending = p.handoffs.find(
      (h) => h.state === "pending" && !h.recoveryUntil,
    ),
    candidates = p.participants.filter((m) => m.enabled && m.id !== p.reviewer);
  const emergency = p.canTakeover,
    base = "projects/" + p.id;
  const reviewerAvailable = p.participants.some(
    (m) => m.id === p.reviewer && m.enabled && m.canReview,
  );
  const propose =
    !locked &&
    (emergency ||
      (reviewerAvailable && (p.owner === user.id || p.reviewer === user.id)));
  const actionable = p.artifacts.filter(
    (a) => !a.approved || a.needsTakeoverReview,
  ).length;
  const feedback = p.artifacts
    .flatMap((a) => a.feedback)
    .filter((f) =>
      ["suggested", "required", "investigate", "verification"].includes(
        f.state,
      ),
    ).length;
  return (
    <>
      {pending && (
        <section className="card">
          <strong>
            {pending.emergency ? "异常接管提名" : "最终确认权转交"} · 待接受
          </strong>
          <p>
            {pending.previous} → {pending.next}。{pending.reason}
          </p>
          {pending.emergency && (
            <p>接管结束条件 · {pending.end}。接任人须逐项审核当前版本。</p>
          )}
          <p>
            当前有 {p.artifacts.length} 份成果，{actionable} 份待审核或核对，
            {feedback} 项反馈待处理。职责生效前仍由原确认人负责。
          </p>
          {pending.successor === user.id && (
            <button
              className="primary"
              disabled={busy}
              onClick={() =>
                void perform(() =>
                  api(base + "/handoffs/" + pending.id, "POST", {
                    expected: p.revision,
                    action: "accept",
                  }),
                )
              }
            >
              接受最终确认职责
            </button>
          )}
          {(pending.successor === user.id ||
            pending.author === user.id ||
            p.owner === user.id ||
            p.reviewer === user.id) && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const reason = new FormData(e.currentTarget).get("reason");
                void perform(() =>
                  api(base + "/handoffs/" + pending.id, "POST", {
                    expected: p.revision,
                    reason,
                    action:
                      pending.successor === user.id ? "decline" : "withdraw",
                  }),
                );
              }}
            >
              <label>
                交接处理说明
                <input required name="reason" maxLength={2000} />
              </label>
              <button disabled={busy}>
                {pending.successor === user.id ? "谢绝提名" : "撤回提名"}
              </button>
            </form>
          )}
        </section>
      )}
      {propose && candidates.length > 0 && (
        <details>
          <summary>
            {emergency ? "异常接管最终确认职责" : "转交最终确认权"}
          </summary>
          {emergency && (
            <p>
              当前确认人已无法履职。提名范围限本项目，接管后需重新核对每份成果。
            </p>
          )}
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const data = new FormData(e.currentTarget);
              void perform(() =>
                api(
                  base + (emergency ? "/takeover" : "/participants"),
                  "POST",
                  {
                    expected: p.revision,
                    member: data.get("member"),
                    reason: data.get("reason"),
                    ...(emergency
                      ? { endCondition: data.get("end") }
                      : { final: true }),
                  },
                ),
              );
            }}
          >
            <label>
              接替人
              <select name="member" aria-label="接替人">
                {candidates.map((m) => (
                  <option value={m.id} key={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {emergency ? "异常原因与影响" : "转交原因"}
              <input required name="reason" maxLength={2000} />
            </label>
            {emergency && (
              <label>
                接管结束条件
                <input required name="end" maxLength={500} />
              </label>
            )}
            {pending && <small>重新提名会使当前待接受提名失效。</small>}
            <button disabled={busy}>提交接任提名</button>
          </form>
        </details>
      )}
      {p.handoffs.length > 0 && (
        <details>
          <summary>确认职责变更记录 · {p.handoffs.length}</summary>
          <ul>
            {p.handoffs.map((h) => (
              <li key={h.id}>
                {h.previous} → {h.next} · {labels[h.state]}
                <small>
                  {new Date(h.created).toLocaleString()} · {h.by}提名 ·{" "}
                  {h.reason}
                  {h.emergency && " · 接管结束条件：" + h.end}
                  {h.decidedAt &&
                    " · " +
                      new Date(h.decidedAt).toLocaleString() +
                      " " +
                      (h.decidedBy || "系统") +
                      "处理"}
                  {h.recoveryUntil &&
                    " · 临时访问截至 " +
                      new Date(h.recoveryUntil).toLocaleString()}
                  {h.accessEnd && " · " + h.accessEnd}
                  {h.decisionReason && " · " + h.decisionReason}
                </small>
              </li>
            ))}
          </ul>
        </details>
      )}
    </>
  );
}

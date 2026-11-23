import { useEffect, useState } from "react";
import { api } from "./client";
import type { ReviewTask } from "./conversation-history";

type Clock = {
  project: string;
  title: string;
  kind: ReviewTask["kind"];
  subject: string;
  actionRevision: number;
  step: number;
  canHandle: boolean;
  name: string;
  version: number;
  clock: string;
  revision: number;
  phase: "initial" | "due" | "escalated" | "unassigned";
  opened: string;
  originalDue: string;
  due: string;
  overdue: boolean;
  owner: boolean;
  assignees: string[];
};
const displayTime = (s: string) =>
  new Date(s).toLocaleString("zh-CN", { hour12: false });
const phaseLabel = {
  initial: "等待处理",
  due: "临近截止",
  escalated: "逾期需协调",
  unassigned: "无人负责",
};
export function ReviewReminders({
  openProject,
  query,
  labels,
}: {
  openProject: (id: string, task?: ReviewTask) => void;
  query: string;
  labels: Record<string, string>;
}) {
  const [items, setItems] = useState<Clock[]>([]),
    [error, setError] = useState(""),
    [refresh, setRefresh] = useState(0);
  useEffect(() => {
    let ticket = 0;
    async function load() {
      if (document.hidden) return;
      const n = ++ticket;
      try {
        const rows = await api<Clock[]>("projects/review-reminders");
        if (n === ticket) {
          setItems(rows);
          setError("");
        }
      } catch (e) {
        if (n === ticket) {
          setItems([]);
          setError((e as Error).message);
        }
      }
    }
    void load();
    const timer = window.setInterval(load, 30000);
    window.addEventListener("focus", load);
    document.addEventListener("visibilitychange", load);
    return () => {
      ++ticket;
      clearInterval(timer);
      window.removeEventListener("focus", load);
      document.removeEventListener("visibilitychange", load);
    };
  }, [refresh]);
  const attention = items.filter(
    (t) => t.phase === "escalated" || t.phase === "unassigned",
  ).length;
  return (
    <details className="review-reminders">
      <summary>
        提醒与协调{attention > 0 && <strong> · {attention} 项需协调</strong>}
      </summary>
      {error && <p role="alert">{error}</p>}
      <button onClick={() => setRefresh((n) => n + 1)}>刷新提醒</button>
      {!error && items.length === 0 && <p>当前没有审核提醒。</p>}
      {items
        .filter((t) =>
          (
            t.title +
            " " +
            t.name +
            " " +
            labels[t.kind] +
            " " +
            phaseLabel[t.phase]
          ).includes(query.trim()),
        )
        .map((t) => (
          <section key={t.clock}>
            <h3>
              {labels[t.kind]}
              {t.name && " · " + t.name}
              {t.version > 0 && " · v" + t.version}
            </h3>
            <p>
              {t.title} ·{" "}
              <strong>
                {t.phase === "due" && t.overdue
                  ? "已到截止时间"
                  : phaseLabel[t.phase]}
              </strong>
            </p>
            <small>
              处理人 · {t.assignees.join("、") || "待分配"}
              <br />
              已等待{" "}
              {Math.max(
                0,
                Math.floor((Date.now() - Date.parse(t.opened)) / 60000),
              )}{" "}
              分钟
              <br />
              截止 {displayTime(t.due)}
            </small>
            {t.originalDue !== t.due && (
              <small>原期限 {displayTime(t.originalDue)}</small>
            )}
            <button
              onClick={() =>
                openProject(
                  t.project,
                  t.canHandle
                    ? {
                        project: t.project,
                        title: t.title,
                        kind: t.kind,
                        name: t.name,
                        subject: t.subject,
                        step: t.step,
                        version: t.version,
                        revision: t.actionRevision,
                      }
                    : undefined,
                )
              }
            >
              {t.canHandle ? "前往处理" : "打开项目协调"}
            </button>
            {t.owner && (
              <ExtendClock clock={t} reload={() => setRefresh((n) => n + 1)} />
            )}
          </section>
        ))}
    </details>
  );
}
function ExtendClock({ clock, reload }: { clock: Clock; reload: () => void }) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [editingRevision, setEditingRevision] = useState<number | null>(null);
  return (
    <details
      onToggle={(e) => {
        setEditingRevision(e.currentTarget.open ? clock.revision : null);
        setError("");
      }}
    >
      <summary>调整本项期限</summary>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (editingRevision !== clock.revision) return;
          const form = e.currentTarget;
          const data = new FormData(form);
          setBusy(true);
          try {
            await api(
              "projects/" +
                clock.project +
                "/review-reminders/" +
                clock.clock +
                "/extend",
              "POST",
              {
                expected: editingRevision,
                due: new Date(String(data.get("due"))).toISOString(),
                reason: data.get("reason"),
              },
            );
            setError("");
            form.reset();
            const details = form.closest("details");
            if (details) details.open = false;
            reload();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <label>
          新截止时间（本地时间）
          <input name="due" type="datetime-local" required />
        </label>
        <label>
          延期原因
          <input name="reason" required />
        </label>
        {editingRevision !== null && editingRevision !== clock.revision && (
          <p role="status">
            期限已更新。核对上方当前截止时间后，可继续编辑。
            <button
              type="button"
              onClick={() => setEditingRevision(clock.revision)}
            >
              已核对当前期限
            </button>
          </p>
        )}
        <button disabled={busy || editingRevision !== clock.revision}>
          确认延期
        </button>
        {error && <p role="alert">{error}</p>}
      </form>
    </details>
  );
}

type Policy = {
  hours: number;
  lead: number;
  escalation: number;
  revision: number;
  source: string;
};
export function ReviewPolicy({ scope }: { scope: string }) {
  const [open, setOpen] = useState(false),
    [policy, setPolicy] = useState<Policy | null>(null),
    [reload, setReload] = useState(0),
    [note, setNote] = useState(""),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    if (!open) return;
    let live = true;
    api<Policy>(scope + "/review-policy")
      .then((p) => {
        if (live) setPolicy(p);
      })
      .catch((e) => {
        if (live) setNote(e.message);
      });
    return () => {
      live = false;
    };
  }, [open, scope, reload]);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>审核提醒节奏</summary>
      {policy && (
        <form
          key={scope + policy.revision}
          onSubmit={async (e) => {
            e.preventDefault();
            const data = new FormData(e.currentTarget);
            setBusy(true);
            try {
              await api(scope + "/review-policy", "PUT", {
                hours: Number(data.get("hours")),
                lead: Number(data.get("lead")),
                escalation: Number(data.get("escalation")),
                expected: policy.revision,
                reason: data.get("reason"),
              });
              setNote("已保存，对新产生的待办生效。");
              setReload((n) => n + 1);
            } catch (e) {
              setNote((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <small>
            当前采用
            {policy.source === "project"
              ? "项目"
              : policy.source === "team"
                ? "团队"
                : "平台默认"}
            设置，按连续小时计算。
          </small>
          <label>
            处理时限（小时）
            <input
              type="number"
              min="1"
              max="8760"
              name="hours"
              defaultValue={policy.hours}
              required
            />
          </label>
          <label>
            截止前提醒（小时）
            <input
              type="number"
              min="0"
              max="8759"
              name="lead"
              defaultValue={policy.lead}
              required
            />
          </label>
          <label>
            截止后升级（小时）
            <input
              type="number"
              min="0"
              max="8760"
              name="escalation"
              defaultValue={policy.escalation}
              required
            />
          </label>
          <label>
            调整原因
            <input name="reason" required />
          </label>
          <button disabled={busy}>保存审核节奏</button>
        </form>
      )}
      {note && <p role="status">{note}</p>}
      <button
        onClick={() => {
          setPolicy(null);
          setNote("");
          setReload((n) => n + 1);
        }}
      >
        重新读取设置
      </button>
    </details>
  );
}

type Preference = {
  binding: string;
  enabled: boolean;
  latest: string | null;
  retry: string | null;
};
export function FeishuReminders() {
  const [data, setData] = useState<Preference | null>(null),
    [err, setErr] = useState(""),
    [busy, setBusy] = useState(false),
    [revision, setRevision] = useState(0);
  const url = "channels/feishu/review-reminders";
  useEffect(() => {
    let active = true;
    api<Preference>(url)
      .then((d) => {
        if (active) {
          setData(d);
          setErr("");
        }
      })
      .catch((e) => {
        if (active) {
          setData(null);
          setErr(e.message);
        }
      });
    return () => {
      active = false;
    };
  }, [revision]);
  async function save(retry: boolean) {
    if (!data) return;
    setBusy(true);
    try {
      await api(
        retry ? url + "/retry" : url,
        retry ? "POST" : "PUT",
        retry
          ? { batch: data.retry }
          : { binding: data.binding, enabled: !data.enabled },
      );
      setRevision((n) => n + 1);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section>
      {data && (
        <>
          <button
            aria-pressed={data.enabled}
            disabled={busy}
            onClick={() => void save(false)}
          >
            {data.enabled ? "关闭审核单聊提醒" : "开启审核单聊提醒"}
          </button>
          <small>
            提醒仅包含工作区入口。当前待办和处理权限以登录后的页面为准。
          </small>
          {data.latest && (
            <p>
              最近一次提醒 ·{" "}
              {
                (
                  {
                    pending: "等待渠道确认",
                    failed: "渠道未确认接收",
                    accepted: "飞书已受理",
                    obsolete: "原提醒已失效",
                  } as Record<string, string>
                )[data.latest]
              }
            </p>
          )}
          {data.retry && (
            <button disabled={busy} onClick={() => void save(true)}>
              重新提醒仍有效的事项
            </button>
          )}
        </>
      )}
      {err && <p role="alert">{err}</p>}
      <button onClick={() => setRevision((n) => n + 1)}>刷新通知状态</button>
    </section>
  );
}

export function ReviewChannelHealth() {
  const [counts, setCounts] = useState<{
      pending: number;
      accepted: number;
      failed: number;
      obsolete: number;
    } | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    let sequence = 0;
    async function load() {
      if (document.hidden) return;
      const n = ++sequence;
      try {
        const result = await api<NonNullable<typeof counts>>(
          "admin/channels/feishu/review-health",
        );
        if (n === sequence) {
          setCounts(result);
          setError("");
        }
      } catch (e) {
        if (n === sequence) {
          setCounts(null);
          setError((e as Error).message);
        }
      }
    }
    void load();
    const timer = window.setInterval(load, 30000);
    window.addEventListener("focus", load);
    return () => {
      ++sequence;
      clearInterval(timer);
      window.removeEventListener("focus", load);
    };
  }, []);
  return (
    <details>
      <summary>
        飞书审核提醒状态
        {counts && counts.failed > 0 && (
          <strong> · {counts.failed} 项待排查</strong>
        )}
      </summary>
      {counts && (
        <p>
          等待渠道确认 {counts.pending} · 飞书已受理 {counts.accepted} ·{" "}
          {counts.failed > 0 ? (
            <strong>渠道未确认 {counts.failed}</strong>
          ) : (
            "渠道未确认 0"
          )}{" "}
          · 已失效 {counts.obsolete}
        </p>
      )}
      {counts && counts.failed > 0 && (
        <small>
          请检查企业机器人的授权和服务状态。绑定成员可在「飞书账号绑定」重新提醒仍有效的事项。
        </small>
      )}
      {error && <p role="alert">{error}</p>}
    </details>
  );
}

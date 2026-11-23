import { useEffect, useState } from "react";
import { api, statusLabel } from "./client";

type Reference = {
  project: string;
  title: string;
  plan: number;
  revision: number;
  stage: string;
};
type Event = {
  seq: number;
  time: string;
  action: string;
  actor: string | null;
  target: string | null;
  reason: string | null;
};
export type RuleTask = {
  team: string;
  name: string;
  id: string;
  action: string;
  body: string;
};
export const ruleTaskNames: Record<string, string> = {
  accept: "待接受维护职责",
  assign: "维护人缺位",
  expired: "已到期，需更新或停用",
  review: "到期复查",
};
const events: Record<string, string> = {
  nominate: "提名接任人",
  accept: "接受维护职责",
  cancel: "取消交接",
  membership: "团队成员资格变更",
  authority: "交接授权变更",
};

export function RuleRecords({
  path,
  version,
}: {
  path: string;
  version: number;
}) {
  const [open, setOpen] = useState(false),
    [snapshot, setSnapshot] = useState<{
      refs: Reference[];
      events: Event[];
    } | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let live = true,
      serial = 0;
    async function load() {
      const request = ++serial;
      try {
        const [refs, events] = await Promise.all([
          api<Reference[]>(path + "/references"),
          api<Event[]>(path + "/handoff-history"),
        ]);
        if (live && serial === request) {
          setSnapshot({ refs, events });
          setError("");
        }
      } catch (e) {
        if (live && serial === request) {
          setSnapshot(null);
          setError((e as Error).message);
        }
      }
    }
    void load();
    const timer = setInterval(() => {
      if (!document.hidden) void load();
    }, 30000);
    window.addEventListener("focus", load);
    return () => {
      clearInterval(timer);
      live = false;
      window.removeEventListener("focus", load);
    };
  }, [open, path, version]);
  return (
    <details
      onToggle={(e) => {
        setOpen(e.currentTarget.open);
        if (!e.currentTarget.open) {
          setSnapshot(null);
          setError("");
        }
      }}
    >
      <summary>项目引用与交接记录</summary>
      {snapshot && (
        <>
          <h3>我可查看的引用项目</h3>
          {snapshot.refs.length ? (
            <ul>
              {snapshot.refs.map((ref) => (
                <li key={ref.project}>
                  <a href={"/#" + ref.project} target="_blank" rel="noreferrer">
                    {ref.title}
                  </a>{" "}
                  · 方案 v{ref.plan} 引用规则 v{ref.revision} ·{" "}
                  {statusLabel[ref.stage] || ref.stage}
                </li>
              ))}
            </ul>
          ) : (
            <p>当前没有可查看的引用项目。</p>
          )}
          <h3>维护职责变更</h3>
          {snapshot.events.length ? (
            <ol>
              {snapshot.events.map((e) => (
                <li key={e.seq}>
                  {events[e.action.split(".").pop()!] || e.action} ·{" "}
                  {e.actor || "系统"}
                  {e.target && " · " + e.target} ·{" "}
                  {new Date(e.time).toLocaleString()}
                  {e.reason && <p>{e.reason}</p>}
                </li>
              ))}
            </ol>
          ) : (
            <p>尚无交接记录。</p>
          )}
        </>
      )}
      {error && <p role="alert">{error}</p>}
    </details>
  );
}

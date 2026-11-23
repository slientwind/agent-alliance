import { useEffect, useRef, useState } from "react";
import { api, RequestError } from "./client";

type Entry = {
  id: string;
  at: string;
  action: string;
  actor?: string;
  reason?: string;
  oldRole?: string;
  role?: string;
  oldEnabled?: boolean;
  enabled?: boolean;
  employment?: string;
  joined?: boolean;
  team?: string;
  previousDepartment?: string;
  department?: string;
};
type History = { member: string; rows: Entry[]; next: string };
const roleText = (role?: string) =>
  role === "admin" ? "管理员" : role === "member" ? "普通用户" : "未记录角色";
function describe(row: Entry) {
  if (row.action === "member.enroll") return "创建账号 · " + roleText(row.role);
  if (row.action === "team.membership")
    return (
      (row.joined ? "加入团队 · " : "移出团队 · ") + (row.team ?? "未记录")
    );
  if (row.action === "department.assign")
    return (
      "部门 · " +
      (row.previousDepartment ?? "未记录").replace(/^$/, "未分配") +
      " → " +
      (row.department ?? "未记录").replace(/^$/, "未分配")
    );
  const previous =
    row.employment === "return"
      ? "已离职"
      : row.oldEnabled === undefined
        ? "未记录"
        : row.oldEnabled
          ? "启用"
          : "暂时停用";
  const current =
    row.employment === "depart"
      ? "已离职"
      : row.enabled === undefined
        ? "未记录"
        : row.enabled
          ? "启用"
          : "暂时停用";
  return (
    roleText(row.oldRole) +
    "（" +
    previous +
    "） → " +
    roleText(row.role) +
    "（" +
    current +
    "）"
  );
}

export function MemberHistory({ id }: { id: string }) {
  const [open, setOpen] = useState(false);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>账号变更记录</summary>
      {open && <HistoryPage key={id} id={id} />}
    </details>
  );
}

function HistoryPage({ id }: { id: string }) {
  const [pages, setPages] = useState<string[]>([]);
  const [revision, setRevision] = useState(0);
  const before = pages.at(-1) ?? "";
  const [result, setResult] = useState<{
    before: string;
    data?: History;
    error?: string;
  }>();
  useEffect(() => {
    let active = true,
      generation = 0;
    async function load() {
      const request = ++generation;
      if (document.hidden) {
        setResult(undefined);
        return;
      }
      try {
        const data = await api<History>(
          "admin/members/" +
            id +
            "/history" +
            (before ? "?before=" + encodeURIComponent(before) : ""),
        );
        if (data.member !== id) throw new Error("账号记录不匹配，请刷新");
        if (active && generation === request) setResult({ before, data });
      } catch (e) {
        if (active && generation === request)
          setResult({ before, error: (e as Error).message });
      }
    }
    void load();
    const timer = window.setInterval(load, 30000);
    window.addEventListener("focus", load);
    document.addEventListener("visibilitychange", load);
    return () => {
      active = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", load);
      document.removeEventListener("visibilitychange", load);
    };
  }, [before, id, revision]);
  const shown = result?.before === before ? result : undefined;
  return (
    <section aria-label="账号记录列表">
      <button
        type="button"
        onClick={() => {
          setResult(undefined);
          setPages([]);
          setRevision((n) => n + 1);
        }}
      >
        刷新账号记录
      </button>
      {shown?.error && <p role="alert">{shown.error}</p>}
      {!shown && <p role="status">正在读取账号记录…</p>}
      {shown?.data && (
        <>
          {shown.data.rows.length === 0 && <p>暂无记录</p>}
          <ol>
            {shown.data.rows.map((row) => (
              <li key={row.id}>
                <span>{describe(row)}</span>
                <small>
                  {new Date(row.at).toLocaleString("zh-CN", { hour12: false })}{" "}
                  · 操作人 {row.actor ?? "未记录"}
                </small>
                {row.reason && <p className="preserve">{row.reason}</p>}
              </li>
            ))}
          </ol>
          <div className="actions">
            {before && (
              <button
                type="button"
                onClick={() => setPages((p) => p.slice(0, -1))}
              >
                较新记录
              </button>
            )}
            {shown.data.next && (
              <button
                type="button"
                onClick={() => setPages((p) => [...p, shown.data!.next])}
              >
                较早记录
              </button>
            )}
          </div>
        </>
      )}
    </section>
  );
}

type Notices = { member: string; role: string; count: number; rows: Entry[] };

export function AccountNotices({
  member,
  role,
  refreshIdentity,
}: {
  member: string;
  role: string;
  refreshIdentity: () => Promise<void>;
}) {
  const [notices, setNotices] = useState<Notices>();
  const [error, setError] = useState("");
  const [revision, setRevision] = useState(0);
  const [reading, setReading] = useState("");
  const generation = useRef(0);
  useEffect(() => {
    let active = true;
    async function load() {
      const request = ++generation.current;
      if (document.hidden) {
        setNotices(undefined);
        return;
      }
      try {
        const data = await api<Notices>("me/account-notices");
        if (!active || request !== generation.current) return;
        if (data.member && data.member !== member) {
          setNotices(undefined);
          await refreshIdentity();
          return;
        }
        setNotices(data);
        setError("");
        if (data.role && data.role !== role) await refreshIdentity();
      } catch (e) {
        if (!active || request !== generation.current) return;
        setNotices(undefined);
        setError((e as Error).message);
        if (e instanceof RequestError && e.status === 401)
          await refreshIdentity();
      }
    }
    void load();
    const timer = window.setInterval(load, 30000);
    window.addEventListener("focus", load);
    document.addEventListener("visibilitychange", load);
    return () => {
      active = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", load);
      document.removeEventListener("visibilitychange", load);
    };
  }, [member, role, refreshIdentity, revision]);
  return (
    <details>
      <summary>
        账号通知
        {notices?.count ? <strong> · {notices.count} 条未读</strong> : ""}
      </summary>
      {error && <p role="alert">{error}</p>}
      {!notices && !error && <p role="status">正在读取通知…</p>}
      {notices?.count === 0 && <p>暂无未读账号变更</p>}
      {notices?.rows?.map((row) => (
        <article key={row.id}>
          <p>{describe(row)}</p>
          <small>
            {new Date(row.at).toLocaleString("zh-CN")} · 操作人{" "}
            {row.actor ?? "未记录"}
          </small>
          {row.reason && <p className="preserve">{row.reason}</p>}
          <button
            disabled={!!reading}
            onClick={async () => {
              setReading(row.id);
              try {
                await api("me/account-notices/" + row.id + "/read", "PUT");
                ++generation.current;
                setRevision((n) => n + 1);
              } catch (e) {
                setError((e as Error).message);
              } finally {
                setReading("");
              }
            }}
          >
            标记已读
          </button>
        </article>
      ))}
      {error && (
        <button onClick={() => setRevision((n) => n + 1)}>重新读取通知</button>
      )}
    </details>
  );
}

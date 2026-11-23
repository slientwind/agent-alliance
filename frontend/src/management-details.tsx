import { useEffect, useState } from "react";
import { api, statusLabel } from "./client";

type Period = { from: string; to: string };
type Actor = { id: string; name: string; kind: string; runs: number };
type Task = {
  id: string;
  actor: string;
  kind: string;
  reviewer: string;
  createdAt: string;
  finishedAt: string | null;
  elapsedSeconds: number;
  status: string;
  block: string | null;
  costs?: {
    entries: number;
    recordedCny: string;
    remainingEstimateCny: string;
    unknown: number;
    pending: number;
  };
};
type Page<T> = { rows: T[]; next?: string };
const blockLabel: Record<string, string> = {
  budget: "预算待核验",
  permission: "授权或职责待核验",
  validation: "能力待复验",
  input: "输入版本待核验",
  receipt: "调用回执待核对",
  paused: "已暂停",
  other: "原因待核对",
};

export function DetailLevel({ value = "summary" }: { value?: string }) {
  return (
    <label>
      成员与任务明细权限
      <select name="detailLevel" defaultValue={value}>
        <option value="summary">仅查看汇总</option>
        <option value="tasks">查看成员与任务明细</option>
        <option value="costs">查看成员与任务明细及逐任务费用</option>
      </select>
    </label>
  );
}

export function ManagementDetails({
  id,
  period,
  costs,
}: {
  id: string;
  period: Period;
  costs: boolean;
}) {
  const [periodAtOpen, setPeriodAtOpen] = useState<Period>();
  return (
    <details
      onToggle={(e) =>
        setPeriodAtOpen(
          e.currentTarget.open
            ? { from: period.from, to: period.to }
            : undefined,
        )
      }
    >
      <summary>成员与任务明细</summary>
      {periodAtOpen && (
        <DetailBrowser id={id} period={periodAtOpen} costs={costs} />
      )}
    </details>
  );
}

function DetailBrowser({
  id,
  period,
  costs,
}: {
  id: string;
  period: Period;
  costs: boolean;
}) {
  const [actor, setActor] = useState<Actor>();
  const [mode, setMode] = useState<"members" | "tasks">("members");
  const [cursors, setCursors] = useState<string[]>([]);
  const [search, setSearch] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [result, setResult] = useState<{
    url: string;
    actors?: Page<Actor>;
    tasks?: Page<Task>;
    error?: string;
  }>();
  const params = new URLSearchParams(period);
  if (mode === "members" && search) params.set("q", search);
  if (actor && mode === "tasks") params.set("member", actor.id);
  const cursor = cursors.at(-1);
  if (cursor) params.set("cursor", cursor);
  const url = "management-scopes/" + id + "/" + mode + "?" + params;
  useEffect(() => {
    let active = true,
      generation = 0;
    async function read() {
      const request = ++generation;
      if (document.hidden) {
        setResult(undefined);
        return;
      }
      try {
        const data =
          mode === "members"
            ? { actors: await api<Page<Actor>>(url) }
            : { tasks: await api<Page<Task>>(url) };
        if (active && generation === request) setResult({ url, ...data });
      } catch (e) {
        if (active && generation === request)
          setResult({ url, error: (e as Error).message });
      }
    }
    void read();
    const timer = window.setInterval(read, 30000);
    document.addEventListener("visibilitychange", read);
    window.addEventListener("focus", read);
    return () => {
      active = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", read);
      window.removeEventListener("focus", read);
    };
  }, [mode, url, refresh]);

  function choose(next: typeof mode, member?: Actor) {
    setCursors([]);
    setMode(next);
    setActor(member);
  }
  const shown = result?.url === url ? result : undefined;
  const next = shown?.actors?.next ?? shown?.tasks?.next;
  return (
    <section aria-label="管理明细">
      <p>
        显示 {new Date(period.from).toLocaleString("zh-CN")} 至{" "}
        {new Date(period.to).toLocaleString("zh-CN")}{" "}
        发起的运行。负责人为项目当前最终确认人。
      </p>
      <div className="actions">
        <button onClick={() => choose("members")}>按发起人查看</button>
        <button onClick={() => choose("tasks")}>全部任务</button>
        <button
          onClick={() => {
            setResult(undefined);
            setRefresh((n) => n + 1);
          }}
        >
          刷新明细
        </button>
      </div>
      {mode === "members" && (
        <form
          className="usage-filters"
          onSubmit={(e) => {
            e.preventDefault();
            setCursors([]);
            setSearch(
              String(new FormData(e.currentTarget).get("search") ?? "").trim(),
            );
          }}
        >
          <label>
            发起人名称
            <input
              name="search"
              type="search"
              defaultValue={search}
              maxLength={80}
            />
          </label>
          <button>筛选发起人</button>
        </form>
      )}
      {actor && shown?.tasks && (
        <p>
          当前发起人 · <strong>{actor.name}</strong>
        </p>
      )}
      {!shown && <p role="status">正在读取明细…</p>}
      {shown?.error && <p role="alert">{shown.error}</p>}
      {shown?.actors?.rows.map((a) => (
        <p key={a.id}>
          <button onClick={() => choose("tasks", a)}>
            {a.name} · 查看任务
          </button>{" "}
          {a.kind === "application" ? "应用" : "成员"} · {a.runs} 次运行
        </p>
      ))}
      {shown?.tasks?.rows.map((t) => (
        <article className="card management-task" key={t.id}>
          <strong>
            {statusLabel[t.status] ?? "状态待核对"}
            {t.block && " · " + (blockLabel[t.block] ?? "原因待核对")}
          </strong>
          <small>任务 {t.id}</small>
          <p>
            {t.kind === "application" ? "发起应用" : "发起成员"} · {t.actor}
            <br />
            最终确认人 · {t.reviewer}
          </p>
          <small>
            {new Date(t.createdAt).toLocaleString("zh-CN", { hour12: false })}{" "}
            发起 · {t.finishedAt ? "已耗时" : "截至查询已耗时"}{" "}
            {t.elapsedSeconds < 1 ? "不足 1 秒" : t.elapsedSeconds + " 秒"}
          </small>
          {costs && t.costs && (
            <p>
              {t.costs.entries ? (
                <>
                  本范围已记账 ¥{t.costs.recordedCny} · 剩余已知预估 ¥
                  {t.costs.remainingEstimateCny} · 待结算 {t.costs.pending} 笔 ·
                  未知费用 {t.costs.unknown} 笔
                </>
              ) : (
                "本范围没有该任务的费用记录"
              )}
            </p>
          )}
        </article>
      ))}
      {(shown?.actors?.rows.length === 0 ||
        shown?.tasks?.rows.length === 0) && (
        <p>所选范围及周期内没有运行记录。</p>
      )}
      {shown && !shown.error && (
        <div className="actions">
          {cursors.length > 0 && (
            <button onClick={() => setCursors((x) => x.slice(0, -1))}>
              上一页
            </button>
          )}
          {next && (
            <button onClick={() => setCursors((x) => [...x, next])}>
              下一页
            </button>
          )}
        </div>
      )}
    </section>
  );
}

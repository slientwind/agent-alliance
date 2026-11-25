import { useEffect, useState } from "react";
import { api, type Agent, type Project } from "./client";
import { outputOperations } from "./output-operations";
import { StatusIcon } from "./status-icon";

type Grant = {
  id: string;
  project: string;
  projectName: string | null;
  name: string;
  revision: number;
  team: string;
  description: string;
  calls: number;
  used: number;
  expires: string;
  resourceApproved: boolean;
  budgetApproved: boolean;
  operation: string;
  resource: string;
  estimate: string | null;
  remotePolicy: Agent["policy"] | null;
  closed: boolean;
  projectEnded: boolean;
  generation: number;
  canResource: boolean;
  canBudget: boolean;
  canRevoke: boolean;
  canRenew: boolean;
  agent: string;
};

export function ProjectAgentGrants({
  project,
  agents = [],
  refreshProject,
  inbox,
}: {
  inbox?: {
    query: string;
    setQuery: (q: string) => void;
    openProject: (id: string) => void;
  };
  project?: Project;
  agents?: Agent[];
  refreshProject?: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [rows, setRows] = useState<Grant[]>([]),
    [loadError, setLoadError] = useState(""),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false),
    [epoch, reload] = useState(0);
  const base = project
    ? "projects/" + project.id + "/agent-grants"
    : inbox
      ? "project-agent-grants/inbox"
      : "project-agent-grants";
  const visible = open || !!inbox;
  useEffect(() => {
    if (!visible) return;
    let live = true,
      seq = 0;
    async function load() {
      if (document.hidden) return;
      const n = ++seq;
      try {
        const items = await api<Grant[]>(base);
        if (live && n === seq) {
          setRows(items);
          setLoadError("");
        }
      } catch (e) {
        if (live && n === seq) {
          setRows([]);
          setLoadError((e as Error).message);
        }
      } finally {
        if (live && n === seq) setLoading(false);
      }
    }
    setRows([]);
    setLoading(true);
    void load();
    const timer = setInterval(load, 15000);
    window.addEventListener("focus", load);
    document.addEventListener("visibilitychange", load);
    return () => {
      live = false;
      clearInterval(timer);
      window.removeEventListener("focus", load);
      document.removeEventListener("visibilitychange", load);
    };
  }, [visible, base, epoch, project?.revision]);
  async function submit(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await fn();
      if (inbox) setNotice("已保存授权决定。");
      if (refreshProject) await refreshProject();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      reload((n) => n + 1);
    }
  }
  const choices = agents
    .filter((a) => a.team !== project?.team && a.publishedReady)
    .map((a) => ({
      id: a.id,
      revision: a.published,
      name: a.publishedName || a.name,
      team: a.teamName,
      historical: false,
    }));
  for (const g of rows) {
    if (
      g.closed &&
      g.canRenew &&
      !choices.some((a) => a.id === g.agent && a.revision === g.revision)
    ) {
      choices.push({
        id: g.agent,
        revision: g.revision,
        name: g.name,
        team: g.team,
        historical: true,
      });
    }
  }
  const needle = inbox?.query.trim().toLocaleLowerCase() || "";
  const matches = rows.filter((g) =>
    [
      g.name,
      g.team,
      g.description,
      g.projectName || g.project,
      "v" + g.revision,
      pendingDuties(g),
    ]
      .join(" ")
      .toLocaleLowerCase()
      .includes(needle),
  );
  const contents = (
    <>
      {inbox && (
        <>
          <input
            type="search"
            aria-label="搜索授权待办"
            placeholder="搜索能力、团队或职责"
            value={inbox.query}
            onChange={(e) => inbox.setQuery(e.target.value)}
          />
          <button
            type="button"
            disabled={busy || loading}
            onClick={() => reload((n) => n + 1)}
          >
            刷新授权待办
          </button>
        </>
      )}
      {project &&
        choices.length > 0 &&
        !["cancelled", "adopted", "delivered", "failed"].includes(
          project.stage,
        ) && (
          <form
            aria-label="提议跨团队能力"
            onSubmit={(e) => {
              e.preventDefault();
              const f = new FormData(e.currentTarget);
              const a = choices.find(
                (x) => x.id + "/" + x.revision === f.get("agent"),
              );
              if (a)
                void submit(() =>
                  api(base, "POST", {
                    agent: a.id,
                    revision: a.revision,
                    description: f.get("description"),
                    days: Number(f.get("days")),
                    calls: Number(f.get("calls")),
                  }),
                );
            }}
          >
            <label>
              提供给本项目的能力
              <select required name="agent" defaultValue="">
                <option value="">选择已发布修订</option>
                {choices.map((a) => (
                  <option
                    key={a.id + "/" + a.revision}
                    value={a.id + "/" + a.revision}
                  >
                    {a.name} · v{a.revision} · {a.team}
                    {a.historical && " · 本项目原修订"}
                  </option>
                ))}
              </select>
            </label>
            <label>
              可向项目成员展示的用途
              <input name="description" required maxLength={1000} />
            </label>
            <label>
              有效天数
              <input
                name="days"
                type="number"
                required
                min={1}
                max={365}
                defaultValue={7}
              />
            </label>
            <label>
              最多调用次数
              <input
                name="calls"
                type="number"
                required
                min={1}
                max={2147483647}
                defaultValue={1}
              />
            </label>
            <button className="primary" disabled={busy}>
              提交授权申请
            </button>
          </form>
        )}
      {loading ? (
        <p role="status">正在读取授权…</p>
      ) : (
        !loadError &&
        !matches.length && (
          <p role="status">
            {inbox
              ? needle
                ? "没有匹配的授权待办"
                : "当前没有需要你确认的授权"
              : project
                ? "目前没有授权。其他团队的成员加入本项目后，可从自己的能力库选择已发布修订并提出申请。"
                : "当前没有需要你管理的项目授权。"}
          </p>
        )
      )}
      {matches.map((g) => {
        const details = (
          <>
            <p>{g.description}</p>
            <dl className="grant-terms">
              <dt>承担团队</dt>
              <dd>{g.team}</dd>
              <dt>绑定资源</dt>
              <dd>{g.resource}</dd>
              <dt>允许操作</dt>
              <dd>{outputOperations[g.operation] || g.operation}</dd>
              {g.remotePolicy && (
                <>
                  <dt>远端核对</dt>
                  <dd>
                    平台可按原任务编号查询或申请取消。每轮核对窗口{" "}
                    {g.remotePolicy.windowSeconds} 秒，查询间隔{" "}
                    {g.remotePolicy.intervalSeconds} 秒，查询失败最多重试{" "}
                    {g.remotePolicy.readRetries} 次。
                  </dd>
                </>
              )}
              <dt>单次预计</dt>
              <dd>
                {g.estimate === null
                  ? "费用未知，实际金额无法封顶"
                  : "¥" + g.estimate}
              </dd>
              <dt>{g.remotePolicy ? "已提交业务调用" : "已调用"}</dt>
              <dd>
                {g.used} / {g.calls} 次
              </dd>
              <dt>有效至</dt>
              <dd>{new Date(g.expires).toLocaleString()}</dd>
            </dl>
            {g.remotePolicy && (
              <small>
                调用次数按业务提交计算。回执查询及取消的费用按提供方实际账单核对。
              </small>
            )}
            <p
              className={
                "review-state" +
                (!g.projectEnded &&
                !g.closed &&
                (!g.resourceApproved || !g.budgetApproved || g.used >= g.calls)
                  ? " pending"
                  : "")
              }
            >
              <StatusIcon tone={g.closed || g.projectEnded ? "neutral" : g.resourceApproved && g.budgetApproved && g.used < g.calls ? "success" : "pending"} />
              {g.closed
                ? "授权已关闭"
                : g.projectEnded
                  ? "项目已结束"
                  : g.resourceApproved && g.budgetApproved
                    ? g.used >= g.calls
                      ? "授权次数已用完"
                      : "已授权本项目调用"
                    : [
                        !g.resourceApproved && "待资源授权",
                        !g.budgetApproved && "待预算确认",
                      ]
                        .filter(Boolean)
                        .join(" · ")}
            </p>
            {!g.closed &&
              ((!g.projectEnded && g.canResource && !g.resourceApproved) ||
                (!g.projectEnded && g.canBudget && !g.budgetApproved) ||
                g.canRevoke) && (
                <form
                  key={g.generation}
                  onSubmit={(e) => {
                    e.preventDefault();
                    const f = new FormData(e.currentTarget);
                    const btn = (e.nativeEvent as SubmitEvent)
                      .submitter as HTMLButtonElement | null;
                    if (btn)
                      void submit(() =>
                        api("project-agent-grants/" + g.id, "POST", {
                          action: btn.value,
                          expected: g.generation,
                          reason: f.get("reason"),
                        }),
                      );
                  }}
                >
                  <label>
                    处理依据
                    <input required name="reason" maxLength={1000} />
                  </label>
                  {!g.projectEnded && g.canResource && !g.resourceApproved && (
                    <button value="resource" disabled={busy}>
                      授权此修订使用绑定资源
                    </button>
                  )}
                  {!g.projectEnded && g.canBudget && !g.budgetApproved && (
                    <button value="budget" disabled={busy}>
                      确认承担上述调用费用
                    </button>
                  )}
                  {g.canRevoke && (
                    <button value="revoke" disabled={busy}>
                      {inbox ? "关闭此申请" : "撤销项目授权"}
                    </button>
                  )}
                </form>
              )}
          </>
        );
        return inbox ? (
          <details className="grant-detail" key={g.id}>
            <summary>
              <strong>{pendingDuties(g)}</strong> · {g.name} · v{g.revision}
              <small>
                {g.team} · {g.projectName || g.project}
              </small>
            </summary>
            {details}
            {g.projectName && (
              <button
                type="button"
                onClick={() => inbox.openProject(g.project)}
              >
                打开所属项目
              </button>
            )}
          </details>
        ) : (
          <section key={g.id} className="grant-detail">
            <h3>
              {g.name} · v{g.revision}
            </h3>
            {!project && <small>项目 · {g.projectName || g.project}</small>}
            {details}
          </section>
        );
      })}
      {loadError && <p role="alert">{loadError}</p>}
      {error && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
    </>
  );
  return inbox ? (
    contents
  ) : (
    <details className="card" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>
        {project ? "跨团队能力授权" : "我可处理的跨团队能力授权"}
      </summary>
      {open && contents}
    </details>
  );
}

function pendingDuties(g: Grant) {
  return [
    g.canResource && !g.resourceApproved && "资源授权",
    g.canBudget && !g.budgetApproved && "费用确认",
  ]
    .filter(Boolean)
    .join("、");
}

import { useEffect, useState } from "react";
import { api, statusLabel } from "./client";
import { exactCny } from "./format-money";
import { Sparkline } from "./sparkline";
import { TraceWaterfall } from "./trace-waterfall";
import "./admin-monitor.css";

type Team = { id: string; name: string };
type Quality = { total: number; met: number };
type Overview = {
  kpi: { calls: number; successfulCalls: number; tokens: number; cachedTokens: number; costMicroCny: string; undisclosed: number };
  quality: { lightweight: Quality; collaborative: Quality };
  funnel: { submitted: number; completed: number; outputPassed: number; accepted: number };
  taskTrend: { day: string; tasks: number; completed: number; lightweight: number; effective: number; collaborative: number; approved: number }[];
  usageTrend: { day: string; tokens: number; costMicroCny: string }[];
};
type AgentRow = {
  teamId: string; agentId: string; name: string; source: string; calls: number; successRate: number | null;
  effectiveRate: number | null; approvalRate: number | null; p50Ms: number | null; p95Ms: number | null;
  tokens: number; costMicroCny: string; undisclosed: number;
};
type Run = { runId: string; status: string; createdAt: string; projectId?: string; title?: string; teamId?: string; blockedReason?: string };
type Queue = { counts: Record<string, number>; rows: Run[] };
type Alert = { id: string; ruleId: string; message: string; state: string; createdAt: string; attempts: number };
type Alerts = { rules: { id: string; name: string; teamId: string | null; enabled: boolean }[]; deliveries: Alert[] };

const root = "admin/monitor/";
const number = new Intl.NumberFormat("zh-CN");
const percentage = new Intl.NumberFormat("zh-CN", { style: "percent", maximumFractionDigits: 1 });
const date = new Intl.DateTimeFormat("zh-CN", { dateStyle: "short", timeStyle: "short" });
const sources: Record<string, string> = { model: "模型", tool: "工具", external: "外部 Agent" };
const alertStates: Record<string, string> = { pending: "待推送", sending: "正在推送", sent: "已发送", failed: "发送失败", cancelled: "已取消", unknown: "送达状态待核对" };
const funnelLabels = { submitted: "提交任务", completed: "执行完成", outputPassed: "成果检查通过", accepted: "结果采纳" };

function rate(value: number | null) {
  return value === null ? "—" : percentage.format(value);
}

function measure(quality: Quality) {
  return quality.total ? quality.met / quality.total : null;
}

function Trend({ title, days, series }: { title: string; days: string[]; series: { label: string; values: number[]; latest: string }[] }) {
  return <section className="card admin-trend" aria-label={title}>
    <h2>{title}</h2>
    {days.length > 0 && <p>{days[0]} 至 {days.at(-1)}</p>}
    {series.map(item => <div key={item.label}>
      <span>{item.label} <strong>{item.latest}</strong></span>
      <Sparkline values={item.values} label={item.label} />
    </div>)}
  </section>;
}

function Metrics({ overview }: { overview: Overview }) {
  const { kpi, quality, funnel, taskTrend, usageTrend } = overview;
  const latestUsage = usageTrend.at(-1);
  const latestTask = taskTrend.at(-1);
  return <>
    <dl className="admin-kpis" aria-label="关键指标">
      <div><dt>调用量</dt><dd>{number.format(kpi.calls)}</dd></div>
      <div><dt>调用成功率</dt><dd>{rate(kpi.calls ? kpi.successfulCalls / kpi.calls : null)}</dd></div>
      <div><dt>用量</dt><dd>{number.format(kpi.tokens)} token</dd><dd>其中缓存 {number.format(kpi.cachedTokens)}</dd></div>
      <div><dt>已计费用</dt><dd>{exactCny(kpi.costMicroCny)}</dd><dd>{number.format(kpi.undisclosed)} 次未上报费用</dd></div>
      <div><dt>轻量任务有效率</dt><dd>{rate(measure(quality.lightweight))}</dd><dd>{quality.lightweight.met} / {quality.lightweight.total}</dd></div>
      <div><dt>协作成果采纳率</dt><dd>{rate(measure(quality.collaborative))}</dd><dd>{quality.collaborative.met} / {quality.collaborative.total}</dd></div>
    </dl>
    <section className="card" aria-label="任务质量漏斗">
      <h2>任务质量漏斗</h2>
      <ol className="admin-funnel">
        {(Object.keys(funnelLabels) as (keyof typeof funnelLabels)[]).map(key => <li key={key}>
          <span>{funnelLabels[key]}</span>
          <meter aria-label={funnelLabels[key]} min={0} max={Math.max(funnel.submitted, 1)} value={funnel[key]} />
          <strong>{number.format(funnel[key])}</strong>
        </li>)}
      </ol>
    </section>
    <div className="admin-trends">
      <Trend title="执行质量趋势" days={taskTrend.map(row => row.day)} series={[
        { label: "轻量任务有效率", values: taskTrend.map(row => row.lightweight ? row.effective / row.lightweight : NaN),
          latest: rate(latestTask?.lightweight ? latestTask.effective / latestTask.lightweight : null) },
        { label: "协作成果采纳率", values: taskTrend.map(row => row.collaborative ? row.approved / row.collaborative : NaN),
          latest: rate(latestTask?.collaborative ? latestTask.approved / latestTask.collaborative : null) },
      ]} />
      <Trend title="用量与费用趋势" days={usageTrend.map(row => row.day)} series={[
        { label: "用量", values: usageTrend.map(row => row.tokens), latest: latestUsage ? number.format(latestUsage.tokens) : "—" },
        { label: "已计费用", values: usageTrend.map(row => Number(row.costMicroCny)), latest: latestUsage ? exactCny(latestUsage.costMicroCny) : "—" },
      ]} />
    </div>
  </>;
}

function RunList({ runs, inspect }: { runs: Run[]; inspect: (run: string) => void }) {
  if (!runs.length) return <p>暂无运行。</p>;
  return <ul className="admin-run-list">{runs.map(run => <li key={run.runId}>
    <span>{run.title || "受限任务"} · {statusLabel[run.status] || run.status} · {date.format(new Date(run.createdAt))}</span>
    {run.projectId && <button type="button" onClick={() => inspect(run.runId)}>查看运行详情</button>}
  </li>)}</ul>;
}

export function AdminMonitor() {
  const [teams, setTeams] = useState<Team[]>([]);
  const [team, setTeam] = useState("");
  const [days, setDays] = useState("7");
  const [source, setSource] = useState("");
  const [sort, setSort] = useState("calls");
  const [offset, setOffset] = useState(0);
  const [refresh, setRefresh] = useState(0);
  const [trace, setTrace] = useState("");
  const [overview, setOverview] = useState<Overview>();
  const [agents, setAgents] = useState<AgentRow[]>();
  const [queue, setQueue] = useState<Queue>();
  const [alerts, setAlerts] = useState<Alerts>();
  const [selection, setSelection] = useState<AgentRow>();
  const [runs, setRuns] = useState<Run[]>();
  const [error, setError] = useState("");
  const [liveError, setLiveError] = useState("");
  const [runsError, setRunsError] = useState("");

  const period = new URLSearchParams({ days, ...(team ? { team } : {}) }).toString();
  const query = period + "&" + new URLSearchParams({ source, sort, offset: String(offset) });

  useEffect(() => {
    let live = true;
    api<Team[]>("teams").then(rows => { if (live) setTeams(rows); })
      .catch(() => { if (live) setError("暂时无法读取团队列表"); });
    return () => { live = false; };
  }, []);

  useEffect(() => {
    let live = true;
    setError("");
    setOverview(undefined);
    api<{ overview: Overview }>(root + "overview?" + period).then(result => { if (live) setOverview(result.overview); })
      .catch(error => { if (live) setError((error as Error).message); });
    return () => { live = false; };
  }, [period, refresh]);

  useEffect(() => {
    let live = true;
    setAgents(undefined);
    api<{ rows: AgentRow[] }>(root + "agents?" + query).then(value => { if (live) setAgents(value.rows); })
      .catch(error => { if (live) setError((error as Error).message); });
    return () => { live = false; };
  }, [query, refresh]);

  useEffect(() => {
    let live = true, revision = 0;
    setQueue(undefined);
    setAlerts(undefined);
    const reload = async () => {
      if (document.hidden) return;
      const ticket = ++revision;
      try {
        const [nextQueue, nextAlerts] = await Promise.all([
          api<Queue>(root + "queue?" + new URLSearchParams({ team })), api<Alerts>(root + "alerts"),
        ]);
        if (!live || ticket !== revision) return;
        setQueue(nextQueue);
        setAlerts(nextAlerts);
        setLiveError("");
      } catch (error) {
        if (live && ticket === revision) setLiveError((error as Error).message);
      }
    };
    void reload();
    const interval = window.setInterval(() => void reload(), 15000);
    document.addEventListener("visibilitychange", reload);
    return () => {
      live = false;
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", reload);
    };
  }, [team, refresh]);

  useEffect(() => {
    if (!selection) return;
    let live = true;
    setRuns(undefined);
    setRunsError("");
    const filter = new URLSearchParams({ days, team: selection.teamId });
    api<Run[]>(root + "agents/" + encodeURIComponent(selection.agentId) + "/runs?" + filter)
      .then(result => { if (live) setRuns(result); }).catch(error => { if (live) setRunsError((error as Error).message); });
    return () => { live = false; };
  }, [selection, days]);

  function resetPage() {
    setOffset(0);
    setSelection(undefined);
  }

  const visibleAlerts = alerts?.deliveries.filter(delivery => !team || alerts.rules.some(rule =>
    rule.id === delivery.ruleId && (!rule.teamId || rule.teamId === team)));

  return <div className="admin-monitor">
    <div className="row admin-filters">
      <label>统计时间<select value={days} onChange={event => { setDays(event.target.value); resetPage(); }}>
        <option value="1">最近 24 小时</option><option value="7">最近 7 天</option><option value="30">最近 30 天</option><option value="90">最近 90 天</option>
      </select></label>
      <label>团队<select value={team} onChange={event => { setTeam(event.target.value); resetPage(); }}>
        <option value="">全部团队</option>{teams.map(row => <option key={row.id} value={row.id}>{row.name}</option>)}
      </select></label>
      <button type="button" onClick={() => setRefresh(value => value + 1)}>刷新监控数据</button>
    </div>
    {error && <p role="alert">{error}</p>}
    {overview ? <Metrics overview={overview} /> : !error && <p role="status">正在读取监控指标…</p>}
    <section className="card" aria-label="Agent 运行榜单">
      <h2>Agent 运行榜单</h2>
      <div className="row admin-filters">
        <label>来源<select value={source} onChange={event => { setSource(event.target.value); resetPage(); }}>
          <option value="">全部来源</option>{Object.entries(sources).map(([key, name]) => <option key={key} value={key}>{name}</option>)}
        </select></label>
        <label>排序<select value={sort} onChange={event => { setSort(event.target.value); resetPage(); }}>
          <option value="calls">调用次数</option><option value="success">成功率</option><option value="effective">轻量任务有效率</option>
          <option value="approved">协作成果采纳率</option><option value="cost">费用</option><option value="p95">P95 耗时</option>
        </select></label>
      </div>
      {agents?.length ? <div className="admin-table"><table>
        <thead><tr>{["Agent", "来源", "调用量", "成功率", "轻量有效率", "协作采纳率", "P50 / P95 耗时", "用量", "已计费用"].map(label => <th key={label} scope="col">{label}</th>)}</tr></thead>
        <tbody>{agents.map(row => <tr key={`${row.teamId}/${row.agentId}/${row.source}`}>
          <th scope="row"><button type="button" onClick={() => setSelection(row)}>{row.name}</button></th>
          <td>{sources[row.source]}</td><td>{number.format(row.calls)}</td><td>{rate(row.successRate)}</td>
          <td>{rate(row.effectiveRate)}</td><td>{rate(row.approvalRate)}</td><td>{row.p50Ms ?? "—"} / {row.p95Ms ?? "—"} ms</td>
          <td>{number.format(row.tokens)}</td><td>{exactCny(row.costMicroCny)}{row.undisclosed > 0 && <small> · {row.undisclosed} 次未上报</small>}</td>
        </tr>)}</tbody>
      </table></div> : agents?.length === 0 ? <p>暂无 Agent 运行数据。</p> : null}
      {(offset > 0 || agents?.length === 25) && <div className="row">
        <button type="button" disabled={offset === 0} onClick={() => { setOffset(offset - 25); setSelection(undefined); }}>上一页</button>
        <button type="button" disabled={agents?.length !== 25 || offset >= 9975} onClick={() => { setOffset(offset + 25); setSelection(undefined); }}>下一页</button>
      </div>}
      {selection && <section aria-label={selection.name + " 运行记录"}>
        <h3>{selection.name} 运行记录</h3>
        {runsError ? <p role="alert">{runsError}</p> : runs ? <RunList runs={runs} inspect={setTrace} /> : <p role="status">正在读取运行记录…</p>}
      </section>}
    </section>
    <div className="admin-trends">
      <section className="card" aria-label="实时运行队列">
        <h2>实时运行队列</h2>
        {liveError && <p role="alert">{liveError}</p>}
        {queue && <><p>{Object.entries(queue.counts).map(([state, count]) => `${statusLabel[state] || state} ${count}`).join(" · ") || "当前队列为空"}</p>
          <RunList runs={queue.rows} inspect={setTrace} /></>}
      </section>
      <section className="card" aria-label="告警记录">
        <h2>告警记录</h2>
        {visibleAlerts?.length === 0 && <p>暂无告警。</p>}
        <ul className="admin-run-list">{visibleAlerts?.map(item => <li key={item.id}>
          <div><strong>{alerts?.rules.find(rule => rule.id === item.ruleId)?.name}</strong><p>{item.message}</p></div>
          <span>{alertStates[item.state] || item.state} · {date.format(new Date(item.createdAt))}</span>
        </li>)}</ul>
      </section>
    </div>
    {trace && <TraceWaterfall key={trace} run={trace} close={() => setTrace("")} />}
  </div>;
}

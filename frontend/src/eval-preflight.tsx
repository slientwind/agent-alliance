import { useEffect, useState } from "react";
import { api } from "./client";

export type EvalModel = { id: string; label: string; enabled: boolean; capabilities: string[] };
export type SpaceOption = { id: string; kind: "team" | "personal" | "commons"; name: string; role: string; teamId?: string };
type Suite = { id: string; name: string; caseCount: number };
type Outcome = { passed: boolean; latencyMs: number; deviations: string[] | null };
type Report = {
  caseCount?: number;
  baselinePassRate?: number;
  candidatePassRate?: number;
  baselineLatencyMs?: number;
  candidateLatencyMs?: number;
  passRateDelta?: number;
  latencyDeltaMs?: number;
  cases?: { caseId: string; baseline: Outcome | null; candidate: Outcome | null }[];
};
type Run = { id: string; kind: "model" | "knowledge"; state: string; report: Report; createdAt: string };

const statusText: Record<string, string> = { queued: "排队中", running: "评测中", completed: "已完成", failed: "评测失败", revoked: "权限已变更" };
const deviationName: Record<string, string> = { fact: "关键事实", number: "指标数值", citation: "知识引用", unverified_citation: "未核验引用",
  invalid_output: "结果格式", model_unavailable: "模型不可用", knowledge_unavailable: "知识不可用", access_ended: "权限已变更",
  result_unconfirmed: "结果未确认" };

function percent(rate?: number) {
  return typeof rate === "number" ? new Intl.NumberFormat("zh-CN", { style: "percent", maximumFractionDigits: 1 }).format(rate) : "—";
}

export function EvaluationPanel({ models = [], fixedSpace, initialKind = "model" }: {
  models?: EvalModel[]; fixedSpace?: string; initialKind?: "model" | "knowledge";
}) {
  const [spaces, setSpaces] = useState<SpaceOption[]>([]);
  const [chosenSpace, setSpace] = useState("");
  const [suites, setSuites] = useState<Suite[]>([]);
  const [suite, setSuite] = useState("");
  const [runs, setRuns] = useState<Run[]>([]);
  const [kind, setKind] = useState(initialKind);
  const [model, setModel] = useState("");
  const [tier, setTier] = useState("default");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const space = fixedSpace || chosenSpace;
  const path = space ? `spaces/${encodeURIComponent(space)}/eval-suites` : "";
  const reportPath = path && suite ? `${path}/${encodeURIComponent(suite)}/runs` : "";
  const active = runs.some(run => run.state === "queued" || run.state === "running");

  useEffect(() => {
    if (fixedSpace) return;
    let live = true;
    api<SpaceOption[]>("spaces").then(items => {
      if (!live) return;
      const allowed = items.filter(item => item.kind === "team" && item.role === "space_admin");
      setSpaces(allowed);
      setSpace(allowed[0]?.id || "");
    }).catch(error => { if (live) setError(error.message); });
    return () => { live = false; };
  }, [fixedSpace]);

  useEffect(() => {
    if (!path) return;
    let live = true;
    api<Suite[]>(path).then(items => {
      if (!live) return;
      setSuites(items);
      setSuite(selected => items.some(item => item.id === selected) ? selected : items[0]?.id || "");
    }).catch(error => { if (live) setError(error.message); });
    return () => { live = false; };
  }, [path]);

  useEffect(() => {
    if (!reportPath) return;
    let live = true;
    const load = () => api<Run[]>(reportPath).then(items => { if (live) setRuns(items); })
      .catch(error => { if (live) setError(error.message); });
    void load();
    const timer = active ? window.setInterval(load, 2500) : undefined;
    return () => { live = false; window.clearInterval(timer); };
  }, [reportPath, active]);

  async function createSuite() {
    setBusy(true);
    setError("");
    try {
      const created = await api<{ id: string }>(path, "POST", { name: name.trim() });
      setSuites(await api<Suite[]>(path));
      setSuite(created.id);
      setName("");
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function launch() {
    setBusy(true);
    setError("");
    try {
      await api(reportPath, "POST", { kind, baselineTier: tier, ...(kind === "model" ? { candidateModelId: model } : {}) });
      setRuns(await api<Run[]>(reportPath));
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const latest = runs[0];
  const report = latest?.report;
  const regressions = report?.cases?.filter(item => item.baseline?.passed && !item.candidate?.passed).length;
  const selectedCount = suites.find(item => item.id === suite)?.caseCount || 0;
  return <section className="card" aria-label="黄金回归体检">
    <h2>黄金回归体检</h2>
    {error && <p role="alert">{error}</p>}
    {!fixedSpace && <label>团队空间<select value={space} onChange={event => {
      setSpace(event.target.value); setSuite(""); setSuites([]); setRuns([]);
    }}><option value="">选择团队空间</option>{spaces.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>}
    {space && <>
      <div className="admin-filters">
        <label>体检集<select value={suite} onChange={event => { setSuite(event.target.value); setRuns([]); }}>
          <option value="">选择体检集</option>{suites.map(item => <option key={item.id} value={item.id}>{item.name} · {item.caseCount} 条用例</option>)}
        </select></label>
        <label>变更类型<select value={kind} onChange={event => setKind(event.target.value as typeof kind)}>
          {models.length > 0 && <option value="model">切换主模型</option>}
          <option value="knowledge">更新知识库</option>
        </select></label>
        <label>基准档位<select value={tier} onChange={event => setTier(event.target.value)}>
          <option value="default">通用</option><option value="fast">快速</option><option value="reasoning">深度思考</option>
        </select></label>
        {kind === "model" && <label>候选模型<select value={model} onChange={event => setModel(event.target.value)}>
          <option value="">选择候选模型</option>
          {models.filter(item => item.enabled && item.capabilities.includes("text")).map(item => <option key={item.id} value={item.id}>{item.label}</option>)}
        </select></label>}
        <button type="button" className="primary" disabled={busy || active || !selectedCount || kind === "model" && !model} onClick={() => void launch()}>运行回归体检</button>
      </div>
      {suite && !selectedCount && <p>可从已完成的团队会话加入体检用例。</p>}
      {latest && <p role="status">{statusText[latest.state] || latest.state} · {latest.kind === "model" ? "模型切换预检" : "知识变更预检"}</p>}
      {latest?.state === "completed" && report && <div>
        <dl className="admin-run-list">
          <div><dt>通过率</dt><dd>{percent(report.baselinePassRate)} → {percent(report.candidatePassRate)}</dd></div>
          <div><dt>平均响应</dt><dd>{report.baselineLatencyMs ?? "—"} → {report.candidateLatencyMs ?? "—"} ms</dd></div>
          <div><dt>出现回归的用例</dt><dd>{regressions ?? 0} / {report.caseCount ?? 0}</dd></div>
        </dl>
        {!!report.cases?.some(item => item.candidate?.deviations?.length) && <details>
          <summary>查看口径偏差</summary>
          <ol className="admin-run-list">{report.cases.map((item, index) => !!item.candidate?.deviations?.length && <li key={item.caseId}>
            <span>用例 {index + 1}</span><span>{item.candidate.deviations.map(issue => deviationName[issue.split(":")[0]] || "待核查").join("、")}</span>
          </li>)}</ol>
        </details>}
      </div>}
      <details>
        <summary>新建体检集</summary>
        <label>名称<input value={name} maxLength={120} onChange={event => setName(event.target.value)} /></label>
        <button type="button" disabled={busy || !name.trim()} onClick={() => void createSuite()}>新建</button>
      </details>
    </>}
  </section>;
}

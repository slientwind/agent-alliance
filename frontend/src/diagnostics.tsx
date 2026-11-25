import { useCallback, useEffect, useRef, useState } from "react";
import { api, statusLabel, type Project } from "./client";

const scopes = {
  structure: "调用结构与日志",
  content: "业务正文节选",
  export: "诊断导出",
};
type Scope = keyof typeof scopes;
type Entry = {
  id: string;
  start: string;
  status: string;
  plan: number;
  kind: string;
  structure: boolean;
  expired: boolean;
  expires: string | null;
  grants: { member: string; scope: Scope; expires: string }[] | null;
};
type Span = {
  attempts: { number: number; status: string; spanId: string }[];
  spanId: string;
  tokens: number | null;
  step: number;
  agent: string;
  revision: number;
  type: string;
  resource: string | null;
  resourceRevision: number | null;
  parents: number[];
  status: string;
  start: string | null;
  end: string | null;
};
type TraceEvent = {
  sequence: number;
  time: string;
  step: number | null;
  kind: string;
  code: string;
};
type Excerpt = {
  remoteTask: string | null;
  remoteContext: string | null;
  step: number;
  brief: string;
  committedOutput: string | null;
  externalTraces?: { id: string; url: string }[];
};
type ExternalReceipt = {
  id: string;
  step: number;
  attempt: number;
  operation: "submit" | "poll" | "cancel" | "supplement";
  costDisclosure: "reported" | "undisclosed";
  receivedAt: string;
  usage?: { inputTokens?: number; outputTokens?: number; totalTokens?: number; cachedTokens?: number };
};
const remoteOperations = { submit: "提交", poll: "查询", cancel: "取消", supplement: "补件" };
type Response = {
  trace: {
    run: string;
    traceId: string;
    spans: Span[];
    events: TraceEvent[];
    externalReceipts?: ExternalReceipt[];
    end: string | null;
  };
  rights: Record<Scope, boolean>;
  content: Excerpt | null;
  asOf: string;
};
const labels: Record<string, string> = {
  ...statusLabel,
  pending: "尚未调用",
  submitted: "已提交调用",
  remote: "等待外部任务",
  unknown: "结果未知",
  submission_overdue: "执行超时，等待原调用回执",
  lookup_invalid_request: "模型未返回有效的检索指令",
  lookup_invalid_source: "资料原文位置需要核对",
  lookup_empty_search: "本次检索未命中原文",
  lookup_searched: "已按模型选词检索方案资料",
  lookup_semantic_searched: "选词未命中，已按问题语义找到方案原文",
  lookup_semantic_fresh: "新资料已在本次语义检索中找到原文",
  lookup_semantic_index_pending: "语义索引尚未覆盖本次全部资料",
  lookup_index_waiting: "保存选词并等待原资料索引",
  lookup_index_rechecked: "索引就绪后已重新核对原资料",
  lookup_index_no_evidence: "索引就绪后仍无可引用原文",
  lookup_semantic_unavailable: "选词未命中，语义检索暂不可用",
  lookup_calculation_needs_input: "引文缺少可核算的同币种金额，已跳过作答调用",
  lookup_calculated: "已从引用原文取数并完成本地金额核算",
  lookup_calculation_rejected: "金额或币种无法核对，已停止计算",
  lookup_answer: "已核对回答所引用的原文",
  lookup_requirement_evidence: "已整理必交项的原文依据与待补提示",
  lookup_quantity_mismatch: "回答中的数值与所引原文无法对应",
  lookup_unverified: "回答格式或引用有误，保留原文供审核",
  lookup_ask: "证据不足，需要补充资料",
  lookup_refined: "已按首轮引文追加检索",
  lookup_no_progress: "追加检索没有找到新依据，已保留首轮引文",
  lookup_turn_invalidated: "续查依据或授权已失效，已停止续查",
  skill_unavailable: "绑定的 Skill 正文或授权不可用，执行已等待处理",
  lookup_no_evidence: "没有可核对的原文，已跳过回答调用",
  receipt_pending: "已有调用记录，需核对回执",
  delegation_expired: "应用或委托已失效",
  verification_failed: "绑定修订的必要检查待复验",
  resource_denied: "资源授权需核验",
  reviewer_changed: "修改职责已变化",
  base_changed: "基准版本已变化",
  input_changed: "资料、规则或偏好需核验",
  working_memory_invalidated: "已用来源、偏好、规则或上游结果失效，等待重新核对",
  budget_denied: "预算或费用授权需核验",
  remote_pending: "等待外部服务回执",
  call_hold: "负责人记录核对依据，继续等待",
  call_redo: "负责人承担重复风险，授权一次新调用",
  input_rejected: "工具已拒绝输入",
  outcome_unknown: "调用结果未知，需人工核对",
};
const date = (value: string) =>
  new Date(value).toLocaleString("zh-CN", { hour12: false });
export function Diagnostics({
  project,
  owner,
  refresh,
}: {
  project: Project;
  owner: boolean;
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  return (
    <details className="card" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>运行诊断与 Trace</summary>
      {open && <Directory project={project} owner={owner} refresh={refresh} />}
    </details>
  );
}
function Directory({
  project,
  owner,
  refresh,
}: {
  project: Project;
  owner: boolean;
  refresh: () => Promise<void>;
}) {
  const [entries, setEntries] = useState<Entry[]>([]),
    [cursor, setCursor] = useState(""),
    [selected, select] = useState(""),
    [error, setError] = useState(""),
    [epoch, invalidate] = useState(0);
  const base = "projects/" + project.id;
  useEffect(() => {
    let active = true;
    async function load() {
      try {
        const res = await api<{ runs: Entry[] }>(
          base + "/diagnostics?before=" + cursor,
        );
        if (active) {
          setEntries(res.runs);
          setError("");
        }
      } catch (e) {
        if (active) {
          setEntries([]);
          setError((e as Error).message);
        }
      }
    }
    void load();
    return () => {
      active = false;
    };
  }, [base, cursor, epoch, project.revision]);
  const run = entries.slice(0, 40).find((e) => e.id === selected) || entries[0];
  return (
    <section>
      {error && <p role="alert">{error}</p>}
      {entries.length > 0 && (
        <label>
          选择运行
          <select value={run.id} onChange={(e) => select(e.target.value)}>
            {entries.slice(0, 40).map((x) => (
              <option key={x.id} value={x.id}>
                {date(x.start)} ·{" "}
                {x.kind === "revision" ? "局部改稿" : "协作执行"} ·{" "}
                {labels[x.status]} · {x.id.slice(0, 8)}
              </option>
            ))}
          </select>
        </label>
      )}
      {entries.length > 40 && (
        <button onClick={() => setCursor(entries[39].id)}>更早的运行</button>
      )}
      {cursor && <button onClick={() => setCursor("")}>最近运行</button>}
      {run && (
        <>
          {run.expires && <small>详细诊断保留至 {date(run.expires)}</small>}
          {run.expired ? (
            <p>此运行的详细诊断保留期已结束。</p>
          ) : run.structure ? (
            <TraceView
              key={run.id + ":" + project.revision + ":" + epoch}
              endpoint={base + "/runs/" + run.id + "/trace"}
            />
          ) : (
            <p>查看技术详情需要发起人授予本次运行的诊断权限。</p>
          )}
          {owner && !run.expired && (
            <details>
              <summary>管理本次运行的诊断授权</summary>
              <p>
                发起人可查看调用结构。其他成员需要结构权限，查看正文或导出还须分别授权。正文另核验所引用资料和偏好的访问权。
              </p>
              {(run.grants || []).map((g) => (
                <small key={g.member + g.scope}>
                  {project.participants.find((m) => m.id === g.member)?.name} ·{" "}
                  {scopes[g.scope]} · 至 {date(g.expires)}
                </small>
              ))}
              <form
                onSubmit={async (e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  try {
                    await api(
                      base + "/runs/" + run.id + "/trace-access",
                      "PATCH",
                      {
                        member: f.get("member"),
                        scope: f.get("scope"),
                        allowed: f.get("action") === "grant",
                        hours: Number(f.get("hours")),
                        reason: f.get("reason"),
                        expected: project.revision,
                      },
                    );
                    await refresh();
                    invalidate((n) => n + 1);
                    setError("");
                  } catch (e) {
                    setError((e as Error).message);
                  }
                }}
              >
                <label>
                  诊断成员
                  <select name="member">
                    {project.participants
                      .filter((m) => m.enabled)
                      .map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.name}
                        </option>
                      ))}
                  </select>
                </label>
                <label>
                  诊断范围
                  <select name="scope">
                    {Object.entries(scopes).map(([key, label]) => (
                      <option key={key} value={key}>
                        {label}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  授权操作
                  <select name="action">
                    <option value="grant">授予或续期</option>
                    <option value="revoke">撤销</option>
                  </select>
                </label>
                <label>
                  有效小时
                  <input
                    type="number"
                    name="hours"
                    min={1}
                    max={720}
                    defaultValue={24}
                    required
                  />
                </label>
                <label>
                  诊断授权原因
                  <input name="reason" required />
                </label>
                <button>保存诊断授权</button>
              </form>
            </details>
          )}
        </>
      )}
    </section>
  );
}
function TraceView({ endpoint }: { endpoint: string }) {
  const [response, setResponse] = useState<Response | null>(null),
    [error, setError] = useState(""),
    [excerpt, setExcerpt] = useState<Excerpt | null>(null),
    [step, setStep] = useState(0);
  const seq = useRef(0),
    bodyRequest = useRef(0);
  const clearContent = useCallback(() => {
    ++bodyRequest.current;
    setExcerpt(null);
  }, []);
  const reload = useCallback(async () => {
    const current = ++seq.current;
    try {
      const data = await api<Response>(endpoint);
      if (current === seq.current) {
        setResponse(data);
        if (!data.rights.content) clearContent();
        setError("");
      }
    } catch (e) {
      if (current === seq.current) {
        setResponse(null);
        clearContent();
        setError((e as Error).message);
      }
    }
  }, [endpoint, clearContent]);
  useEffect(() => {
    void reload();
    const sync = () => {
      clearContent();
      if (!document.hidden) void reload();
    };
    const timer = setInterval(() => {
      if (!document.hidden) void reload();
    }, 10000);
    window.addEventListener("focus", sync);
    document.addEventListener("visibilitychange", sync);
    return () => {
      ++seq.current;
      ++bodyRequest.current;
      clearInterval(timer);
      window.removeEventListener("focus", sync);
      document.removeEventListener("visibilitychange", sync);
    };
  }, [reload, clearContent]);
  async function showBody() {
    clearContent();
    const current = bodyRequest.current;
    try {
      const data = await api<Response>(endpoint + "?mode=content&step=" + step);
      if (current === bodyRequest.current) {
        setExcerpt(data.content);
        setError("");
      }
    } catch (e) {
      if (current === bodyRequest.current) setError((e as Error).message);
    }
  }
  async function download(include: boolean) {
    try {
      const res = await fetch(
        "/api/" + endpoint + "?mode=export" + (include ? "&step=" + step : ""),
        { cache: "no-store" },
      );
      if (!res.ok) throw new Error((await res.json()).error);
      const url = URL.createObjectURL(await res.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = "trace-" + response!.trace.run + ".json";
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      setError((e as Error).message);
    }
  }
  const trace = response?.trace;
  return (
    <section>
      {error && <p role="alert">{error}</p>}
      {trace && response && (
        <>
          <p>
            Trace ID <code>{trace.traceId}</code>
          </p>
          <label>
            筛选步骤
            <select
              value={step}
              onChange={(e) => {
                clearContent();
                setStep(Number(e.target.value));
              }}
            >
              <option value={0}>全部步骤</option>
              {trace.spans.map((s) => (
                <option value={s.step} key={s.step}>
                  步骤 {s.step}
                </option>
              ))}
            </select>
          </label>
          {trace.spans
            .filter((s) => !step || step === s.step)
            .map((s) => (
              <div key={s.step}>
                <p>
                  <strong>
                    步骤 {s.step} · {labels[s.status]}
                  </strong>{" "}
                  ·{" "}
                  {s.type === "tool"
                    ? "工具"
                    : s.type === "external"
                      ? "外部 Agent"
                      : "模型"}{" "}
                  · Agent v{s.revision}
                </p>
                {s.attempts.length > 1 && (
                  <ul>
                    {s.attempts.map((a) => (
                      <li key={a.number}>
                        第 {a.number} 次 · {labels[a.status] || a.status} · Span{" "}
                        {a.spanId}
                      </li>
                    ))}
                  </ul>
                )}
                <small>
                  Span {s.spanId} · Agent {s.agent}
                  {s.resource &&
                    " · 模型资源 " + s.resource + " v" + s.resourceRevision}
                </small>
                <small>
                  {s.tokens !== null && s.tokens + " token · "}
                  {s.parents.length
                    ? "前置步骤 " + s.parents.join("、")
                    : "独立开始"}
                  {s.start && " · " + date(s.start)}
                  {s.end &&
                    s.start &&
                    " · 耗时 " +
                      (
                        (Date.parse(s.end) - Date.parse(s.start)) /
                        1000
                      ).toFixed(2) +
                      " 秒"}
                </small>
                {trace.externalReceipts?.filter(receipt => receipt.step === s.step).map(receipt => (
                  <small key={receipt.id}>
                    {remoteOperations[receipt.operation]} · 第 {receipt.attempt} 次 · {date(receipt.receivedAt)}
                    {" · "}{receipt.costDisclosure === "reported" ? "已上报用量" : "未上报用量"}
                    {receipt.usage?.totalTokens !== undefined && ` · 共 ${receipt.usage.totalTokens} token`}
                    {receipt.usage?.inputTokens !== undefined && ` · 输入 ${receipt.usage.inputTokens} token`}
                    {receipt.usage?.outputTokens !== undefined && ` · 输出 ${receipt.usage.outputTokens} token`}
                    {receipt.usage?.cachedTokens !== undefined && ` · 缓存 ${receipt.usage.cachedTokens} token`}
                  </small>
                ))}
              </div>
            ))}
          <details>
            <summary>
              状态事件 ·{" "}
              {trace.events.filter((e) => !step || e.step === step).length} 条
            </summary>
            <ol>
              {trace.events
                .filter((e) => !step || e.step === step)
                .map((e) => (
                  <li key={e.sequence}>
                    {date(e.time)} · {e.step ? "步骤 " + e.step : "运行"} ·{" "}
                    {labels[e.code] || e.code}
                  </li>
                ))}
            </ol>
          </details>
          {step > 0 && response.rights.content && (
            <button onClick={() => void showBody()}>
              查看此步骤的正文节选
            </button>
          )}
          {excerpt && (
            <section>
              {excerpt.remoteTask && (
                <p>
                  远端任务编号 <code>{excerpt.remoteTask}</code>
                  {excerpt.remoteContext && (
                    <>
                      {" "}
                      · 上下文 <code>{excerpt.remoteContext}</code>
                    </>
                  )}
                </p>
              )}
              {excerpt.externalTraces?.map((entry, index) => (
                <p key={entry.id}>
                  <a href={entry.url} target="_blank" rel="noreferrer">查看外部追踪 {index + 1}</a>
                </p>
              ))}
              <h3>计划简报</h3>
              <p className="preserve">{excerpt.brief}</p>
              <h3>已提交成果</h3>
              <p className="preserve">
                {excerpt.committedOutput ?? "尚无已提交版本"}
              </p>
              <button onClick={clearContent}>收起正文</button>
            </section>
          )}
          {response.rights.export && (
            <div className="actions">
              <button onClick={() => void download(false)}>
                导出脱敏结构与日志
              </button>
              {step > 0 && excerpt && (
                <button onClick={() => void download(true)}>
                  导出含此步骤正文的记录
                </button>
              )}
            </div>
          )}
        </>
      )}
    </section>
  );
}

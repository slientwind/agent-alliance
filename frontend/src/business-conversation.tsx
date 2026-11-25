import { useEffect, useRef, useState } from "react";
import { BookOpen, ShieldCheck } from "lucide-react";
import { api, type Artifact, type KnowledgeCitation, type Project } from "./client";
import type { SpaceOption } from "./eval-preflight";
import { citationLocation } from "./knowledge-canvas";
import "./business-conversation.css";

export function ArtifactEvidence({ artifact, openCitation }: { artifact: Artifact; openCitation: (item: KnowledgeCitation) => void }) {
  return <>
    {!!artifact.knowledgeCitations?.length && <ul className="citation-chips" aria-label="知识出处">
      {artifact.knowledgeCitations.map(item => <li key={item.documentId + "/" + item.version + "/" + item.ordinal}>
        <button type="button" onClick={() => openCitation(item)}><BookOpen size={14} aria-hidden="true" />
          {item.title} v{item.version} · {citationLocation(item.citationAnchor)}
        </button>
      </li>)}
    </ul>}
    {artifact.businessProvenance?.map((item, index) => <details className="business-provenance" key={item.viewId + "/" + index}>
      <summary>取数口径 · {item.viewName}</summary>
      <dl>
        <dt>数据来源</dt><dd>{item.source}</dd>
        <dt>行权限</dt><dd>{item.access}</dd>
        {item.from && item.until && <><dt>统计区间</dt><dd>从 {item.from} 至 {item.until}（截止日不含）</dd></>}
        {!!item.filters?.length && <><dt>限定条件</dt><dd>{item.filters.join("、")}</dd></>}
        <dt>返回行数</dt><dd>{item.rows}{item.truncated && " · 已达结果上限"}</dd>
        {Object.entries(item.formulas || {}).map(([metric, formula]) => <div key={metric}>
          <dt>{item.formulaSources?.[metric]?.term || metric}</dt>
          <dd><code>{formula}</code>{item.formulaSources?.[metric] && ` · 核定版本 v${item.formulaSources[metric].revision}`}</dd>
        </div>)}
      </dl>
    </details>)}
  </>;
}

type Mutation = {
  mutationId: string;
  label: string;
  systemName?: string;
  recordKey?: string;
  before?: Record<string, unknown>;
  after?: Record<string, unknown>;
  externalReceiptNo?: string;
  status: string;
  revision: number;
  canDecide: boolean;
};
const mutationStates: Record<string, string> = {
  pending: "待审批", approved: "已批准，等待执行", executing: "正在写入系统", reconciling: "核对回执中",
  succeeded: "已写入", denied: "已拒绝", expired: "已失效", failed: "写入失败", unconfirmed: "结果待人工核对",
};

function formatValue(value: unknown) {
  if (value === undefined) return "—";
  return typeof value === "string" ? value : JSON.stringify(value);
}

export function SystemMutations({ project, revision, anchor, refresh }: {
  project: string; revision: number; anchor: string; refresh: () => Promise<void>;
}) {
  const [items, setItems] = useState<Mutation[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [request, forceReload] = useState(0);
  const focusedAnchor = useRef("");
  const path = `projects/${encodeURIComponent(project)}/system-mutations`;

  useEffect(() => {
    let active = true;
    void api<Mutation[]>(path).then(rows => {
      if (active) setItems(rows || []);
    }).catch(error => { if (active) { setItems([]); setError((error as Error).message); } });
    return () => { active = false; };
  }, [path, revision, request]);

  useEffect(() => {
    if (!anchor || focusedAnchor.current === anchor || !items.length) return;
    const target = document.getElementById(anchor);
    if (!target) return;
    focusedAnchor.current = anchor;
    target.focus({ preventScroll: true });
    target.scrollIntoView({ block: "center" });
  }, [anchor, items]);

  async function decide(item: Mutation, accept: boolean) {
    setBusy(item.mutationId);
    setError("");
    try {
      await api(`${path}/${encodeURIComponent(item.mutationId)}/decision`, "PUT", { expected: item.revision, accept });
      setItems(rows => rows.map(row => row.mutationId === item.mutationId ? {
        ...row, canDecide: false, status: accept ? "approved" : "denied",
      } : row));
      await refresh();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      forceReload(value => value + 1);
      setBusy("");
    }
  }

  return <>
    {error && <p role="alert">{error}</p>}
    {items.map(item => {
      const fields = new Set([...Object.keys(item.before || {}), ...Object.keys(item.after || {})]);
      return <article className="card business-mutation" key={item.mutationId} id={"mutation-" + item.mutationId} tabIndex={-1}>
        <header><h2>业务系统写回</h2><span data-status={item.status}>{mutationStates[item.status] || item.status}</span></header>
        <p>{item.systemName && <>{item.systemName} · </>}{item.label}{item.recordKey && ` · ${item.recordKey}`}</p>
        {fields.size > 0 && <div className="business-diff"><table>
          <thead><tr><th scope="col">字段</th><th scope="col">原值</th><th scope="col">拟变更值</th></tr></thead>
          <tbody>{[...fields].map(field => <tr key={field}><th scope="row">{field}</th>
            <td>{formatValue(item.before?.[field])}</td><td>{formatValue(item.after?.[field])}</td>
          </tr>)}</tbody>
        </table></div>}
        {item.externalReceiptNo && <p className="mutation-receipt"><ShieldCheck size={16} aria-hidden="true" /> 幂等回执单号 <code>{item.externalReceiptNo}</code></p>}
        {item.canDecide && <div className="actions">
          <button type="button" className="primary" disabled={!!busy} onClick={() => void decide(item, true)}>批准写入系统（幂等执行）</button>
          <button type="button" disabled={!!busy} onClick={() => void decide(item, false)}>拒绝写回</button>
        </div>}
      </article>;
    })}
  </>;
}

export function PromoteConversation({ project, spaces, open }: {
  project: Project;
  spaces: SpaceOption[];
  open: (space: string, publication: string) => void;
}) {
  const artifacts = project.artifacts.filter(item => item.canPromote);
  const teams = spaces.filter(item => item.kind === "team");
  const [choice, choose] = useState(artifacts.at(-1)?.id ?? "");
  const [team, chooseTeam] = useState(teams[0]?.id ?? "");
  const [receipt, setReceipt] = useState<{ id: string; spaceId: string }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<{ body: string; key: string } | undefined>(undefined);
  const artifact = artifacts.find(item => item.id === choice);

  async function promote() {
    if (busy || !artifact || !team) return;
    setBusy(true);
    setError("");
    const input = { destination: team, kind: "artifact", item: artifact.id, version: artifact.version };
    const body = JSON.stringify(input);
    if (request.current?.body !== body) request.current = { body, key: crypto.randomUUID() };
    try {
      setReceipt(await api<{ id: string; spaceId: string }>(`spaces/${project.spaceId}/promote`, "POST", input,
        { "Idempotency-Key": request.current.key }));
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (receipt) return <>
    <p role="status">已将所选成果版本发布至团队空间。</p>
    <button type="button" onClick={() => open(receipt.spaceId, receipt.id)}>查看团队成果快照</button>
  </>;
  return <form className="form" onSubmit={event => { event.preventDefault(); void promote(); }}>
    <p>团队成员将看到所选成果版本的快照。原对话和协作记录保留在个人空间。</p>
    <label>团队空间<select value={team} onChange={event => chooseTeam(event.target.value)} required>
      {teams.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>
    <label>移交成果<select value={choice} onChange={event => choose(event.target.value)} required>
      {artifacts.map(item => <option key={item.id} value={item.id}>{item.name} · v{item.version}</option>)}
    </select></label>
    {error && <p role="alert">{error}</p>}
    <button className="primary" type="submit" disabled={busy || !artifact || !team}>确认移交成果</button>
  </form>;
}

export function CaptureEvaluationCase({ space, artifact }: { space: string; artifact: Artifact }) {
  const [suites, setSuites] = useState<{ id: string; name: string }[]>();
  const [suite, setSuite] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const path = `spaces/${space}/eval-suites`;

  async function list() {
    setError("");
    try {
      const rows = await api<{ id: string; name: string }[]>(path);
      setSuites(rows);
      setSuite(rows[0]?.id || "");
    } catch (error) {
      setError((error as Error).message);
    }
  }
  async function capture() {
    setError("");
    setMessage("");
    setBusy(true);
    try {
      await api(`${path}/${suite}/cases`, "POST", { artifactId: artifact.id, version: artifact.version });
      setMessage("已保存为脱敏回归用例。");
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <details onToggle={event => { if (event.currentTarget.open && !suites) void list(); }}>
    <summary>加入黄金回归体检集</summary>
    {suites?.length === 0 && <p>请先在管理后台创建体检集。</p>}
    {!!suites?.length && <form className="actions" onSubmit={event => { event.preventDefault(); void capture(); }}>
      <label>体检集<select value={suite} onChange={event => setSuite(event.target.value)}>
        {suites.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
      </select></label>
      <button type="submit" disabled={busy || !suite}>保存为回归用例</button>
    </form>}
    {message && <p role="status">{message}</p>}
    {error && <p role="alert">{error}</p>}
  </details>;
}

import { useEffect, useState } from "react";
import { api } from "./client";

type StudioRelease = {
  id: string;
  revision: number;
  name: string;
  owner: string;
  team: string;
  digest: string;
  spec: { purpose: string; instructions: string; steps: string[]; checks: string[] };
  dependencies: { modelTier: string; capabilities: string[]; externalAgents: { id: string; revision: number }[] };
};

type ExternalRelease = {
  id: string;
  agent: string;
  revision: number;
  name: string;
  description: string;
  state: string;
  buildDigest: string;
  evidenceDigest: string;
};

const labels: Record<string, string> = {
  knowledge: "企业知识库", office: "表格与文档", images: "识图与生图", search: "联网搜索", external: "外部 Agent",
  default: "通用", fast: "快速", reasoning: "深度思考",
};

export function AdminReleases() {
  const [studio, setStudio] = useState<StudioRelease[]>([]);
  const [external, setExternal] = useState<ExternalRelease[]>([]);
  const [selection, setSelection] = useState<"studio" | "external">("studio");
  const [reason, setReason] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let mounted = true;
    Promise.all([api<StudioRelease[]>("admin/studio/releases"), api<ExternalRelease[]>("admin/external-agents/releases")])
      .then(([a, b]) => { if (mounted) { setStudio(a); setExternal(b); } })
      .catch(error => { if (mounted) setError((error as Error).message); })
      .finally(() => { if (mounted) setLoading(false); });
    return () => { mounted = false; };
  }, []);

  async function decide(item: StudioRelease | ExternalRelease, approve: boolean) {
    setBusy(item.id);
    setError("");
    setNotice("");
    const rationale = (reason[item.id] || "").trim();
    try {
      if ("digest" in item) {
        await api("admin/studio/releases/" + item.id, "POST", {
          action: approve ? "approve" : "return", digest: item.digest, reason: rationale,
        });
        setStudio(items => items.filter(row => row.id !== item.id));
      } else {
        await api(`agents/${item.agent}/release-requests/${item.id}`, "POST", {
          action: approve ? "publish" : "return", revision: item.revision,
          buildDigest: item.buildDigest, evidenceDigest: item.evidenceDigest, reason: rationale,
        });
        setExternal(items => items.filter(row => row.id !== item.id));
      }
      setNotice(approve ? `${item.name} 已全企可用` : `${item.name} 已退回`);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  const requests = selection === "studio" ? studio : external;
  return (
    <>
      <div className="row" role="group" aria-label="审批类别">
        <button type="button" aria-pressed={selection === "studio"} onClick={() => setSelection("studio")}>企业助手</button>
        <button type="button" aria-pressed={selection === "external"} onClick={() => setSelection("external")}>外部 Agent 认证</button>
      </div>
      {loading && <p role="status">正在读取发布申请…</p>}
      {error && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
      {!loading && !error && requests.length === 0 && <p>当前没有待审批的申请。</p>}
      {requests.map(item => (
        <section className="card" key={item.id} aria-labelledby={"release-" + item.id}>
          <h2 id={"release-" + item.id}>{item.name}</h2>
          <p>{"spec" in item ? item.owner + " · " + item.team + " · " : ""}版本 {item.revision}</p>
          {"spec" in item ? (
            <details>
              <summary>查看冻结的配置</summary>
              {item.spec.purpose && <p>{item.spec.purpose}</p>}
              {item.spec.instructions && <p>{item.spec.instructions}</p>}
              {item.spec.steps?.length > 0 && <ol>{item.spec.steps.map((step, i) => <li key={i}>{step}</li>)}</ol>}
              {item.spec.checks?.length > 0 && <ul>{item.spec.checks.map((check, i) => <li key={i}>{check}</li>)}</ul>}
              <p>档位 {labels[item.dependencies.modelTier] || item.dependencies.modelTier}</p>
              {item.dependencies.capabilities?.length > 0 && <p>能力 {item.dependencies.capabilities.map(capability => labels[capability]).join("、")}</p>}
              {item.dependencies.externalAgents?.length > 0 && <p>指定外部 Agent 版本 {item.dependencies.externalAgents.length} 个</p>}
            </details>
          ) : (
            <>
              {item.description && <p>{item.description}</p>}
              {item.state !== "pending_verify" && <p>当前版本需重新测试。可将申请退回给负责人。</p>}
            </>
          )}
          <label>退回原因
            <textarea maxLength={800} value={reason[item.id] || ""} disabled={!!busy}
              onChange={event => setReason(values => ({ ...values, [item.id]: event.target.value }))} />
          </label>
          <div className="row">
            <button type="button" className="primary" disabled={!!busy || ("state" in item && item.state !== "pending_verify")}
              onClick={() => void decide(item, true)}>批准上架</button>
            <button type="button" disabled={!!busy || !(reason[item.id] || "").trim()} onClick={() => void decide(item, false)}>退回</button>
          </div>
        </section>
      ))}
    </>
  );
}

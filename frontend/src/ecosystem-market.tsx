import { useEffect, useState } from "react";
import { api } from "./client";

type Entry = {
  id: string;
  name: string;
  kind: "skill" | "mcp" | "agent";
  version: string;
  source: string;
  description: string;
  reviewStatus: "clean" | "review_required" | "blocked";
  license?: string;
  package?: string;
  retired: boolean;
  enabled: boolean;
  bindingRevision: number;
  request?: "pending" | "approved" | "returned";
  reply?: string;
};

const kindLabels = { skill: "Skill", mcp: "MCP 服务", agent: "外部 Agent" };
const reviewLabels = { clean: "体检通过", review_required: "待人工复核", blocked: "已阻断" };

export function EcosystemMarket() {
  const [entries, setEntries] = useState<Entry[]>();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    let active = true;
    api<Entry[]>("ecosystem/catalog").then(data => { if (active) setEntries(data); })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, []);

  async function choose(entry: Entry) {
    setBusy(entry.id);
    setError("");
    setNotice("");
    try {
      if (entry.package) {
        await api(`ecosystem/bindings/${entry.package}`, "PUT", {
          expected: entry.bindingRevision, enabled: !entry.enabled,
        });
        setNotice(entry.enabled ? `已卸载 ${entry.name}` : `${entry.name} 已加载到我的助手`);
      } else {
        await api(`ecosystem/catalog/${entry.id}/request`, "PUT");
        setNotice(`${entry.name} 的准入申请已提交`);
      }
      setEntries(await api<Entry[]>("ecosystem/catalog"));
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  return <section aria-label="生态发现市场">
    <h2>生态发现市场</h2>
    {error && <p role="alert">{error}</p>}
    {notice && <p role="status">{notice}</p>}
    {!entries && !error && <p role="status">正在读取目录…</p>}
    {entries?.length === 0 && <p>暂无发现的生态条目。</p>}
    {entries?.map(entry => <article className="card" key={entry.id} aria-label={`${entry.name} ${entry.version}`}>
      <h3>{entry.name}</h3>
      {entry.description && <p>{entry.description}</p>}
      <p>{[kindLabels[entry.kind], entry.source, entry.version, entry.license,
        entry.package ? "已准入" : entry.retired ? "已下架" : reviewLabels[entry.reviewStatus]].filter(Boolean).join(" · ")}</p>
      {entry.package && entry.kind === "skill" &&
        <button type="button" disabled={!!busy} onClick={() => void choose(entry)}>{entry.enabled ? "卸载" : "加载到我的助手"}</button>}
      {!entry.package && entry.request === "pending" && <p>准入申请审核中</p>}
      {!entry.package && entry.request === "returned" && <p>准入申请已退回{entry.reply && ` · ${entry.reply}`}</p>}
      {!entry.package && !entry.retired && !entry.request && entry.reviewStatus !== "blocked" &&
        <button type="button" disabled={!!busy} onClick={() => void choose(entry)}>申请管理员准入</button>}
    </article>)}
  </section>;
}

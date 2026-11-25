import { useEffect, useState } from "react";
import { api } from "./client";

type Entry = {
  id: string;
  name: string;
  kind: "skill" | "mcp" | "agent";
  version: string;
  source: string;
  description: string;
  homepage?: string;
  reviewStatus: "clean" | "review_required" | "blocked";
  license?: string;
  licenseTag?: string;
  activity?: string;
  package?: string;
  retired: boolean;
  enabled: boolean;
  bindingRevision: number;
  request?: "pending" | "approved" | "returned";
  reply?: string;
};

type Scan = { id: string; name: string; status: keyof typeof scanLabels; finishedAt?: string };

const kindLabels = { skill: "Skill", mcp: "MCP 服务", agent: "外部 Agent" };
const reviewLabels = { clean: "体检通过", review_required: "待人工复核", blocked: "已阻断" };
const licenseLabels: Record<string, string> = { unknown: "许可证未知", commercial_restrictions: "含商业使用限制" };
const activityLabels: Record<string, string> = { recent: "近 3 个月更新", updated_this_year: "近一年更新", older_than_year: "超过一年未更新", unknown: "更新日期未知" };
const scanLabels = { pending: "正在配置", ready: "等待扫描", running: "扫描中", completed: "扫描完成", failed: "扫描失败", skipped: "等待下次扫描", paused: "已暂停" };

export function EcosystemMarket() {
  const [entries, setEntries] = useState<Entry[]>();
  const [scans, setScans] = useState<Scan[]>([]);
  const [scanError, setScanError] = useState("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  useEffect(() => {
    let active = true;
    api<Entry[]>("ecosystem/catalog").then(data => { if (active) setEntries(data); })
      .catch(error => { if (active) setError((error as Error).message); });
    const refreshScans = () => {
      api<Scan[]>("ecosystem/scan-status").then(data => {
        if (!active) return;
        setScanError("");
        setScans(data);
      }).catch(() => { if (active) setScanError("暂时无法读取扫描状态"); });
    };
    refreshScans();
    window.addEventListener("focus", refreshScans);
    return () => {
      active = false;
      window.removeEventListener("focus", refreshScans);
    };
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
    {scanError && <p role="status">{scanError}</p>}
    {scans.length > 0 && <section className="ecosystem-scan-status" aria-label="定期扫描状态">
      <ul>{scans.map(scan => <li key={scan.id}>
        <span>{scan.name} · {scanLabels[scan.status]}</span>
        {scan.finishedAt && <time dateTime={scan.finishedAt}>{new Date(scan.finishedAt).toLocaleString()}</time>}
      </li>)}</ul>
    </section>}
    {error && <p role="alert">{error}</p>}
    {notice && <p role="status">{notice}</p>}
    {!entries && !error && <p role="status">正在读取目录…</p>}
    {entries?.length === 0 && <p>暂无发现的生态条目。</p>}
    {entries?.map(entry => <article className="card" key={entry.id} aria-label={`${entry.name} ${entry.version}`}>
      <h3>{entry.name}</h3>
      {entry.description && <p>{entry.description}</p>}
      <p>{[kindLabels[entry.kind], entry.source, entry.version, entry.activity && activityLabels[entry.activity]].filter(Boolean).join(" · ")}</p>
      <p className="row ecosystem-tags">
        {entry.license && <span data-tone={entry.licenseTag === "review" ? "review_required" : entry.licenseTag === "permissive" ? "clean" : undefined}>
          {licenseLabels[entry.license] || entry.license}
        </span>}
        <span data-tone={entry.reviewStatus}>{reviewLabels[entry.reviewStatus]}</span>
        {entry.package && <span>已准入</span>}
        {entry.retired && <span>已下架</span>}
        {entry.homepage?.startsWith("https://") && <a href={entry.homepage} target="_blank" rel="noreferrer">查看上游来源</a>}
      </p>
      {entry.package && entry.kind === "skill" &&
        <button type="button" disabled={!!busy} onClick={() => void choose(entry)}>{entry.enabled ? "卸载" : "加载到我的助手"}</button>}
      {!entry.package && entry.request === "pending" && <p>准入申请审核中</p>}
      {!entry.package && entry.request === "returned" && <p>准入申请已退回{entry.reply && ` · ${entry.reply}`}</p>}
      {!entry.package && !entry.retired && !entry.request && entry.reviewStatus !== "blocked" &&
        <button type="button" disabled={!!busy} onClick={() => void choose(entry)}>申请管理员准入</button>}
    </article>)}
  </section>;
}

import { useEffect, useState } from "react";
import { api } from "./client";

type Candidate = {
  id: string;
  name: string;
  kind: "skill" | "mcp" | "agent";
  version: string;
  description: string;
  source: string;
  endpoint?: string;
  review: { status: "clean" | "review_required" | "blocked"; license: string; activity: string;
    findings: { code: string; file?: string }[] };
  reviewDigest: string;
  approved: boolean;
  requestedBy?: number;
};

type UpdateNotice = {
  id: string;
  catalog: string;
  name: string;
  source: string;
  priorVersion: string;
  version: string;
  summary: { contentChanged: boolean; descriptionChanged: boolean; metadataChanged: boolean; reviewStatus: string; license?: string };
};

const labels: Record<string, string> = {
  clean: "检查通过", review_required: "需要复核", blocked: "已阻断",
  license_review: "许可证需要人工复核", invalid_path: "不安全的文件路径",
  unapproved_destination: "不允许的目的地", unsafe_command: "高风险命令",
  plaintext_credential_request: "请求发送明文密钥", executable_file: "包含可执行文件",
  malformed_manifest: "清单格式无效", malformed_archive: "归档格式无效",
  symlink_or_special_file: "包含符号链接或特殊文件", outbound_instruction: "包含需要确认的外联指令",
  entry_size_limit: "文件超过解压限制", archive_size_limit: "归档超过大小限制",
  encrypted_archive_entry: "包含加密归档条目", unknown: "未知",
  commercial_restrictions: "含商业限制", recent: "最近 3 个月有更新",
  updated_this_year: "过去 12 个月有更新", older_than_year: "超过一年未更新",
};

const path = "admin/ecosystem/catalog";

export function EcosystemApprovals() {
  const [items, setItems] = useState<Candidate[]>([]);
  const [updates, setUpdates] = useState<UpdateNotice[]>([]);
  const [review, setReview] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState("");
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    Promise.all([api<Candidate[]>(path), api<UpdateNotice[]>("admin/ecosystem/updates")]).then(([candidates, notices]) => {
      if (!active) return;
      setItems(candidates);
      setUpdates(notices);
    })
      .catch(error => { if (active) setError((error as Error).message); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, []);

  async function decide(item: Candidate, decision: "approve" | "return-requests") {
    setBusy(item.id);
    setStatus("");
    setError("");
    try {
      await api(`${path}/${item.id}/${decision}`, "POST", { expected: item.reviewDigest, reason: (review[item.id] || "").trim() });
      setItems(current => current.map(row => row.id === item.id ? { ...row, approved: decision === "approve", requestedBy: 0 } : row));
      if (decision === "approve") setUpdates(current => current.filter(notice => notice.catalog !== item.id));
      setStatus(decision === "approve" ? `${item.name} 已准入并固化版本` : `${item.name} 的准入申请已退回`);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  async function acknowledge(notice: UpdateNotice) {
    setBusy(notice.id);
    setError("");
    setStatus("");
    try {
      await api(`admin/ecosystem/updates/${notice.id}/acknowledge`, "PUT");
      setUpdates(current => current.filter(row => row.id !== notice.id));
      setStatus(`${notice.name} 的版本提醒已读`);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy("");
    }
  }

  return (
    <section className="ecosystem-catalog" aria-label="生态准入">
      {loading && <p role="status">正在读取体检结果…</p>}
      {error && <p role="alert">{error}</p>}
      {status && <p role="status">{status}</p>}
      {updates.length > 0 && <section aria-label="上游版本提醒">
        <h2>上游版本提醒</h2>
        {updates.map(notice => <article className="card" key={notice.id} aria-label={`${notice.name} 版本提醒`}>
          <h3>{notice.name}</h3>
          <p>{notice.source} · {notice.priorVersion} → {notice.version}</p>
          <p>{[notice.summary.contentChanged && "Skill 文件已变化", notice.summary.descriptionChanged && "说明已更新",
            notice.summary.metadataChanged && "服务配置已更新", labels[notice.summary.reviewStatus],
            notice.summary.license && `许可证 ${labels[notice.summary.license] || notice.summary.license}`].filter(Boolean).join(" · ")}</p>
          <div className="row">
            {items.some(item => item.id === notice.catalog) && <a href={`#ecosystem-candidate-${notice.catalog}`}>查看新版准入</a>}
            <button type="button" disabled={!!busy} onClick={() => void acknowledge(notice)}>标为已读</button>
          </div>
        </article>)}
      </section>}
      {!loading && !error && !items.length && <p>暂无可复核的生态条目。</p>}
      {[...items].sort((first, second) => (second.requestedBy || 0) - (first.requestedBy || 0)).map(item => <article className="card" key={item.id} id={`ecosystem-candidate-${item.id}`} aria-label={item.name}>
        <h2>{item.name}</h2>
        <p>{item.source} · {item.version} · {item.approved ? "已准入" : labels[item.review.status]}</p>
        {!!item.requestedBy && <p>{item.requestedBy} 人申请准入</p>}
        <details>
          <summary>查看体检报告</summary>
          {item.description && <p>{item.description}</p>}
          <p>许可证 {labels[item.review.license] || item.review.license}</p>
          <p>维护情况 {labels[item.review.activity] || item.review.activity}</p>
          {!!item.review.findings?.length && <ul>{item.review.findings.map(({ code, file }, index) =>
            <li key={index}>{labels[code] || code}{file ? " · " + file : ""}</li>)}</ul>}
          {item.kind === "agent" && item.endpoint && <p>申请准入前，请在企业服务核对该域名的访问许可。<br /><code>{item.endpoint}</code></p>}
        </details>
        {!item.approved && <>
          {(item.review.status === "review_required" || !!item.requestedBy) && <label>人工复核意见<textarea maxLength={800}
            value={review[item.id] || ""} disabled={!!busy} onChange={event => setReview(value => ({ ...value, [item.id]: event.target.value }))} /></label>}
          {item.review.status !== "blocked" && <button className="primary" type="button" disabled={!!busy || item.review.status === "review_required" && !(review[item.id] || "").trim()}
            onClick={() => void decide(item, "approve")}>批准准入</button>}
          {!!item.requestedBy && <button type="button" disabled={!!busy || !(review[item.id] || "").trim()}
            onClick={() => void decide(item, "return-requests")}>退回申请</button>}
        </>}
      </article>)}
    </section>
  );
}

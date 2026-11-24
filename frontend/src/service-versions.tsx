import { useEffect, useRef, useState } from "react";
import { AggregationScope, type AggregationConfig } from "./aggregation-scope";
import { api, RequestError } from "./client";
import { outputOperations } from "./output-operations";

type VersionSummary = { version: number; name: string | null };
type ServiceHistory = {
  currentVersion: number;
  routingRevision: number;
  versions: (VersionSummary & { publishedAt: string })[];
  lastRollback: { from: number; to: number; reason: string; at: string; actor: string } | null;
};
type VersionContract = VersionSummary & {
  instructions: string;
  example: string;
  team: string;
  reviewer: string;
  allowsDocuments: boolean;
  requiresCSV: boolean;
  requiresFile: boolean;
  requiresPictures?: boolean;
  editsImage?: boolean;
  steps: {
    ordinal: number;
    name: string;
    revision: number;
    operation: string;
    phase: string;
    estimatedCny: string | null;
    aggregation: AggregationConfig | null;
    dependsOn: number[];
    alternative: { name: string; revision: number; aggregation: AggregationConfig | null } | null;
  }[];
};
type Preview = {
  currentVersion: number;
  routingRevision: number;
  current: VersionContract;
  target: VersionContract;
  blockers: { code: string; message: string }[];
};
const phaseLabels: Record<string, string> = { search: "原文检索", answer: "原文答复", answer_calc: "引文金额核算", review_evidence: "再次核对新依据", synthesize: "有新增原文时续答" };

function VersionDetails({ value, label }: { value: VersionContract; label: string }) {
  const allowed = value.requiresCSV ? "CSV" : value.allowsDocuments ? "TXT、Markdown、CSV、JSON、Word、Excel、PowerPoint、PDF" : "TXT、Markdown、CSV、JSON";
  return (
    <section className="service-version-contract" aria-label={label}>
      <h4>{label} · v{value.version}</h4>
      {value.name && <strong>{value.name}</strong>}
      <p className="preserve">{value.instructions}</p>
      {value.example && <p className="preserve">示例 · {value.example}</p>}
      <p>{value.requiresFile || value.requiresCSV ? "需附资料" : "可附资料"} · {allowed}</p>
      {value.requiresPictures && <p>{value.editsImage
        ? "需附一张 PNG 或 JPEG 参考图 · 编辑后输出 PNG"
        : "需附 1 至 4 张 PNG 或 JPEG 图片 · 视觉模型读取原件"}</p>}
      <p>最终确认人 · {value.reviewer}</p>
      <p>最多 {value.steps.length} 次调用 · {value.team}承担费用</p>
      <ol>
        {value.steps.map(step => (
          <li key={step.ordinal}>
            <strong>{step.name} · v{step.revision}</strong>
            <small>{phaseLabels[step.phase] || outputOperations[step.operation]?.split(" · ")[0] || step.operation}</small>
            <small>{step.dependsOn.length ? "引用步骤 " + step.dependsOn.join("、") : "可独立开始"}</small>
            {step.aggregation && <small><AggregationScope config={step.aggregation} /></small>}
            {step.alternative && <small>备选 · {step.alternative.name} · v{step.alternative.revision}
              {step.alternative.aggregation && <AggregationScope config={step.alternative.aggregation} />}
            </small>}
            <small>{step.estimatedCny === null ? "单次费用待核对" : "单次预估 ¥" + step.estimatedCny}</small>
          </li>
        ))}
      </ol>
    </section>
  );
}

export function ServiceVersions({
  id,
  routingRevision,
  refreshService,
}: {
  id: string;
  routingRevision?: number;
  refreshService: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [history, setHistory] = useState<ServiceHistory>();
  const [target, setTarget] = useState("");
  const [preview, setPreview] = useState<Preview>();
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [previewError, setPreviewError] = useState("");
  const [status, setStatus] = useState("");
  const [busy, setBusy] = useState(false);
  const [epoch, refresh] = useState(0);
  const replay = useRef<{ body: string; key: string } | undefined>(undefined);
  const path = "services/" + id + "/versions";
  useEffect(() => {
    if (!open) return;
    let active = true;
    setHistory(undefined);
    setError("");
    api<ServiceHistory>(path)
      .then(result => { if (active) setHistory(result); })
      .catch(e => { if (active) setError(e.message); });
    return () => { active = false; };
  }, [open, path, epoch, routingRevision]);
  useEffect(() => {
    setPreview(undefined);
    setPreviewError("");
    if (!open || !target || !history) return;
    let active = true;
    api<Preview>(path + "/" + target + "/preflight")
      .then(result => { if (active) setPreview(result); })
      .catch(e => { if (active) setPreviewError(e.message); });
    return () => { active = false; };
  }, [open, path, target, history]);
  const approved = preview && String(preview.target.version) === target &&
    preview.routingRevision === history?.routingRevision && preview.blockers.length === 0;
  return (
    <details onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>版本与回退</summary>
      {open && (
        <>
          {error && <p role="alert">{error}</p>}
          {status && <p role="status">{status}</p>}
          <button type="button" disabled={busy} onClick={() => {
            setHistory(undefined);
            setPreview(undefined);
            refresh(n => n + 1);
          }}>刷新版本</button>
          {!history && !error && <p role="status">正在读取版本…</p>}
          {history && (
            <>
              <p>当前默认 <strong>v{history.currentVersion}</strong></p>
              {history.lastRollback && (
                <p className="preserve">最近回退 · {history.lastRollback.actor} · v{history.lastRollback.from} → v{history.lastRollback.to}
                  <small>{new Date(history.lastRollback.at).toLocaleString("zh-CN")} · {history.lastRollback.reason}</small>
                </p>
              )}
              {history.versions.length > 1 ? (
                <label>
                  回退目标版本
                  <select value={target} disabled={busy} onChange={event => {
                    setTarget(event.target.value);
                    setReason("");
                    setStatus("");
                  }}>
                    <option value="">选择历史版本</option>
                    {history.versions.filter(item => item.version !== history.currentVersion).map(item => (
                      <option key={item.version} value={item.version}>
                        v{item.version}{item.name ? " · " + item.name : ""} · {new Date(item.publishedAt).toLocaleDateString("zh-CN")}
                      </option>
                    ))}
                  </select>
                </label>
              ) : <p>发布新版后可比较历史版本并回退。</p>}
              {previewError && <p role="alert">{previewError}</p>}
              {target && !preview && !previewError && <p role="status">正在核对目标版本…</p>}
              {preview && String(preview.target.version) === target && (
                <>
                  {preview.routingRevision !== history.routingRevision && <p role="alert">服务默认版本已变化，请刷新版本。</p>}
                  {preview.blockers.length > 0 && (
                    <ul aria-label="回退阻塞原因">
                      {preview.blockers.map(blocker => <li key={blocker.code + blocker.message}>{blocker.message}</li>)}
                    </ul>
                  )}
                  <div className="service-version-comparison">
                    <VersionDetails label="当前默认版本" value={preview.current} />
                    <VersionDetails label="回退目标版本" value={preview.target} />
                  </div>
                  {preview.target.name === null && <p>此历史版本的名称未记录，回退时沿用当前入口名称。</p>}
                  <p>新发起的网页和飞书任务会看到目标版本。外部 API 按调用方确认的版本受理。</p>
                  <form onSubmit={async event => {
                    event.preventDefault();
                    if (!approved || busy || !reason.trim()) return;
                    const input = {
                      expected: preview.currentVersion,
                      expectedRoutingRevision: preview.routingRevision,
                      target: preview.target.version,
                      reason: reason.trim(),
                    };
                    const body = JSON.stringify(input);
                    if (replay.current?.body !== body) replay.current = { body, key: crypto.randomUUID() };
                    setBusy(true);
                    setStatus("");
                    try {
                      const result = await api<{ version: number }>("services/" + id + "/rollback", "POST",
                        input, { "Idempotency-Key": replay.current.key });
                      setStatus("已记录默认版本回退至 v" + result.version);
                      setTarget("");
                      setReason("");
                      replay.current = undefined;
                      refresh(n => n + 1);
                      await refreshService();
                    } catch (e) {
                      setStatus((e as Error).message);
                      if (e instanceof RequestError && e.status === 409) refresh(n => n + 1);
                    } finally {
                      setBusy(false);
                    }
                  }}>
                    <label>回退原因
                      <textarea value={reason} onChange={event => setReason(event.target.value)} maxLength={800} required disabled={busy} />
                    </label>
                    <button disabled={!approved || busy || !reason.trim()}>确认回退至 v{preview.target.version}</button>
                  </form>
                </>
              )}
            </>
          )}
        </>
      )}
    </details>
  );
}

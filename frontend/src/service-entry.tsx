import { AggregationScope, type AggregationConfig } from "./aggregation-scope";
import { capabilityMatcher } from "./capability-match";
import { outputOperations } from "./output-operations";
import { useEffect, useState, type FormEvent } from "react";
import { Plus, X } from "lucide-react";
import { api, RequestError, statusLabel, type Project } from "./client";
import { encodeAttachment, isPictureName, pictureExtensions } from "./attachment-data";

export type PublishedService = {
  id: string;
  name: string;
  version: number;
  routingRevision?: number;
  brief: string;
  example: string;
  calls: number;
};
type Entry = Omit<PublishedService, "calls"> & {
  team: string;
  reviewer: string;
  ready: boolean;
  allowDocumentAttachments: boolean;
  requiresPictures?: boolean;
  editsImage?: boolean;
  example: string;
  steps: { toolChoice?: {name:string; revision:number;selector:number;primaryConfig:AggregationConfig;config:AggregationConfig} | null; lookupPhase?: string; name: string; revision: number; operation: string }[];
  recent: { id: string; version: number; stage: string; input: string }[];
};
export function serviceURL(id: string) {
  return "/?" + new URLSearchParams({ service: id });
}
export function ServiceRecommendations({
  project,
  goal,
}: {
  project: string;
  goal: string;
}) {
  const [services, setServices] = useState<PublishedService[]>([]);
  useEffect(() => {
    let live = true;
    api<PublishedService[]>("services")
      .then((rows) => {
        if (live) setServices(rows);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [project, goal]);
  const match = capabilityMatcher(goal);
  const matches = services
    .map(s => ({s, ...match(s.name, s.brief)}))
    .filter((x) => x.score > 0)
    .sort((a, b) => b.score - a.score || a.s.name.localeCompare(b.s.name))
    .slice(0, 3);
  if (!matches.length) return null;
  return (
    <details className="card">
      <summary>可复用的团队服务 · {matches.length}</summary>
      {matches.map(({ s, words }) => (
        <p key={s.id}>
          <a href={serviceURL(s.id) + "&from=" + encodeURIComponent(project)}>
            {s.name} · v{s.version}
          </a>
          <small>
            {s.brief} ·{" "}
            {words.length ? "匹配：" + words.join("、") : "名称与本次需求一致"}
          </small>
        </p>
      ))}
    </details>
  );
}
const textFormats = [".txt", ".md", ".csv", ".json"];
const documentFormats = [".docx", ".xlsx", ".pptx", ".pdf"];
const fileExtension = (name: string) => name.slice(name.lastIndexOf(".")).toLowerCase();

function validateFiles(files: File[], formats: string[]) {
  if (files.length > 20) return "一次最多附 20 份资料。";
  let textBytes = 0, originalBytes = 0, pictures = 0, pictureBytes = 0;
  for (const file of files) {
    const ext = fileExtension(file.name);
    if (!formats.includes(ext)) return `此服务不读取 ${file.name} 的文件格式。`;
    if (!file.size) return `请提供非空资料 ${file.name}。`;
    originalBytes += file.size;
    if (textFormats.includes(ext)) textBytes += file.size;
    if (isPictureName(file.name)) {
      if (file.size > 3 * 1024 * 1024) return `图片 ${file.name} 超过 3 MiB。`;
      pictureBytes += file.size;
      pictures++;
    }
  }
  if (pictures > 4 || pictureBytes > 6 * 1024 * 1024) return "一次最多附 4 张图片，合计最多 6 MiB。";
  if (originalBytes > 8 * 1024 * 1024) return "资料原件合计最多 8 MiB。";
  if (textBytes > 96 * 1024) return "文本文件合计最多 96 KiB。";
  return "";
}

export function ServiceEntry({ id, member }: { id: string; member: string }) {
  const [entry, setEntry] = useState<Entry | null>(null),
    [loadError, setLoadError] = useState(""),
    [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0),
    [input, setInput] = useState(""),
    [files, setFiles] = useState<File[]>([]),
    [awaitReceipt, setAwaitReceipt] = useState(true),
    [awaitLookup, setAwaitLookup] = useState(false),
    [consent, setConsent] = useState(false),
    [busy, setBusy] = useState(false);
  const [fromGoal, setFromGoal] = useState("");
  useEffect(() => {
    let live = true;
    setEntry(null);
    setConsent(false);
    setLoadError("");
    api<Entry>("services/" + encodeURIComponent(id) + "/entry")
      .then((data) => {
        if (live) setEntry(data);
      })
      .catch((e) => {
        if (live) setLoadError(e.message);
      });
    return () => {
      live = false;
    };
  }, [id, refresh]);
  useEffect(() => {
    let live = true;
    const from = new URLSearchParams(location.search).get("from");
    if (from)
      api<Project>("projects/" + encodeURIComponent(from))
        .then((p) => {
          if (live)
            setFromGoal(p.messages.find((m) => m.kind === "user")?.body || "");
        })
        .catch(() => {});
    return () => {
      live = false;
    };
  }, []);
  const needsCSV = !!entry?.steps.some(s => s.operation === "aggregate" && s.toolChoice);
  const allowsDocuments = !!entry?.allowDocumentAttachments && !needsCSV;
  const requiresPictures = !!entry?.requiresPictures;
  const formats = needsCSV ? [".csv"] : [
    ...textFormats,
    ...(allowsDocuments ? documentFormats : []),
    ...(requiresPictures ? pictureExtensions : []),
  ];
  const needsDocument = needsCSV || !!entry?.steps.some(s => s.lookupPhase === "search");
  const pictures = files.filter(file => isPictureName(file.name)).length;
  const missingFile = (needsDocument && !files.some(file => !isPictureName(file.name))) ||
    (requiresPictures && pictures === 0) || (!!entry?.editsImage && pictures !== 1);
  const canAwaitLookup = !!entry?.steps.some((s) => s.lookupPhase === "answer");
  const canAwaitReceipt = !!entry?.steps.some(s => s.lookupPhase === "answer_calc");
  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!consent || !entry?.ready || busy || !input.trim() || missingFile) return;
    const invalid = validateFiles(files, formats);
    if (invalid) {
      setError(invalid);
      return;
    }
    setBusy(true);
    setError("");
    try {
      const originals = await Promise.all(files.map(encodeAttachment));
      const body = {
        version: entry.version,
        input,
        authorizeCalls: entry.steps.length,
        awaitCalculationReceipt: canAwaitReceipt && awaitReceipt ? true : undefined,
        awaitLookupReceipt: canAwaitLookup && awaitLookup ? true : undefined,
        attachment: originals.length === 1 ? originals[0] : undefined,
        attachments: originals.length > 1 ? originals : undefined,
      };
      const digest = new Uint8Array(
        await crypto.subtle.digest(
          "SHA-256",
          new TextEncoder().encode(JSON.stringify(body)),
        ),
      );
      const slot = ["alliance.service", member, id, ...digest].join(".");
      const key = sessionStorage.getItem(slot) || crypto.randomUUID();
      sessionStorage.setItem(slot, key);
      const result = await api<{ project: string }>(
        "services/" + id + "/chat",
        "POST",
        body,
        { "Idempotency-Key": key },
      );
      sessionStorage.removeItem(slot);
      location.assign("/#" + result.project);
    } catch (err) {
      setError(
        err instanceof TypeError
          ? "未收到提交回执，可使用相同输入重试，或在此服务的最近任务中核对。"
          : (err as Error).message,
      );
      if (err instanceof RequestError) {
        if (err.status === 403 || err.status === 404 || err.status === 409) {
          setRefresh((n) => n + 1);
        } else if (err.status === 401) {
          location.reload();
        }
      }
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <header>
        <strong>Alliance</strong>
        <a href="/">返回工作区</a>
      </header>
      <main className="service-page">
        {error && <p role="alert">{error}</p>}
        {loadError ? (
          <>
            <p role="alert">{loadError}</p>
            <button onClick={() => setRefresh((n) => n + 1)}>
              重新读取服务
            </button>
          </>
        ) : !entry ? (
          <p role="status">正在读取服务…</p>
        ) : (
          <>
            <h1>{entry.name}</h1>
            <p>{entry.brief}</p>
            <small>
              {entry.team} · v{entry.version} · 最终确认人 {entry.reviewer}
            </small>
            <details>
              <summary>协作步骤 · {entry.steps.length}</summary>
              <ol>
                {entry.steps.map((s, i) => (
                  <li key={i}>
                    {s.name} v{s.revision}{s.lookupPhase === "search" ? " · 检索依据" : s.lookupPhase === "answer_calc" ? " · 引文金额核算" : s.lookupPhase === "answer" ? " · 据原文回答" : s.lookupPhase === "review_evidence" ? " · 再次核对新依据" : s.lookupPhase === "synthesize" ? " · 有新增原文时续答" : ""}
                    {s.toolChoice && (
                      <>
                        {s.operation === "aggregate" && <small><AggregationScope config={s.toolChoice.primaryConfig} /></small>}
                        <small>或由 Agent 选用 {s.toolChoice.name} v{s.toolChoice.revision}
                          {s.operation === "aggregate" && <> · <AggregationScope config={s.toolChoice.config} /></>}
                        </small>
                        {s.operation !== "aggregate" && <small>步骤 {s.toolChoice.selector} 负责选择</small>}
                      </>
                    )}
                    <small>
                      {outputOperations[s.operation] || "成果操作范围待核实"}
                    </small>
                  </li>
                ))}
              </ol>
            </details>
            <form className="service-form" onSubmit={submit}>
              {needsDocument && <small>{needsCSV ? "请附 CSV 资料，Agent 将从约定的两个汇总工具中选择一个。" : "请附用于查找依据的文字资料。"}</small>}
              {requiresPictures && <small>{entry.editsImage
                ? "请附一张 PNG 或 JPEG 参考图，最多 3 MiB。确认后，服务会把原件发送给图片编辑模型。"
                : "请附 1 至 4 张 PNG 或 JPEG。单张最多 3 MiB，合计最多 6 MiB。确认后，原件将发送给视觉理解模型。"}</small>}
              <div className="composer">
                <textarea
                  aria-label="向服务提出任务"
                  placeholder="描述这次要完成的工作…"
                  value={input}
                  required
                  disabled={busy}
                  onChange={(e) => {
                    setInput(e.target.value);
                    setConsent(false);
                  }}
                />
                <div className="composer-tools">
                  <label className="file-pick" title="附资料">
                    <Plus aria-hidden="true" />
                    <input
                      aria-label="附资料"
                      type="file"
                      multiple
                      accept={formats.join(",")}
                      disabled={busy}
                      onChange={(e) => {
                        const next = [...files, ...Array.from(e.target.files ?? [])];
                        e.target.value = "";
                        setConsent(false);
                        const invalid = validateFiles(next, formats);
                        if (invalid) {
                          setError(invalid);
                          return;
                        }
                        setFiles(next);
                        setError("");
                      }}
                    />
                  </label>
                  {files.length ? (
                    files.map((file, index) => (
                      <span className="attachment" key={`${file.name}-${index}`}>
                        <small>{file.name}</small>
                        <button
                          type="button"
                          disabled={busy}
                          aria-label={files.length === 1 ? "移除资料" : "移除资料 " + file.name}
                          onClick={() => {
                            setFiles(files.filter((_, position) => index !== position));
                            setConsent(false);
                            setError("");
                          }}
                        >
                          <X aria-hidden="true" />
                        </button>
                      </span>
                    ))
                  ) : (
                    <small>{needsCSV ? "CSV" : allowsDocuments ? "TXT、MD、CSV、JSON、DOCX、XLSX、PPTX、PDF" : "TXT、MD、CSV、JSON"}，最多 20 份</small>
                  )}
                </div>
              </div>
              <small>{allowsDocuments ? "原件合计最多 8 MiB，可读取文字合计最多 96 KiB。" : "文本文件合计最多 96 KiB。"}</small>
              <div className="actions">
                {entry.example && (
                  <button
                    disabled={busy}
                    type="button"
                    onClick={() => {
                      setInput(entry.example);
                      setConsent(false);
                    }}
                  >
                    使用发布者示例
                  </button>
                )}
                {fromGoal && (
                  <button
                    disabled={busy}
                    type="button"
                    onClick={() => {
                      setInput(fromGoal);
                      setConsent(false);
                    }}
                  >
                    带入原对话的首条需求
                  </button>
                )}
              </div>
              {entry.ready && canAwaitLookup && (
                <label>
                  <input
                    type="checkbox"
                    checked={awaitLookup}
                    disabled={busy}
                    onChange={(e) => {
                      setAwaitLookup(e.target.checked);
                      setConsent(false);
                    }}
                  />
                  找不到原文时在原任务等待我补件
                </label>
              )}
              {entry.ready && canAwaitReceipt && (
                <label>
                  <input type="checkbox" checked={awaitReceipt} disabled={busy} onChange={e=>{setAwaitReceipt(e.target.checked); setConsent(false)}}/>
                  金额不足时在原任务等待我补交原件
                </label>
              )}
              {entry.ready ? (
                <label>
                  <input
                    type="checkbox"
                    checked={consent}
                    disabled={busy}
                    onChange={(e) => setConsent(e.target.checked)}
                  />{" "}
                  授权本次最多 {entry.steps.length} 次调用，由{entry.team}
                  承担实际费用
                </label>
              ) : (
                <p role="status">
                  <strong>当前服务暂不可调用</strong> ·
                  请维护者核对助手、资源授权或审核人。
                </p>
              )}
              <button
                className="primary"
                disabled={!entry.ready || !input.trim() || !consent || busy || missingFile}
              >
                {busy ? "正在受理…" : "确认并启动对话"}
              </button>
            </form>
            {entry.recent.length > 0 && (
              <details>
                <summary>
                  我在此服务发起的任务 · 最近 {entry.recent.length} 项
                </summary>
                {entry.recent.map((x) => (
                  <p key={x.id}>
                    <a href={"/#" + x.id}>{x.input || entry.name}</a>
                    <small>
                      v{x.version} · {statusLabel[x.stage] || x.stage}
                    </small>
                  </p>
                ))}
              </details>
            )}
          </>
        )}
      </main>
    </>
  );
}

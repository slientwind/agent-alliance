import { useEffect, useState, type FormEvent } from "react";
import { api } from "./client";

const kinds = {
  mcp_registry: "MCP 官方目录",
  skills_git: "公开 Skill 仓库",
  a2a_directory: "A2A 认证目录",
  private_git: "企业私有仓库",
};

type Kind = keyof typeof kinds;
type Source = {
  id: string; name: string; kind: Kind; endpoint: string; config: { ref?: string; root?: string };
  cron: string; timeZone: string; enabled: boolean; revision: number; hasCredential: boolean;
  schedule: "pending" | "ready" | "retrying";
  lastScan?: { status: string; found: number; added: number; detail: string; startedAt: string; finishedAt?: string };
};

type Draft = Pick<Source, "id" | "name" | "kind" | "endpoint" | "config" | "cron" | "timeZone" | "enabled" | "revision"> & {
  credential: string;
  previousEndpoint?: string;
};

const initial = (): Draft => ({
  id: "", name: "", kind: "mcp_registry", endpoint: "https://registry.modelcontextprotocol.io/v0.1/servers",
  config: {}, cron: "0 7 * * *", timeZone: Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  enabled: false, revision: 0, credential: "",
});

const path = "admin/ecosystem/sources";
const statusLabel: Record<string, string> = { running: "扫描中", completed: "扫描完成", failed: "扫描失败", skipped: "已跳过" };

export function EcosystemSources() {
  const [sources, setSources] = useState<Source[]>([]);
  const [draft, setDraft] = useState<Draft>();
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const git = draft?.kind === "skills_git" || draft?.kind === "private_git";

  useEffect(() => {
    let active = true;
    api<Source[]>(path).then(items => { if (active) setSources(items); })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, []);

  async function run(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await action();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function reload() { setSources(await api<Source[]>(path)); }

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft) return;
    void run(async () => {
      await api(draft.id ? path + "/" + draft.id : path, draft.id ? "PUT" : "POST", {
        name: draft.name.trim(), kind: draft.kind, endpoint: draft.endpoint.trim(),
        config: git ? draft.config : {}, cron: draft.cron, timeZone: draft.timeZone, enabled: draft.enabled, expected: draft.revision,
        credential: draft.credential, keepCredential: draft.kind === "private_git" && !draft.credential &&
          draft.previousEndpoint === draft.endpoint.trim(),
      });
      setDraft(undefined);
      await reload();
      setNotice("扫描设置已保存。");
    });
  }

  return (
    <section aria-label="生态扫描源" className="card">
      <h2>生态扫描源</h2>
      {error && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
      <div className="row">
        <button type="button" disabled={busy || !!draft} onClick={() => setDraft(initial())}>添加扫描源</button>
        <button type="button" disabled={busy} onClick={() => void run(reload)}>刷新扫描状态</button>
      </div>
      {draft && <form onSubmit={save} className="service-form">
        <label>扫描源名称<input value={draft.name} required maxLength={120} disabled={busy}
          onChange={event => setDraft({ ...draft, name: event.target.value })} /></label>
        <label>来源类型<select value={draft.kind} disabled={busy} onChange={event => {
          const kind = event.target.value as Kind;
          setDraft({ ...draft, kind, config: {}, credential: "", previousEndpoint: undefined,
            endpoint: kind === "mcp_registry" ? initial().endpoint : "" });
        }}>
          {Object.entries(kinds).map(([id, label]) => <option key={id} value={id}>{label}</option>)}
        </select></label>
        <label>{git ? "仓库 API 地址" : "目录 HTTPS 地址"}<input type="url" value={draft.endpoint} required maxLength={2048} disabled={busy}
          placeholder={git ? "https://api.github.com/repos/组织/仓库" : "https://registry.example.com/agents"}
          onChange={event => setDraft({ ...draft, endpoint: event.target.value })} /></label>
        {git && <>
          <label>分支或标签<input value={draft.config.ref || ""} disabled={busy} placeholder="默认分支"
            onChange={event => setDraft({ ...draft, config: { ...draft.config, ref: event.target.value } })} /></label>
          <label>Skill 目录（可选）<input value={draft.config.root || ""} disabled={busy} placeholder="skills"
            onChange={event => setDraft({ ...draft, config: { ...draft.config, root: event.target.value } })} /></label>
        </>}
        {draft.kind === "private_git" && (
          <label>仓库访问凭据<input type="password" value={draft.credential} autoComplete="new-password" disabled={busy}
            placeholder={draft.previousEndpoint === draft.endpoint.trim() ? "留空沿用原凭据" : "填写私有仓库的读取凭据"}
            onChange={event => setDraft({ ...draft, credential: event.target.value })} /></label>
        )}
        <label>扫描时间表达式<input value={draft.cron} required maxLength={120} disabled={busy} placeholder="0 7 * * *"
          onChange={event => setDraft({ ...draft, cron: event.target.value })} /></label>
        <label>时区<input value={draft.timeZone} required disabled={busy}
          onChange={event => setDraft({ ...draft, timeZone: event.target.value })} /></label>
        <label className="row"><input type="checkbox" checked={draft.enabled} disabled={busy}
          onChange={event => setDraft({ ...draft, enabled: event.target.checked })} /> 启用定时扫描</label>
        {draft.enabled && <small>扫描只访问已获得企业许可的主机。</small>}
        <div className="row">
          <button type="submit" className="primary" disabled={busy}>保存扫描源</button>
          <button type="button" disabled={busy} onClick={() => setDraft(undefined)}>取消</button>
        </div>
      </form>}
      {sources.map(source => <article className="card" key={source.id} aria-label={source.name}>
        <h3>{source.name}</h3>
        <p>{kinds[source.kind]} · {source.enabled ? "已启用" : "已暂停"} · {source.cron} · {source.timeZone}</p>
        {source.schedule !== "ready" && <p>定时设置等待同步。</p>}
        {source.lastScan && (
          <p>{statusLabel[source.lastScan.status] || source.lastScan.status}
            {source.lastScan.status === "completed" && ` · 找到 ${source.lastScan.found} 项，新增 ${source.lastScan.added} 项`}
            {source.lastScan.detail && ` · ${source.lastScan.detail}`}</p>
        )}
        <div className="row">
          <button type="button" disabled={busy || !!draft} onClick={() => setDraft({
            ...source, credential: "", previousEndpoint: source.hasCredential ? source.endpoint : undefined,
          })}>编辑</button>
          <button type="button" disabled={busy || !source.enabled} onClick={() => void run(async () => {
            await api(`${path}/${source.id}/scan`, "POST", { expected: source.revision }, { "Idempotency-Key": crypto.randomUUID() });
            setNotice("已提交扫描，可刷新查看进度。");
          })}>立即扫描</button>
        </div>
      </article>)}
    </section>
  );
}

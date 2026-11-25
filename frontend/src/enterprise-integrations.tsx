import { useEffect, useState, type FormEvent } from "react";
import { api } from "./client";
import { MCPDirectory } from "./mcp-directory";

const integrations = "admin/integrations";

export function EnterpriseIntegrations() {
  return <><SandboxPool /><KnowledgeConnections /><MCPDirectory /><BotSettings /></>;
}

type Pool = { maxJobs: number; timeoutSeconds: number; revision: number; active: number };

function SandboxPool() {
  const path = integrations + "/sandbox";
  const [pool, setPool] = useState<Pool>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  useEffect(() => {
    let active = true;
    api<Pool>(path).then(value => { if (active) setPool(value); })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, [path]);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!pool) return;
    const form = new FormData(event.currentTarget);
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await api(path, "PUT", {
        expected: pool.revision, maxJobs: Number(form.get("jobs")), timeoutSeconds: Number(form.get("timeout")),
      });
      setPool(await api<Pool>(path));
      setStatus("沙箱配额已保存");
    } catch (error) {
      setError((error as Error).message);
    } finally { setBusy(false); }
  }

  return <section className="card" aria-label="沙箱配额">
    <h2>沙箱配额</h2>
    {error && <p role="alert">{error}</p>}
    {status && <p role="status">{status}</p>}
    {pool && <form key={pool.revision} onSubmit={save}>
      <p>运行中 {pool.active} 个任务。并发设为 0 后暂停接收新任务。</p>
      <label>最大并发<input required type="number" min="0" max="32" name="jobs" defaultValue={pool.maxJobs} disabled={busy} /></label>
      <label>任务时限（秒）<input required type="number" min="5" max="60" name="timeout" defaultValue={pool.timeoutSeconds} disabled={busy} /></label>
      <button className="primary" disabled={busy}>保存沙箱配额</button>
    </form>}
  </section>;
}

const documentProviders = { feishu: "飞书文档", dingtalk: "钉钉在线文档", confluence: "Confluence Cloud" };
type DocumentProvider = keyof typeof documentProviders;
type Knowledge = {
  id: string; provider: DocumentProvider; name: string; project: string; projectName?: string;
  settings: { document: string; account: string; origin: string };
  enabled: boolean; hasCredential: boolean; revision: number;
  state: string; verdict: string; fetchedAt?: string; version?: number;
};
type Target = { id: string; title: string };
const knowledgePath = integrations + "/knowledge";
const syncState: Record<string, string> = { pending: "等待同步", syncing: "正在同步", ready: "已同步", unavailable: "同步失败", disabled: "已暂停" };
const syncVerdict: Record<string, string> = {
  access: "上游访问已失效", credential: "凭据不可用", owner: "项目负责人的管理权限已变更",
  format: "文档内容不可读取或超过 256 KiB", egress: "请检查域名访问许可", remote: "上游暂不可用",
  local: "项目资料已被更新或撤回",
};

function KnowledgeConnections() {
  const [rows, setRows] = useState<Knowledge[]>([]);
  const [targets, setTargets] = useState<Target[]>([]);
  const [draft, setDraft] = useState<Knowledge>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  const syncing = rows.some(row => row.state === "pending" || row.state === "syncing");
  useEffect(() => {
    let active = true;
    Promise.all([api<Knowledge[]>(knowledgePath), api<Target[]>(knowledgePath + "/targets")])
      .then(([sources, projects]) => { if (active) { setRows(sources); setTargets(projects); } })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, []);
  useEffect(() => {
    if (!syncing || busy) return;
    let active = true;
    const poll = window.setInterval(() => {
      api<Knowledge[]>(knowledgePath).then(value => { if (active) setRows(value); })
        .catch(error => { if (active) setError((error as Error).message); });
    }, 4000);
    return () => { active = false; window.clearInterval(poll); };
  }, [busy, syncing]);

  function empty() {
    setDraft({
      id: crypto.randomUUID(), provider: "feishu", name: "", project: targets[0]?.id ?? "",
      settings: { document: "", account: "", origin: "" }, enabled: true, hasCredential: false, revision: 0,
      state: "pending", verdict: "",
    });
  }

  async function update(action: () => Promise<unknown>, message: string, close = false) {
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await action();
      setRows(await api<Knowledge[]>(knowledgePath));
      if (close) setDraft(undefined);
      setStatus(message);
    } catch (error) {
      setError((error as Error).message);
    } finally { setBusy(false); }
  }

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft) return;
    const form = new FormData(event.currentTarget);
    const clear = form.has("clearSecret");
    const secret = clear ? "" : String(form.get("secret") ?? "").trim() || undefined;
    const { id, name, provider, project, settings, enabled, revision } = draft;
    void update(() => api(knowledgePath + "/" + id, "PUT", { name, provider, project, settings, enabled, secret, expected: revision }),
      "知识源已保存", true);
  }

  return <section className="card" aria-label="企业知识源">
    <h2>企业知识源</h2>
    {error && <p role="alert">{error}</p>}
    {status && <p role="status">{status}</p>}
    <ul>{rows.map(row => <li key={row.id}>
      <strong>{row.name}</strong> · {documentProviders[row.provider]} · {row.projectName} · {syncState[row.state]}
      {row.verdict && syncVerdict[row.verdict] ? " · " + syncVerdict[row.verdict] : ""}
      {row.version ? " · 资料版本 " + row.version : ""}
      {row.fetchedAt && <small> · 最近成功 {new Date(row.fetchedAt).toLocaleString()}</small>}
      <div className="row">
        <button type="button" disabled={busy} onClick={() => setDraft(row)}>编辑 {row.name}</button>
        <button type="button" disabled={busy || !row.enabled || row.state === "syncing"}
          onClick={() => void update(() => api(knowledgePath + "/" + row.id + "/sync", "POST"), "同步已排队")}>同步 {row.name}</button>
      </div>
    </li>)}</ul>
    {!draft && <div className="row">
      <button type="button" disabled={busy || targets.length === 0} onClick={empty}>添加知识源</button>
      <button type="button" disabled={busy} onClick={() => void update(async () => {
        setTargets(await api<Target[]>(knowledgePath + "/targets"));
      }, "同步状态已刷新")}>刷新同步状态</button>
    </div>}
    {targets.length === 0 && !draft && <p>由你负责的项目可作为同步目标。</p>}
    {draft && <form key={draft.id + draft.provider} onSubmit={save}>
      <fieldset disabled={busy}>
        <label>知识源名称<input required maxLength={65} value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} /></label>
        <label>来源<select aria-label="来源" disabled={draft.revision > 0} value={draft.provider} onChange={event => setDraft({
          ...draft, provider: event.target.value as DocumentProvider, settings: { document: "", account: "", origin: "" },
        })}>{Object.entries(documentProviders).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <label>同步到项目<select required disabled={draft.revision > 0} value={draft.project}
          onChange={event => setDraft({ ...draft, project: event.target.value })}>
          {draft.revision > 0 && !targets.some(target => target.id === draft.project) && <option value={draft.project}>{draft.projectName}</option>}
          {targets.map(target => <option key={target.id} value={target.id}>{target.title}</option>)}
        </select></label>
        <p>同步的正文会成为项目资料，对有权查看项目资料的成员可见。</p>
        <label>{draft.provider === "confluence" ? "页面 ID" : "文档 ID"}
          <input required disabled={draft.revision > 0} value={draft.settings.document}
            onChange={event => setDraft({ ...draft, settings: { ...draft.settings, document: event.target.value } })} /></label>
        {draft.provider === "confluence" && <label>Confluence 站点<input type="url" required disabled={draft.revision > 0}
          value={draft.settings.origin} onChange={event => setDraft({ ...draft, settings: { ...draft.settings, origin: event.target.value } })} /></label>}
        {draft.provider !== "dingtalk" && <label>{draft.provider === "feishu" ? "飞书 App ID" : "Confluence 账号邮箱"}
          <input type={draft.provider === "feishu" ? "text" : "email"} required disabled={draft.revision > 0}
            value={draft.settings.account} onChange={event => setDraft({ ...draft, settings: { ...draft.settings, account: event.target.value } })} /></label>}
        <label>{{ feishu: "飞书 App Secret", dingtalk: "钉钉文档 MCP 含密钥地址", confluence: "Confluence API Token" }[draft.provider]}
          <input name="secret" type="password" autoComplete="new-password" placeholder={draft.hasCredential ? "留空保留已保存凭据" : ""} /></label>
        {draft.hasCredential && <label><input type="checkbox" name="clearSecret" /> 清除凭据</label>}
        <label><input type="checkbox" checked={draft.enabled} onChange={event => setDraft({ ...draft, enabled: event.target.checked })} /> 启用同步</label>
        {!draft.enabled && draft.revision > 0 && <p>保存后会撤回从该连接发布的项目资料。</p>}
        <div className="row">
          <button className="primary">保存知识源</button>
          <button type="button" onClick={() => setDraft(undefined)}>取消</button>
        </div>
      </fieldset>
    </form>}
  </section>;
}

const botProviders = { feishu: "飞书", slack: "Slack", dingtalk: "钉钉" };
type Bot = {
  provider: keyof typeof botProviders; enabled: boolean; revision: number; hasCredentials: boolean;
  fields: { app: string; tenant: string; bot: string };
};

function BotSettings() {
  const path = integrations + "/bots";
  const [rows, setRows] = useState<Bot[]>([]);
  const [draft, setDraft] = useState<Bot>();
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    api<Bot[]>(path).then(value => { if (active) setRows(value); })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, [path]);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft) return;
    const form = new FormData(event.currentTarget);
    const credentials = {
      secret: String(form.get("secret") ?? "").trim(), encrypt: String(form.get("encrypt") ?? "").trim(), verify: String(form.get("verify") ?? "").trim(),
    };
    if (form.has("clearCredentials")) Object.assign(credentials, { secret: "", encrypt: "", verify: "" });
    const { provider, enabled, fields, revision } = draft;
    setBusy(true);
    setStatus("");
    setError("");
    try {
      await api(path + "/" + provider, "PUT", {
        enabled, fields, expected: revision,
        credentials: form.has("clearCredentials") || Object.values(credentials).some(Boolean) ? credentials : undefined,
      });
      setRows(await api<Bot[]>(path));
      setDraft(undefined);
      setStatus("机器人配置已保存");
    } catch (error) {
      setError((error as Error).message);
    } finally { setBusy(false); }
  }

  return <section className="card" aria-label="IM 渠道机器人">
    <h2>IM 渠道机器人</h2>
    {status && <p role="status">{status}</p>}
    {error && <p role="alert">{error}</p>}
    <ul>{rows.map(row => <li key={row.provider}>
      <strong>{botProviders[row.provider]}</strong> · {row.enabled ? "已启用" : "已停用"}
      {row.revision ? " · 数据库配置" : row.enabled ? " · 部署配置" : ""}
      <button type="button" disabled={busy} onClick={() => setDraft(row)}>编辑 {botProviders[row.provider]}机器人</button>
    </li>)}</ul>
    {draft && <form key={draft.provider} onSubmit={save}>
      <fieldset disabled={busy}>
        <legend>{botProviders[draft.provider]}机器人</legend>
        <label>{draft.provider === "dingtalk" ? "企业 Corp ID" : draft.provider === "slack" ? "工作区 ID" : "租户 Key"}
          <input value={draft.fields.tenant} onChange={event => setDraft({ ...draft, fields: { ...draft.fields, tenant: event.target.value } })} /></label>
        <label>{draft.provider === "dingtalk" ? "Client ID" : "App ID"}
          <input value={draft.fields.app} onChange={event => setDraft({ ...draft, fields: { ...draft.fields, app: event.target.value } })} /></label>
        {draft.provider !== "slack" && <label>{draft.provider === "feishu" ? "机器人 Open ID（选填）" : "Robot Code（留空使用 Client ID）"}
          <input value={draft.fields.bot} onChange={event => setDraft({ ...draft, fields: { ...draft.fields, bot: event.target.value } })} /></label>}
        {draft.hasCredentials && <p>{draft.revision ? "全部留空可保留数据库凭据。更换凭据时请填写该渠道的全部凭据。" : "迁移部署配置时，请重新填写该渠道的凭据。"}</p>}
        <label>{{ feishu: "App Secret", slack: "Signing Secret", dingtalk: "Client Secret" }[draft.provider]}
          <input name="secret" type="password" autoComplete="new-password" /></label>
        {draft.provider === "feishu" && <label>Encrypt Key<input name="encrypt" type="password" autoComplete="new-password" /></label>}
        {draft.provider !== "dingtalk" && <label>{draft.provider === "feishu" ? "Verification Token" : "Bot Token"}
          <input name="verify" type="password" autoComplete="new-password" /></label>}
        {draft.revision > 0 && draft.hasCredentials && <label><input type="checkbox" name="clearCredentials" /> 清除数据库凭据</label>}
        <label><input type="checkbox" checked={draft.enabled} onChange={event => setDraft({ ...draft, enabled: event.target.checked })} /> 启用机器人</label>
        <div className="row">
          <button className="primary">保存机器人配置</button>
          <button type="button" onClick={() => setDraft(undefined)}>取消</button>
        </div>
      </fieldset>
    </form>}
  </section>;
}

import { useEffect, useState, type FormEvent } from "react";
import { Database, Plug } from "lucide-react";
import { api, RequestError } from "./client";
import type { KnowledgeScope } from "./knowledge-workbench";

type Operation = { name: string; description: string; risk: string };
type Connector = { id: string; spaceId: string; provider: string; driver: string; category: string; name: string; endpoint?: string;
  authMode: string; health: string; enabled: boolean; revision: number; operations: Operation[]; settings?: Record<string, unknown> };
type Field = { label: string; type: string; source?: string };
type Metric = { label: string; formula: string };
type View = { id: string; spaceId: string; connectorId: string; name: string; description: string; dimensions: Record<string, Field>;
  metrics: Record<string, Metric>; relationships: { dimension: string; relatedView: string; relatedDimension: string }[];
  timeDimension?: string; relation?: string; operation?: string; sensitivity: string; revision: number; enabled: boolean;
  defaultFilters?: Record<string, unknown>; rls?: Record<string, string> };
type OBOStatus = { bound: boolean; authorized: boolean; expiresAt?: string };
const vendors: Record<string, { label: string; driver: string; category: string }> = {
  kingdee: { label: "金蝶云星空", driver: "saas_pack", category: "erp" }, yonyou: { label: "用友 BIP", driver: "saas_pack", category: "erp" },
  fxiaoke: { label: "纷享销客", driver: "saas_pack", category: "crm" }, salesforce: { label: "Salesforce", driver: "saas_pack", category: "crm" },
  feishu_crm: { label: "飞书客户", driver: "saas_pack", category: "crm" }, douyin: { label: "抖店", driver: "saas_pack", category: "commerce" },
  qianniu: { label: "千牛", driver: "saas_pack", category: "commerce" }, jushuitan: { label: "聚水潭", driver: "saas_pack", category: "commerce" },
  mysql: { label: "MySQL", driver: "readonly_sql", category: "warehouse" }, postgresql: { label: "PostgreSQL", driver: "readonly_sql", category: "warehouse" },
  clickhouse: { label: "ClickHouse", driver: "readonly_sql", category: "warehouse" }, starrocks: { label: "StarRocks", driver: "readonly_sql", category: "warehouse" },
  openapi: { label: "OpenAPI / Swagger", driver: "openapi_rest", category: "internal" },
  chromium: { label: "内网只读表格", driver: "browser_rpa_readonly", category: "internal" },
};
const health: Record<string, string> = { ready: "连接正常", untested: "待检测", unavailable: "连接异常" };

export function BusinessWorkspace({ space }: KnowledgeScope) {
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [views, setViews] = useState<View[]>([]);
  const [editConnector, setEditConnector] = useState<Partial<Connector>>();
  const [editView, setEditView] = useState<Partial<View>>();
  const [version, setVersion] = useState(0);
  const [error, setError] = useState("");
  const admin = space.role === "space_admin" && space.kind === "team";
  const base = `spaces/${space.id}/business/`;

  useEffect(() => {
    let active = true;
    Promise.all([api<Connector[]>(base + "connectors"), api<View[]>(base + "views")])
      .then(([sources, definitions]) => { if (active) { setConnectors(sources); setViews(definitions); } })
      .catch(error => { if (active) setError(error.message); });
    return () => { active = false; };
  }, [base, version]);

  async function openView(view: View) {
    try {
      const list = await api<View[]>(base + "views?manage=true");
      setEditView(list.find(item => item.id === view.id));
    } catch (error) {
      setError((error as Error).message);
    }
  }

  return <div className="knowledge-stack">
    {error && <p role="alert">{error}</p>}
    <div className="knowledge-toolbar"><h2>业务系统</h2>
      {admin && <button type="button" onClick={() => setEditConnector({ id: crypto.randomUUID(), enabled: true })}>新增业务连接器</button>}
    </div>
    {connectors.length === 0 && <p>当前空间没有业务系统连接器。</p>}
    <div className="knowledge-grid">
      {connectors.map(item => <ConnectorCard key={item.id + "/" + item.revision} base={base} connector={item}
        views={views.filter(view => view.connectorId === item.id)} admin={admin} space={space.id} edit={() => setEditConnector(item)} />)}
    </div>
    {editConnector && <ConnectionEditor base={base} key={editConnector.id} connector={editConnector}
      close={() => setEditConnector(undefined)} saved={() => { setEditConnector(undefined); setVersion(value => value + 1); }} />}
    <section className="card knowledge-panel" aria-label="受控业务语义视图">
      <div className="knowledge-toolbar"><h2>业务语义视图</h2>
        {admin && connectors.length > 0 && <button type="button" onClick={() => setEditView({ id: crypto.randomUUID(), connectorId: connectors[0].id })}>新增数据视图</button>}
      </div>
      {views.length === 0 && <p>当前空间没有已发布的数据视图。</p>}
      <ul className="knowledge-entries">{views.map(view => <li key={view.id}>
        <details><summary>{view.name}{view.spaceId !== space.id && " · 全企公共底座"}</summary>
          <p>{view.description}</p>
          <dl>
            <dt>业务维度</dt><dd>{Object.values(view.dimensions).map(field => field.label).join("、")}</dd>
            <dt>核定指标</dt><dd>{Object.entries(view.metrics).map(([key, metric]) => <div key={key}>{metric.label} <code>{metric.formula}</code></div>)}</dd>
            {!!view.relationships?.length && <><dt>关联维度</dt><dd>{view.relationships.map(relation => `${view.dimensions[relation.dimension]?.label || relation.dimension} → ${views.find(item => item.id === relation.relatedView)?.name || "关联视图"}`).join("、")}</dd></>}
          </dl>
          {admin && view.spaceId === space.id && <button type="button" onClick={() => void openView(view)}>编辑语义视图</button>}
        </details>
      </li>)}</ul>
    </section>
    {editView && <ViewEditor base={base} key={editView.id} view={editView} connectors={connectors}
      close={() => setEditView(undefined)} saved={() => { setEditView(undefined); setVersion(value => value + 1); }} />}
  </div>;
}

function ConnectorCard({ base, connector, views, admin, space, edit }: {
  base: string; connector: Connector; views: View[]; admin: boolean; space: string; edit: () => void;
}) {
  const [viewID, setViewID] = useState("");
  const [obo, setOBO] = useState<OBOStatus>();
  const [summary, setSummary] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [binding, setBinding] = useState(false);
  const view = views.find(item => item.id === viewID) || views[0];
  const path = base + `connectors/${connector.id}`;
  const employee = connector.authMode === "obo";

  async function test() {
    setError(""); setSummary(""); setBusy(true);
    try {
      if (employee) {
        const state = await api<OBOStatus>(path + "/obo");
        setOBO(state);
        if (!state.authorized) {
          setSummary(state.bound ? "请为当前员工完成 OAuth 授权。" : "请先由空间管理员绑定当前员工的业务身份。");
          return;
        }
      }
      if (!view) {
        setSummary("需要先为此连接器发布可测试的语义视图。");
        return;
      }
      const dimension = Object.keys(view.dimensions)[0];
      const metric = Object.keys(view.metrics)[0];
      const result = await api<{ rowCount: number; provenance: { access: string } }>(base + `views/${view.id}/test`, "POST",
        dimension ? { dimensions: [dimension] } : { metrics: [metric] });
      setSummary(`连通 · ${result.rowCount} 行 · ${result.provenance.access}`);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function authorize(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    setBusy(true); setError("");
    try {
      await api(path + "/obo", "POST", { subjectToken: new FormData(form).get("token") });
      form.reset();
      const state = await api<OBOStatus>(path + "/obo");
      setOBO(state);
      setSummary("员工授权已生效。");
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <article className="card knowledge-card">
    <h3><Plug size={16} aria-hidden="true" /> {connector.name}</h3>
    <div className="knowledge-badges">
      <span className="knowledge-badge">{vendors[connector.provider]?.label || connector.provider}</span>
      <span className="knowledge-badge" data-state={connector.health}>{connector.enabled ? health[connector.health] || connector.health : "已停用"}</span>
    </div>
    <p>{employee ? "OBO 员工及当前部门行权限" : "空间授权只读"}</p>
    <small>读 L0 · 系统写回须经 L2 预览确认</small>
    {views.length > 1 && <label>测试视图<select value={view?.id || ""} onChange={event => setViewID(event.target.value)}>
      {views.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>}
    <div className="knowledge-actions">
      <button type="button" disabled={busy || !connector.enabled} onClick={() => void test()}>{employee ? "测试行权限连通" : "测试连通"}</button>
      {admin && <button type="button" disabled={busy} onClick={edit}>连接器设置</button>}
      {admin && employee && <button type="button" aria-expanded={binding} disabled={busy} onClick={() => setBinding(value => !value)}>员工身份绑定</button>}
    </div>
    {summary && <p role="status">{summary}</p>}{error && <p role="alert">{error}</p>}
    {employee && obo?.bound && !obo.authorized && <form className="knowledge-form" onSubmit={authorize}>
      <label>员工 OAuth 凭据<input type="password" name="token" autoComplete="off" required /></label>
      <button type="submit" disabled={busy}>授权当前员工</button>
    </form>}
    {binding && <EmployeeBinding space={space} path={path} />}
  </article>;
}

function EmployeeBinding({ space, path }: { space: string; path: string }) {
  type Profile = { issuerSubject: string; employeeNo: string; departmentCode: string; revision: number };
  const [roster, setRoster] = useState<{ id: string; name: string }[]>([]);
  const [member, setMember] = useState("");
  const [profile, setProfile] = useState<Profile>();
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    let active = true;
    api<typeof roster>(`spaces/${space}/members`).then(items => { if (active) setRoster(items); })
      .catch(error => { if (active) setError(error.message); });
    return () => { active = false; };
  }, [space]);
  useEffect(() => {
    if (!member) return;
    let active = true;
    api<Profile>(path + "/obo-identities/" + member).then(value => { if (active) setProfile(value); })
      .catch(error => {
        if (active) {
          if (error instanceof RequestError && error.status === 404) setProfile({ issuerSubject: "", employeeNo: "", departmentCode: "", revision: 0 });
          else setError(error.message);
        }
      });
    return () => { active = false; };
  }, [member, path]);
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!profile) return;
    const data = new FormData(event.currentTarget);
    const update = { issuerSubject: String(data.get("subject") || ""), employeeNo: String(data.get("employee") || ""),
      departmentCode: String(data.get("department") || ""), expectedVersion: profile.revision };
    setError(""); setMessage(""); setLoading(true);
    try {
      const result = await api<{ revision: number }>(path + "/obo-identities/" + member, "PUT", update);
      setProfile({ ...update, revision: result.revision });
      setMessage("员工身份已绑定。");
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setLoading(false);
    }
  }
  return <div className="knowledge-form">
    <label>空间员工<select value={member} onChange={event => { setMember(event.target.value); setProfile(undefined); setError(""); setMessage(""); }}>
      <option value="">选择员工</option>{roster.map(person => <option key={person.id} value={person.id}>{person.name}</option>)}
    </select></label>
    {profile && <form className="knowledge-form" onSubmit={save} key={member + "/" + profile.revision}>
      <label>授权主体<input name="subject" defaultValue={profile.issuerSubject} required /></label>
      <label>业务员工号<input name="employee" defaultValue={profile.employeeNo} required /></label>
      <label>业务部门编号<input name="department" defaultValue={profile.departmentCode} required /></label>
      <button type="submit" disabled={loading}>保存员工身份绑定</button>
    </form>}
    {message && <p role="status">{message}</p>}{error && <p role="alert">{error}</p>}
  </div>;
}

function objectField(value: FormDataEntryValue | null, fallback: Record<string, unknown> = {}) {
  if (!value || !String(value).trim()) return fallback;
  const parsed = JSON.parse(String(value));
  if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") throw new Error("请填写 JSON 对象。");
  return parsed as Record<string, unknown>;
}

function ConnectionEditor({ base, connector, close, saved }: { base: string; connector: Partial<Connector>; close: () => void; saved: () => void }) {
  const [provider, setProvider] = useState(connector.provider || "kingdee");
  const [ops, setOps] = useState(connector.operations || []);
  const [file, setFile] = useState<File>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const profile = vendors[provider];
  async function importFile() {
    if (!file) return;
    setBusy(true); setError("");
    try {
      const response = await fetch("/api/" + base + "openapi/import", { method: "POST", body: file, credentials: "same-origin" });
      const info = await response.json().catch(() => ({}));
      if (!response.ok) throw new RequestError(response.status, info.error || "接口规范导入失败");
      setOps(info.operations);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setError(""); setBusy(true);
    try {
      await api(base + "connectors/" + connector.id, "PUT", { provider, driver: profile.driver, category: connector.category || profile.category,
        name: data.get("name"), endpoint: data.get("endpoint"), authMode: data.get("auth"), enabled: data.has("enabled"),
        expectedVersion: connector.revision || 0, settings: objectField(data.get("settings")), operations: ops,
        ...(String(data.get("credential") || "").trim() ? { credential: objectField(data.get("credential")) } : {}),
      });
      saved();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <form className="card knowledge-form" onSubmit={save} aria-label="配置业务连接器">
    <h2>配置业务连接器</h2>
    {error && <p role="alert">{error}</p>}
    <label>名称<input name="name" maxLength={120} required defaultValue={connector.name} /></label>
    <label>业务系统<select value={provider} disabled={!!connector.revision} onChange={event => { setProvider(event.target.value); setOps([]); }}>
      {Object.entries(vendors).map(([id, item]) => <option key={id} value={id}>{item.label}</option>)}
    </select></label>
    <label>系统地址<input name="endpoint" type="url" defaultValue={connector.endpoint} required readOnly={!!connector.revision} /></label>
    <label>访问模式<select name="auth" defaultValue={connector.authMode || "service"}><option value="service">空间服务凭据</option><option value="obo">员工 OBO 授权</option></select></label>
    <label>来源设置 JSON<textarea name="settings" rows={4} defaultValue={JSON.stringify(connector.settings || {}, null, 2)} /></label>
    <label>凭据 JSON<input name="credential" type="password" autoComplete="off" placeholder='例如 {"token":"…"}' /></label>
    {provider === "openapi" && <fieldset>
      <legend>导入 OpenAPI / Swagger</legend>
      <input aria-label="接口规范文件" type="file" accept=".json,.yaml,.yml" onChange={event => setFile(event.target.files?.[0])} />
      <button type="button" disabled={busy || !file} onClick={() => void importFile()}>解析接口规范</button>
      <ul className="knowledge-entries">{ops.map(op => <li key={op.name}>{op.description} · {op.risk === "write" ? "L2 写回" : "L0 只读"}</li>)}</ul>
    </fieldset>}
    <label><span><input type="checkbox" name="enabled" defaultChecked={connector.enabled ?? true} /> 启用</span></label>
    <div className="knowledge-actions"><button type="submit" className="primary" disabled={busy || provider === "openapi" && !ops.length}>保存连接器</button>
      <button type="button" disabled={busy} onClick={close}>取消</button></div>
  </form>;
}

function ViewEditor({ base, view, connectors, close, saved }: { base: string; view: Partial<View>; connectors: Connector[]; close: () => void; saved: () => void }) {
  const [connector, setConnector] = useState(view.connectorId || "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const source = connectors.find(item => item.id === connector);
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setError(""); setBusy(true);
    try {
      const extra = objectField(data.get("extra"));
      await api(base + "views/" + view.id, "PUT", { ...extra, connectorId: connector, name: data.get("name"), description: data.get("description"),
        relation: data.get("relation") || "", operation: data.get("operation") || "", timeDimension: data.get("time") || "",
        dimensions: objectField(data.get("dimensions")), metrics: objectField(data.get("metrics")),
        sensitivity: data.get("sensitivity"), enabled: data.has("enabled"), expectedVersion: view.revision || 0,
      });
      saved();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <form className="card knowledge-form" onSubmit={save} aria-label="配置业务语义视图">
    <h2><Database size={16} aria-hidden="true" /> 配置业务语义视图</h2>
    {error && <p role="alert">{error}</p>}
    <label>名称<input name="name" maxLength={120} defaultValue={view.name} required /></label>
    <label>业务说明<input name="description" defaultValue={view.description} required /></label>
    <label>业务来源<select value={connector} disabled={!!view.revision} onChange={event => setConnector(event.target.value)}>
      {connectors.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>
    {source?.driver === "readonly_sql" ? <label>核定关系<input name="relation" defaultValue={view.relation} required /></label> :
      <label>只读操作<select name="operation" defaultValue={view.operation}>
        {(source?.operations || []).filter(item => item.risk === "read").map(item => <option key={item.name} value={item.name}>{item.description}</option>)}
      </select></label>}
    <label>业务维度 JSON<textarea name="dimensions" rows={4} defaultValue={JSON.stringify(view.dimensions || {}, null, 2)} required /></label>
    <label>核定指标 JSON<textarea name="metrics" rows={3} defaultValue={JSON.stringify(view.metrics || {}, null, 2)} /></label>
    <label>日期维度<input name="time" defaultValue={view.timeDimension} /></label>
    <label>密级<select name="sensitivity" defaultValue={view.sensitivity || "internal"}><option value="public">公开</option><option value="internal">内部</option>
      <option value="confidential">机密</option><option value="secret">严格保密</option></select></label>
    <label>关联键、固定筛选和行权限 JSON<textarea name="extra" rows={4} defaultValue={JSON.stringify({ relationships: view.relationships || [],
      defaultFilters: view.defaultFilters || {}, rls: view.rls || {} }, null, 2)} /></label>
    <label><span><input type="checkbox" name="enabled" defaultChecked={view.enabled ?? true} /> 发布视图</span></label>
    <div className="knowledge-actions"><button type="submit" className="primary" disabled={busy}>保存语义视图</button>
      <button type="button" disabled={busy} onClick={close}>取消</button></div>
  </form>;
}

import { useEffect, useState, type FormEvent } from "react";
import { Copy, Webhook } from "lucide-react";
import { api } from "./client";
import type { SpaceOption } from "./eval-preflight";
import type { PublishedAssistant } from "./studio-client";
import "./knowledge-workbench.css";

type Person = { id: string; name: string };
type Connector = { id: string; name: string; enabled: boolean };
type Target = { memberId: string; channel: "feishu" | "dingtalk" };
type Trigger = {
  id: string; name: string; filter: string; dedupKey: string; cooldownSeconds: number; eventFields: string[];
  connectorId?: string; studioReleaseId?: string; notifyTargets: Target[]; allowWritePreview: boolean;
  maintainerId: string; enabled: boolean; revision: number;
};
type Hit = {
  id: string; firstAt: string; lastAt: string; readyAt: string; hits: number; status: string; projectId?: string; resultCode?: string;
};
type Credentials = { webhookPath: string; secret: string };
const results: Record<string, string> = { queued: "等待合并", started: "已启动", completed: "已完成", failed: "执行失败", revoked: "配置已撤销" };
const time = new Intl.DateTimeFormat("zh-CN", { dateStyle: "short", timeStyle: "medium" });

export function EventTriggers({ spaces, preferred, actor, openProject }: {
  spaces: SpaceOption[]; preferred?: string; actor: string; openProject: (id: string) => void;
}) {
  const teams = spaces.filter(item => item.kind === "team" && item.role === "space_admin");
  const [chosen, setChosen] = useState("");
  const space = teams.find(item => item.id === chosen) || teams.find(item => item.id === preferred) || teams[0];
  if (!space) return null;
  return <section className="card" aria-label="业务事件 Webhook 触发器">
    <h2><Webhook size={17} aria-hidden="true" /> 业务事件 Webhook 触发器</h2>
    <label>触发器所属空间<select value={space.id} onChange={event => setChosen(event.target.value)}>
      {teams.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select></label>
    <TriggerList key={space.id} space={space} actor={actor} openProject={openProject} />
  </section>;
}

function TriggerList({ space, actor, openProject }: { space: SpaceOption; actor: string; openProject: (id: string) => void }) {
  const [items, setItems] = useState<Trigger[]>([]);
  const [people, setPeople] = useState<Person[]>([]);
  const [connectors, setConnectors] = useState<Connector[]>([]);
  const [assistants, setAssistants] = useState<PublishedAssistant[]>([]);
  const [config, setConfig] = useState<Trigger>();
  const [selected, select] = useState("");
  const [credentials, setCredentials] = useState<Credentials>();
  const [revision, reload] = useState(0);
  const [error, setError] = useState("");
  const path = `spaces/${space.id}/event-triggers`;

  useEffect(() => {
    let active = true;
    void api<Trigger[]>(path).then(rows => { if (active) setItems(rows); })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, [path, revision]);
  useEffect(() => {
    let active = true;
    const loads = [
      api<Person[]>(`spaces/${space.id}/members`).then(rows => { if (active) setPeople(rows); }),
      api<Connector[]>(`spaces/${space.id}/business/connectors`).then(rows => { if (active) setConnectors(rows.filter(item => item.enabled)); }),
      api<PublishedAssistant[]>("studio/published").then(rows => {
        if (active) setAssistants(rows.filter(item => item.scope === "team" && item.team === space.teamId));
      }),
    ];
    for (const load of loads) void load.catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, [space.id, space.teamId]);

  function edit(value: Trigger) {
    setCredentials(undefined);
    setConfig(value);
  }
  return <>
    {error && <p role="alert">{error}</p>}
    {items.length === 0 && <p>当前团队尚未配置业务事件触发器。</p>}
    <button type="button" onClick={() => edit({ id: crypto.randomUUID(), name: "", filter: "", dedupKey: "event.id",
      cooldownSeconds: 60, eventFields: [], notifyTargets: [], allowWritePreview: false, maintainerId: actor, enabled: true, revision: 0 })}>
      新增 Webhook 触发器
    </button>
    {items.map(item => <article className="card" key={item.id}>
      <h3>{item.name} <small>{item.enabled ? "已启用" : "已暂停"}</small></h3>
      <p>去重键 <code>{item.dedupKey}</code> · {item.cooldownSeconds} 秒内合并处理</p>
      <div className="actions">
        <button type="button" onClick={() => edit(item)}>编辑触发器</button>
        <button type="button" aria-expanded={selected === item.id} onClick={() => select(selected === item.id ? "" : item.id)}>查看命中记录</button>
      </div>
      {selected === item.id && <EventHits path={`${path}/${item.id}/hits`} openProject={openProject} />}
    </article>)}
    {config && <TriggerForm key={config.id + "/" + config.revision} item={config} path={path} people={people} connectors={connectors}
      assistants={assistants} cancel={() => setConfig(undefined)}
      saved={result => { setConfig(undefined); setCredentials(result); reload(value => value + 1); }} />}
    {credentials && <WebhookCredentials value={credentials} close={() => setCredentials(undefined)} />}
  </>;
}

function TriggerForm({ item, path, connectors, people, assistants, cancel, saved }: {
  item: Trigger; path: string; connectors: Connector[]; people: Person[]; assistants: PublishedAssistant[];
  cancel: () => void; saved: (credentials?: Credentials) => void;
}) {
  const [connector, setConnector] = useState(item.connectorId || "");
  const [targets, setTargets] = useState(item.notifyTargets || []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  function update(index: number, target: Target) {
    setTargets(items => items.map((item, i) => i === index ? target : item));
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    setBusy(true);
    setError("");
    try {
      const result = await api<Partial<Credentials>>(`${path}/${item.id}`, "PUT", {
        name: String(data.get("name")).trim(),
        connectorId: connector,
        filter: String(data.get("filter")).trim(),
        dedupKey: String(data.get("dedup")).trim(),
        cooldownSeconds: Number(data.get("cooldown")),
        eventFields: String(data.get("fields")).split(/[,，]/).map(field => field.trim()).filter(Boolean),
        studioReleaseId: data.get("assistant"),
        notifyTargets: targets,
        allowWritePreview: data.has("write"),
        maintainerId: data.get("maintainer"),
        enabled: data.has("enabled"),
        expectedVersion: item.revision,
        rotate: data.has("rotate"),
      });
      saved(result.webhookPath && result.secret ? { webhookPath: result.webhookPath, secret: result.secret } : undefined);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return <form className="card knowledge-form" aria-label="配置 Webhook 触发器" onSubmit={save}>
    <h3>{item.revision ? "编辑 Webhook 触发器" : "新增 Webhook 触发器"}</h3>
    <label>触发器名称<input name="name" maxLength={120} defaultValue={item.name} required /></label>
    <label>业务来源<select value={connector} onChange={event => setConnector(event.target.value)}>
      <option value="">通用 Webhook</option>
      {connector && !connectors.some(item => item.id === connector) && <option value={connector}>当前来源（不可用）</option>}
      {connectors.map(source => <option key={source.id} value={source.id}>{source.name}</option>)}
    </select></label>
    <label>事件筛选（CEL）<input name="filter" placeholder="event.quantity < 5" defaultValue={item.filter} maxLength={512} /></label>
    <label>事件去重键（CEL）<input name="dedup" defaultValue={item.dedupKey} maxLength={256} required /></label>
    <label>合并窗口（秒）<input type="number" name="cooldown" defaultValue={item.cooldownSeconds} min={15} max={21600} required /></label>
    <label>传入任务的字段<input name="fields" defaultValue={(item.eventFields || []).join(", ")} placeholder="例如 id, quantity" maxLength={780} /></label>
    <label>执行助手<select name="assistant" defaultValue={item.studioReleaseId || ""}>
      <option value="">通用 Agent</option>
      {item.studioReleaseId && !assistants.some(assistant => assistant.id === item.studioReleaseId) && <option value={item.studioReleaseId}>当前助手（不可用）</option>}
      {assistants.map(assistant => <option key={assistant.id} value={assistant.id}>{assistant.name} · v{assistant.revision}</option>)}
    </select></label>
    <label>触发器负责人<select name="maintainer" defaultValue={item.maintainerId} required>
      {!people.some(person => person.id === item.maintainerId) && <option value={item.maintainerId}>当前负责人</option>}
      {people.map(person => <option key={person.id} value={person.id}>{person.name}</option>)}
    </select></label>
    <fieldset>
      <legend>私聊通知对象</legend>
      {targets.map((target, index) => <div className="knowledge-toolbar" key={index}>
        <label>成员 {index + 1}<select value={target.memberId} onChange={event => update(index, { ...target, memberId: event.target.value })} required>
          <option value="">选择成员</option>{people.map(person => <option key={person.id} value={person.id}>{person.name}</option>)}
        </select></label>
        <label>渠道 {index + 1}<select value={target.channel} onChange={event => update(index, { ...target, channel: event.target.value as Target["channel"] })}>
          <option value="feishu">飞书</option><option value="dingtalk">钉钉</option>
        </select></label>
        <button type="button" aria-label={`移除通知对象 ${index + 1}`} onClick={() => setTargets(items => items.filter((_, i) => i !== index))}>移除</button>
      </div>)}
      <button type="button" disabled={!people.length || targets.length >= 8} onClick={() => setTargets(items => [...items, { memberId: "", channel: "feishu" }])}>添加通知对象</button>
    </fieldset>
    <label><input type="checkbox" name="write" disabled={!connector} defaultChecked={item.allowWritePreview} /> 允许对所选来源发起 L2 写回审批</label>
    <label><input type="checkbox" name="enabled" defaultChecked={item.enabled} /> 启用触发器</label>
    {item.revision > 0 && <label><input type="checkbox" name="rotate" /> 生成新的 Webhook 地址和签名密钥</label>}
    {error && <p role="alert">{error}</p>}
    <div className="actions"><button type="submit" className="primary" disabled={busy}>保存触发器</button>
      <button type="button" disabled={busy} onClick={cancel}>取消</button></div>
  </form>;
}

function WebhookCredentials({ value, close }: { value: Credentials; close: () => void }) {
  const [message, setMessage] = useState("");
  const url = new URL(value.webhookPath, location.origin).href;
  function copy(text: string) {
    if (!navigator.clipboard) return setMessage("请选中字段手动复制");
    void navigator.clipboard.writeText(text).then(() => setMessage("已复制"), () => setMessage("请选中字段手动复制"));
  }
  return <section className="card knowledge-panel" aria-label="Webhook 签名参数">
    <h3>Webhook 签名参数</h3>
    <p>请保存本次生成的地址和签名密钥。发送方使用 <code>X-Alliance-Timestamp</code> 和 <code>X-Alliance-Signature</code> 签名后提交事件。</p>
    <label>Webhook 地址<input readOnly value={url} onFocus={event => event.currentTarget.select()} /></label>
    <button type="button" onClick={() => copy(url)}><Copy size={14} aria-hidden="true" /> 复制地址</button>
    <label>签名密钥<input readOnly value={value.secret} onFocus={event => event.currentTarget.select()} /></label>
    <button type="button" onClick={() => copy(value.secret)}><Copy size={14} aria-hidden="true" /> 复制密钥</button>
    <button type="button" onClick={close}>关闭签名参数</button>
    {message && <p role="status">{message}</p>}
  </section>;
}

function EventHits({ path, openProject }: { path: string; openProject: (id: string) => void }) {
  const [refresh, setRefresh] = useState(0);
  const [hits, setHits] = useState<Hit[]>();
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void api<Hit[]>(path).then(items => { if (active) { setHits(items); setError(""); } })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, [path, refresh]);

  return <section aria-label="Webhook 命中记录">
    <button type="button" onClick={() => setRefresh(value => value + 1)}>刷新命中记录</button>
    {error && <p role="alert">{error}</p>}
    {!hits && !error && <p role="status">正在读取命中记录…</p>}
    {hits?.length === 0 && <p>暂无命中记录。</p>}
    {!!hits?.length && <div className="admin-table"><table>
      <thead><tr><th>状态</th><th>合并次数</th><th>最近命中</th><th>处理</th></tr></thead>
      <tbody>{hits.map(hit => <tr key={hit.id}>
        <td>{results[hit.status] || hit.status}</td><td>{hit.hits}</td>
        <td>{time.format(new Date(hit.lastAt))}</td>
        <td>{hit.projectId ? <button type="button" onClick={() => openProject(hit.projectId!)}>打开对话</button> :
          hit.status === "queued" ? time.format(new Date(hit.readyAt)) : hit.resultCode || "—"}</td>
      </tr>)}</tbody>
    </table></div>}
  </section>;
}

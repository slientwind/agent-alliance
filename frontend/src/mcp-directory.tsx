import { useEffect, useState, type FormEvent } from "react";
import { api } from "./client";

type Mode = "read_only" | "paid_read" | "write";
type MCP = {
  id: string; name: string; endpoint: string; transport: "streamable_http" | "sse";
  allowPrivateHttp: boolean; enabled: boolean; revision: number; hasCredential: boolean;
  tools: Record<string, Mode>; teams: { id: string; enabled: boolean; revision: number }[];
};
type Team = { id: string; name: string };
type Discovered = { name: string; allowed: boolean };
const path = "admin/mcp/servers";
const toolModes: Record<Mode, string> = { read_only: "免费查询", paid_read: "付费查询", write: "写入" };

export function MCPDirectory() {
  const [rows, setRows] = useState<MCP[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const [draft, setDraft] = useState<MCP>();
  const [selectedTeam, setSelectedTeam] = useState("");
  const [probe, setProbe] = useState<{ id: string; tools: Discovered[] }>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");

  useEffect(() => {
    let active = true;
    Promise.all([api<MCP[]>(path), api<Team[]>("teams")])
      .then(([services, eligible]) => { if (active) { setRows(services); setTeams(eligible); } })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, []);

  function edit(service: MCP) {
    setStatus("");
    setError("");
    setDraft(service);
    setSelectedTeam(teams[0]?.id ?? "");
  }

  function add() {
    edit({ id: crypto.randomUUID(), name: "", endpoint: "", transport: "streamable_http", allowPrivateHttp: false,
      enabled: true, revision: 0, hasCredential: false, tools: {}, teams: [] });
  }

  async function update(action: () => Promise<unknown>, message: string) {
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await action();
      const services = await api<MCP[]>(path);
      setRows(services);
      setDraft(old => old && services.find(service => service.id === old.id) || old);
      setStatus(message);
    } catch (error) {
      setError((error as Error).message);
    } finally { setBusy(false); }
  }

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft) return;
    const form = new FormData(event.currentTarget);
    const bearer = form.has("clearBearer") ? "" : String(form.get("bearer") ?? "").trim() || undefined;
    const { id, name, endpoint, transport, allowPrivateHttp, enabled, tools, revision } = draft;
    void update(async () => {
      await api(path + "/" + id, "PUT", { name, endpoint, transport, allowPrivateHttp, enabled, tools, bearer, expected: revision });
      setProbe(undefined);
    }, "MCP 服务已保存");
  }

  function changeTool(name: string, mode: string) {
    if (!draft) return;
    const tools = { ...draft.tools };
    if (mode) tools[name] = mode as Mode;
    else delete tools[name];
    setDraft({ ...draft, tools });
  }

  const discovered = probe?.id === draft?.id ? probe?.tools ?? [] : [];
  const toolNames = [...new Set([...Object.keys(draft?.tools ?? {}), ...discovered.map(tool => tool.name)])].sort();
  const grant = draft?.teams?.find(team => team.id === selectedTeam);

  return <section className="card" aria-label="企业 MCP 服务">
    <h2>企业 MCP 服务</h2>
    {error && <p role="alert">{error}</p>}
    {status && <p role="status">{status}</p>}
    <ul>{rows.map(row => <li key={row.id}>
      <strong>{row.name}</strong> · {row.enabled ? "已启用" : "已停用"} · {(row.teams ?? []).filter(team => team.enabled).length} 个获准团队
      <div className="row">
        <button type="button" disabled={busy} onClick={() => edit(row)}>编辑 {row.name}</button>
        <button type="button" disabled={busy || !row.enabled} onClick={() => {
          edit(row);
          void update(async () => {
            const result = await api<{ tools: Discovered[] }>(path + "/" + row.id + "/probe", "POST");
            setProbe({ id: row.id, tools: result.tools });
          }, "服务可连接，工具列表已读取");
        }}>检测 {row.name}</button>
      </div>
    </li>)}</ul>
    {!draft && <button type="button" disabled={busy} onClick={add}>添加 MCP 服务</button>}
    {draft && <form key={draft.id + ":" + draft.revision} onSubmit={save}>
      <fieldset disabled={busy}>
        <label>服务名称<input required value={draft.name} onChange={event => setDraft({ ...draft, name: event.target.value })} /></label>
        <label>服务地址<input required type="url" value={draft.endpoint} onChange={event => setDraft({ ...draft, endpoint: event.target.value })} /></label>
        <label>传输协议<select value={draft.transport} onChange={event => setDraft({ ...draft, transport: event.target.value as MCP["transport"] })}>
          <option value="streamable_http">Streamable HTTP</option><option value="sse">SSE</option>
        </select></label>
        <label>访问凭据<input type="password" name="bearer" autoComplete="new-password" placeholder={draft.hasCredential ? "留空保留已保存凭据" : ""} /></label>
        {draft.hasCredential && <label><input type="checkbox" name="clearBearer" /> 清除凭据</label>}
        <label><input type="checkbox" checked={draft.allowPrivateHttp}
          onChange={event => setDraft({ ...draft, allowPrivateHttp: event.target.checked })} /> 允许白名单中的内网 HTTP</label>
        <label><input type="checkbox" checked={draft.enabled} onChange={event => setDraft({ ...draft, enabled: event.target.checked })} /> 启用服务</label>
        {toolNames.length > 0 && <fieldset><legend>可用工具与调用权限</legend>
          {toolNames.map(name => <label key={name}>{name}<select value={draft.tools[name] ?? ""} onChange={event => changeTool(name, event.target.value)}>
            <option value="">禁止调用</option>
            {Object.entries(toolModes).map(([mode, label]) => <option key={mode} value={mode}>{label}</option>)}
          </select></label>)}
        </fieldset>}
        <div className="row">
          <button className="primary">保存 MCP 服务</button>
          <button type="button" onClick={() => setDraft(undefined)}>关闭</button>
        </div>
      </fieldset>
    </form>}
    {draft && draft.revision > 0 && teams.length > 0 && <fieldset disabled={busy}>
      <legend>团队授权</legend>
      <label>团队<select value={selectedTeam} onChange={event => setSelectedTeam(event.target.value)}>
        {teams.map(team => <option key={team.id} value={team.id}>{team.name}</option>)}
      </select></label>
      <button type="button" disabled={!selectedTeam} onClick={() => void update(
        () => api(path + "/" + draft.id + "/teams/" + selectedTeam, "PUT", { expected: grant?.revision ?? 0, enabled: !grant?.enabled }),
        grant?.enabled ? "团队访问权已撤回" : "已授权该团队",
      )}>{grant?.enabled ? "撤回团队访问权" : "授权团队访问"}</button>
    </fieldset>}
  </section>;
}

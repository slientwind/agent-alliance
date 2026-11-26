import { useEffect, useState, type FormEvent } from "react";
import { api } from "./client";

type Host = { host: string; port: number; trustedNetworks: string[]; enabled: boolean; revision: number };
const empty: Host = { host: "", port: 443, trustedNetworks: [], enabled: true, revision: 0 };
const path = "admin/integrations/outbound-hosts";

export function OutboundHosts() {
  const [rows, setRows] = useState<Host[]>([]);
  const [draft, setDraft] = useState<Host>();
  const [networks, setNetworks] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    api<Host[]>(path).then(value => { if (active) setRows(value); })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, []);

  function edit(host: Host) {
    setStatus("");
    setError("");
    setDraft(host);
    setNetworks(host.trustedNetworks.join("\n"));
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    if (!draft) return;
    setBusy(true);
    setError("");
    try {
      const { host, port, revision, enabled } = draft;
      const trustedNetworks = networks.split(/[\s,，]+/).filter(Boolean);
      await api(path, "PUT", { host, port, enabled, expectedRevision: revision, trustedNetworks });
      setRows(await api<Host[]>(path));
      setDraft(undefined);
      setStatus("域名访问设置已保存");
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <section className="card outbound-hosts" aria-label="域名访问许可">
    <h2>域名访问许可</h2>
    {error && <p role="alert">{error}</p>}
    {status && <p role="status">{status}</p>}
    <ul>{rows.map(row => <li key={`${row.host}:${row.port}`}>
      <span>{row.host}:{row.port} · {row.enabled ? "已启用" : "已停用"}</span>{" "}
      <button type="button" disabled={busy} onClick={() => edit(row)}>编辑 {row.host}</button>
    </li>)}</ul>
    {!draft && <button type="button" onClick={() => edit(empty)}>添加域名许可</button>}
    {draft && <form onSubmit={save}>
      <label>域名<input required disabled={busy || !!draft.revision} value={draft.host} onChange={event => setDraft({ ...draft, host: event.target.value })} /></label>
      <label>HTTPS 端口<input required type="number" min="1" max="65535" disabled={busy || !!draft.revision} value={draft.port || ""}
        onChange={event => setDraft({ ...draft, port: Number(event.target.value) })} /></label>
      <label>允许访问的内网网段<textarea rows={2} disabled={busy} value={networks} onChange={event => setNetworks(event.target.value)} /></label>
      <label><input type="checkbox" disabled={busy} checked={draft.enabled} onChange={event => setDraft({ ...draft, enabled: event.target.checked })} /> 启用域名许可</label>
      <div className="row">
        <button className="primary" type="submit" disabled={busy}>保存域名许可</button>
        <button type="button" disabled={busy} onClick={() => setDraft(undefined)}>取消</button>
      </div>
    </form>}
  </section>;
}

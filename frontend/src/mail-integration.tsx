import { useCallback, useEffect, useState } from "react";
import { api } from "./client";

const path = "admin/integrations/email";
type Settings = {
  host: string;
  port: number;
  security: "tls" | "starttls";
  sender: string;
  username: string;
  enabled: boolean;
  hasPassword: boolean;
  revision: number;
  health: "unchecked" | "ready" | "unavailable";
};
type Draft = Pick<Settings, "host" | "port" | "security" | "sender" | "username" | "enabled">;

export function MailIntegration() {
  const [settings, setSettings] = useState<Settings>();
  const [draft, setDraft] = useState<Draft>();
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const reload = useCallback(async () => {
    const data = await api<Settings>(path);
    setSettings(data);
    setDraft(data);
  }, []);
  useEffect(() => { void reload().catch((e: Error) => setError(e.message)); }, [reload]);

  async function save(probe: boolean) {
    if (!settings || !draft || busy) return;
    setBusy(true);
    setError("");
    try {
      if (probe) await api(path + "/probe", "POST");
      else {
        const { host, port, security, sender, username, enabled } = draft;
        await api(path, "PUT", { host, port, security, sender, username, enabled,
          expected: settings.revision, ...(password ? { password } : {}) });
        setPassword("");
      }
      await reload();
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const existingPassword = settings && draft && settings.hasPassword && settings.host === draft.host.trim() &&
    settings.port === draft.port && settings.security === draft.security && settings.username === draft.username;
  const changed = draft && settings && (draft.host !== settings.host || draft.port !== settings.port ||
    draft.security !== settings.security || draft.sender !== settings.sender || draft.username !== settings.username ||
    draft.enabled !== settings.enabled || !!password);

  return (
    <section className="card" aria-labelledby="mail-integration-heading">
      <h2 id="mail-integration-heading">邮件通知</h2>
      {!draft || !settings ? (
        error ? <p role="alert">{error} <button type="button" onClick={() => void reload().catch(e => setError(e.message))}>重试</button></p>
          : <p role="status">正在加载配置…</p>
      ) : (
        <form onSubmit={event => { event.preventDefault(); void save(false); }}>
          <label>SMTP 服务器
            <input required value={draft.host} disabled={busy} onChange={event => setDraft({ ...draft, host: event.target.value })} />
          </label>
          <label>SMTP 端口
            <input required min="1" max="65535" type="number" value={draft.port} disabled={busy}
              onChange={event => setDraft({ ...draft, port: Number(event.target.value) })} />
          </label>
          <label>加密方式
            <select value={draft.security} disabled={busy}
              onChange={event => setDraft({ ...draft, security: event.target.value as Draft["security"] })}>
              <option value="tls">直接 TLS</option><option value="starttls">STARTTLS</option>
            </select>
          </label>
          <label>发件邮箱
            <input required type="email" value={draft.sender} disabled={busy}
              onChange={event => setDraft({ ...draft, sender: event.target.value })} />
          </label>
          <label>SMTP 用户名
            <input value={draft.username} disabled={busy} onChange={event => setDraft({ ...draft, username: event.target.value })} />
          </label>
          {draft.username && (
            <label>SMTP 密码
              <input type="password" autoComplete="new-password" value={password} disabled={busy}
                placeholder={existingPassword ? "已保存，填写可替换" : ""} onChange={event => setPassword(event.target.value)} />
            </label>
          )}
          <label><input type="checkbox" checked={draft.enabled} disabled={busy}
            onChange={event => setDraft({ ...draft, enabled: event.target.checked })} /> 允许发送定时任务邮件</label>
          <div className="row">
            <button className="primary" type="submit" disabled={busy || !changed || (!!draft.username && draft.enabled && !password && !existingPassword)}>保存邮件服务</button>
            <button type="button" disabled={busy || !!changed || !settings.host} onClick={() => void save(true)}>检测邮件连接</button>
          </div>
          <p>{settings.health === "ready" ? "邮件连接正常" : settings.health === "unavailable" ? "邮件连接失败" : "尚未检测邮件连接"}</p>
          {error && <p role="alert">{error}</p>}
        </form>
      )}
    </section>
  );
}

import { useEffect, useState } from "react";
import { api } from "./client";

const path = "admin/integrations/search";
const providers = {
  tavily: { name: "Tavily", endpoint: "https://api.tavily.com/search" },
  bocha: { name: "博查", endpoint: "https://api.bochaai.com/v1/web-search" },
} as const;
type Provider = keyof typeof providers;
type SearchSettings = {
  provider: Provider;
  endpoint: string;
  enabled: boolean;
  hasCredential: boolean;
  revision: number;
  health: "unchecked" | "ready" | "unavailable";
  checkedAt?: string;
};

export function SearchIntegration() {
  const [settings, setSettings] = useState<SearchSettings>();
  const [provider, setProvider] = useState<Provider>("tavily");
  const [endpoint, setEndpoint] = useState("");
  const [key, setKey] = useState("");
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let current = true;
    setError("");
    api<SearchSettings>(path).then(value => {
      if (!current) return;
      setSettings(value);
      setProvider(value.provider);
      setEndpoint(value.endpoint);
      setEnabled(value.enabled);
    }).catch((e: Error) => { if (current) setError(e.message); });
    return () => { current = false; };
  }, [attempt]);

  async function action(probe: boolean) {
    if (!settings) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      if (probe) {
        await api(path + "/probe", "POST");
      } else {
        await api(path, "PUT", {
          provider, endpoint, enabled, expected: settings.revision,
          ...(key ? { key } : {}),
        });
        setKey("");
      }
      setNotice(probe ? "检测已完成" : "搜索服务已保存");
      setAttempt(value => value + 1);
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const existing = settings?.hasCredential && settings.provider === provider &&
    settings.endpoint === (endpoint.trim() || providers[provider].endpoint);
  const changed = settings && (settings.provider !== provider || settings.endpoint !== (endpoint.trim() || providers[provider].endpoint) ||
    settings.enabled !== enabled || !!key);

  return (
    <section className="card" aria-labelledby="search-integration-heading">
      <h2 id="search-integration-heading">联网搜索</h2>
      {!settings ? (
        error ? <p role="alert">{error} <button type="button" onClick={() => setAttempt(value => value + 1)}>重试</button></p>
          : <p role="status">正在加载配置…</p>
      ) : (
        <form onSubmit={event => { event.preventDefault(); void action(false); }}>
          <label>搜索服务
            <select value={provider} disabled={busy} onChange={event => {
              setProvider(event.target.value as Provider);
              setEndpoint("");
              setKey("");
            }}>
              {Object.entries(providers).map(([id, choice]) => <option key={id} value={id}>{choice.name}</option>)}
            </select>
          </label>
          <label>服务地址
            <input type="url" value={endpoint} placeholder={providers[provider].endpoint} disabled={busy}
              onChange={event => setEndpoint(event.target.value)} />
          </label>
          <label>搜索服务密钥
            <input type="password" autoComplete="new-password" value={key} disabled={busy}
              placeholder={existing ? "已保存，填写可替换" : "输入服务密钥"} onChange={event => setKey(event.target.value)} />
          </label>
          <label><input type="checkbox" checked={enabled} disabled={busy} onChange={event => setEnabled(event.target.checked)} /> 允许员工在对话中联网搜索</label>
          <div className="row">
            <button type="submit" className="primary" disabled={busy || !changed || (enabled && !existing && !key)}>保存</button>
            <button type="button" disabled={busy || !settings.hasCredential || !!changed} onClick={() => void action(true)}>检测连接</button>
          </div>
          <p>{settings.health === "ready" ? "连接正常" : settings.health === "unavailable" ? "连接失败，请核对服务地址和密钥" : "尚未检测"}</p>
          {notice && <p role="status">{notice}</p>}
          {error && <p role="alert">{error}</p>}
        </form>
      )}
    </section>
  );
}

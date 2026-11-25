import { useEffect, useState, type FormEvent } from "react";
import { api } from "./client";
import { exactCny, microCny } from "./format-money";
import "./admin-monitor.css";
import { ModelProviders, type ModelProvider } from "./model-providers";

type Route = { modelId: string; model: string; provider: string; priority: number; weight: number; enabled: boolean; health: string };
type Tier = { tier: keyof typeof tiers; revision: number; routes: Route[] };
type Option = { id: string; label: string; enabled: boolean; capabilities: string[] };
type Price = { id: string; modelId: string; providerId: string; model: string; effectiveAt: string } & Record<keyof typeof priceFields, string>;

const root = "admin/models/";
const tiers = { default: "通用", fast: "快速", reasoning: "深度思考", vision: "识图", image: "生图", embedding: "向量检索" };
const health: Record<string, string> = { healthy: "连接正常", unavailable: "连接异常", untested: "待检测" };
const priceFields = {
  inputPerMillionMicroCny: "每百万输入 token", outputPerMillionMicroCny: "每百万输出 token",
  cachedPerMillionMicroCny: "每百万缓存 token", perImageMicroCny: "每张图片",
};
const fields = Object.keys(priceFields) as (keyof typeof priceFields)[];
const date = new Intl.DateTimeFormat("zh-CN", { dateStyle: "short", timeStyle: "short" });

function TierCard({ row, models, saved }: { row: Tier; models: Option[]; saved: (tier: string) => Promise<void> }) {
  const [primary, setPrimary] = useState(row.routes.find(route => route.priority === 0)?.modelId || "");
  const [backup, setBackup] = useState("");
  const [fallbacks, setFallbacks] = useState(row.routes.filter(route => route.priority !== 0).map(route => route.modelId));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const capable = row.tier === "default" || row.tier === "fast" ? "text" : row.tier;
  const supported = models.filter(model => model.capabilities.includes(capable) && (model.enabled || row.routes.some(route => route.modelId === model.id)));
  const optionLabel = (id: string) => supported.find(model => model.id === id)?.label || row.routes.find(route => route.modelId === id)?.model || id;
  const currentHealth = row.routes.find(route => route.modelId === primary)?.health;

  function promote(id: string) {
    setError("");
    setFallbacks(items => {
      const updated = items.filter(item => item !== id);
      const original = row.routes.find(route => route.modelId === primary);
      return original && primary !== id && !updated.includes(primary) && updated.length < 31 ? [primary, ...updated] : updated;
    });
    setPrimary(id);
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const oldRank = row.routes.find(route => route.modelId === primary)?.priority || 1;
      const lastRank = Math.max(0, ...row.routes.map(route => route.priority));
      const routes = [primary, ...fallbacks.filter(id => id !== primary)].map((modelId, index) => {
        const prior = row.routes.find(route => route.modelId === modelId);
        const priority = index === 0 ? 0 : prior ? prior.priority || oldRank : Math.min(32767, lastRank + index);
        return { modelId, priority, weight: prior?.weight || 1 };
      });
      await api(root + "tiers/" + row.tier, "PUT", { expectedRevision: row.revision, routes });
      await saved(tiers[row.tier]);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <form className="card admin-tier" onSubmit={save} aria-label={tiers[row.tier] + "档位"}>
    <h2>{tiers[row.tier]}</h2>
    {error && <p role="alert">{error}</p>}
    <label>主模型<select value={primary} required disabled={busy} onChange={event => promote(event.target.value)}>
      <option value="" disabled>选择模型</option>
      {supported.map(model => <option value={model.id} key={model.id}>{model.label}{model.enabled ? "" : " · 已停用"}</option>)}
    </select></label>
    {currentHealth && <p>{health[currentHealth] || "待检测"}</p>}
    {fallbacks.length > 0 && <ol className="admin-run-list" aria-label="备选模型">
      {fallbacks.map(id => <li key={id}><span>{optionLabel(id)}</span><span className="row">
        <button type="button" disabled={busy} onClick={() => promote(id)}>设为主模型</button>
        <button type="button" disabled={busy} aria-label={"移除 " + optionLabel(id)} onClick={() => setFallbacks(rows => rows.filter(item => item !== id))}>移除</button>
      </span></li>)}
    </ol>}
    <div className="row admin-filters">
      <label>备选模型<select value={backup} disabled={busy} onChange={event => setBackup(event.target.value)}>
        <option value="">选择备选</option>
        {supported.filter(model => model.id !== primary && !fallbacks.includes(model.id)).map(model => <option key={model.id} value={model.id}>{model.label}</option>)}
      </select></label>
      <button type="button" disabled={busy || !backup || backup === primary || fallbacks.includes(backup) || fallbacks.length >= 31}
        onClick={() => { setFallbacks(value => [...value, backup]); setBackup(""); }}>添加备选</button>
    </div>
    <button type="submit" className="primary" disabled={busy || !primary}>保存档位</button>
  </form>;
}

export function AdminGateway() {
  const [providers, setProviders] = useState<ModelProvider[]>([]);
  const [routing, setRouting] = useState<Tier[]>([]);
  const [prices, setPrices] = useState<Price[]>([]);
  const [error, setError] = useState("");
  const [tierNotice, setTierNotice] = useState("");
  const [selected, setSelected] = useState("");
  const [at, setAt] = useState("");
  const [priceError, setPriceError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [priceRefresh, setPriceRefresh] = useState(0);
  const pricePath = root + "pricing" + (selected ? "?" + new URLSearchParams({ model: selected }) : "");

  useEffect(() => {
    let live = true;
    Promise.all([api<ModelProvider[]>(root + "providers"), api<Tier[]>(root + "tiers")])
      .then(([items, rows]) => { if (live) { setProviders(items || []); setRouting(rows || []); } })
      .catch(error => { if (live) setError((error as Error).message); });
    return () => { live = false; };
  }, []);

  useEffect(() => {
    let live = true;
    api<Price[]>(pricePath).then(rates => { if (live) setPrices(rates || []); })
      .catch(error => { if (live) setPriceError((error as Error).message); });
    return () => { live = false; };
  }, [pricePath, priceRefresh]);

  const models = providers.flatMap(provider => provider.models.map(model => ({ ...model, label: provider.name + " · " + model.name,
    enabled: provider.enabled && model.enabled })));
  const pricesShown = selected ? prices.filter(price => price.modelId === selected) : prices;

  async function addPrice(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy(true);
    setPriceError("");
    setSaved(false);
    try {
      const rates = Object.fromEntries(fields.map(field => [field, microCny(String(data.get(field) || ""))]));
      await api(root + "pricing", "POST", { modelId: selected, ...(at ? { effectiveAt: new Date(at).toISOString() } : {}), ...rates });
      setPriceRefresh(value => value + 1);
      setSaved(true);
      setAt("");
      form.reset();
    } catch (error) {
      setPriceError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="admin-gateway">
    {error && <p role="alert">{error}</p>}
    {tierNotice && <p role="status">{tierNotice}</p>}
    <ModelProviders items={providers} refresh={async () => {
      const items = await api<ModelProvider[]>(root + "providers");
      setProviders(items || []);
      setRouting((await api<Tier[]>(root + "tiers")) || []);
    }} />
    <div className="admin-tier-grid">{routing.map(row => <TierCard key={row.tier + "/" + row.revision} row={row} models={models}
      saved={async name => { setRouting((await api<Tier[]>(root + "tiers")) || []); setTierNotice(`${name}档位已保存`); }} />)}</div>
    <section className="card" aria-label="模型单价">
      <h2>模型单价</h2>
      {priceError && <p role="alert">{priceError}</p>}
      <label>模型<select value={selected} onChange={event => { setSelected(event.target.value); setSaved(false); setPriceError(""); }}>
        <option value="">全部模型</option>{models.map(item => <option key={item.id} value={item.id}>{item.label}</option>)}
      </select></label>
      {pricesShown.length > 0 ? <div className="admin-table"><table>
        <thead><tr><th scope="col">供应商与模型</th><th scope="col">生效时间</th>{fields.map(field => <th scope="col" key={field}>{priceFields[field]}</th>)}</tr></thead>
        <tbody>{pricesShown.map(price => <tr key={price.id}>
          <th scope="row">{providers.find(provider => provider.id === price.providerId)?.name} · {price.model}</th>
          <td>{date.format(new Date(price.effectiveAt))}</td>{fields.map(field => <td key={field}>{exactCny(price[field])}</td>)}
        </tr>)}</tbody>
      </table></div> : <p>暂无单价记录。</p>}
      {selected && <details>
        <summary>新增生效单价</summary>
        <form className="admin-price-form" onSubmit={addPrice}>
          {fields.map(field => <label key={field}>{priceFields[field]}（元）<input name={field} inputMode="decimal" pattern="[0-9]+(\.[0-9]{1,6})?" required defaultValue="0" /></label>)}
          <label>生效时间<input type="datetime-local" value={at} onChange={event => setAt(event.target.value)} /></label>
          {saved && <p role="status">单价已保存</p>}
          <button type="submit" className="primary" disabled={busy}>保存单价</button>
        </form>
      </details>}
    </section>
  </div>;
}

import { useState } from "react";
import { api } from "./client";

const protocols: Record<string, string> = {
  openai: "OpenAI 兼容", bailian: "阿里百炼", ark: "火山方舟", deepseek: "DeepSeek",
  anthropic: "Anthropic", gemini: "Gemini 兼容", vllm: "vLLM", ollama: "Ollama",
};
const capabilityNames: Record<string, string> = {
  text: "对话", tool_call: "工具调用", reasoning: "深度思考", vision: "识图", image: "生图", embedding: "向量检索",
};

export type GlobalModel = {
  id: string; name: string; capabilities: string[]; enabled: boolean;
  contextTokens: number; maxOutputTokens: number; rpm?: number; tpm?: number;
};
export type ModelProvider = {
  id: string; name: string; kind: string; endpoint: string; enabled: boolean; health: string;
  hasKey: boolean; revision: number; models: GlobalModel[];
};
type Impact = { tier: string; model: string; priority: number; availableAlternates: number };

const route = "admin/models/providers";

export function ModelProviders({ items, refresh }: { items: ModelProvider[]; refresh: () => Promise<void> }) {
  return <section className="card" aria-label="全局模型供应商">
    <h2>全局模型供应商</h2>
    <details>
      <summary>添加模型供应商</summary>
      <ProviderForm refresh={refresh} />
    </details>
    {items.map(provider => <ProviderEntry key={provider.id} provider={provider} refresh={refresh} />)}
  </section>;
}

function ProviderEntry({ provider, refresh }: { provider: ModelProvider; refresh: () => Promise<void> }) {
  const [impact, setImpact] = useState<Impact[]>();
  const [error, setError] = useState("");
  const [health, setHealth] = useState("");
  const [busy, setBusy] = useState(false);
  const [probeModel, setProbeModel] = useState("");
  const probeChoices = provider.models.filter(model => model.capabilities.some(cap => ["text", "reasoning", "vision", "embedding"].includes(cap)));
  const selected = probeChoices.find(model => model.id === probeModel) ?? probeChoices[0];

  async function action(load: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await load();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <details>
    <summary>{provider.name} · {provider.enabled ? "已启用" : "已停用"}</summary>
    <ProviderForm key={provider.revision} provider={provider} refresh={refresh} />
    <div className="admin-form-actions">
      <button type="button" disabled={busy} onClick={() => void action(async () => {
        const rows = await api<{ routes: Impact[] }>(route + "/" + provider.id + "/impact");
        setImpact(rows.routes);
      })}>查看影响范围</button>
      {selected && <>
        <label>检测模型<select value={selected.id} onChange={event => setProbeModel(event.target.value)}>
          {probeChoices.map(model => <option key={model.id} value={model.id}>{model.name}</option>)}
        </select></label>
        <button type="button" disabled={busy} onClick={() => void action(async () => {
          const result = await api<{ health: string; providerStatus: number }>(route + "/" + provider.id + "/probe", "POST",
            { modelId: selected.id, expectedRevision: provider.revision });
          setHealth(result.health === "healthy" ? "连接正常" : "连接未成功" + (result.providerStatus ? "（" + result.providerStatus + "）" : ""));
          await refresh();
        })}>检测连接</button>
      </>}
    </div>
    {impact && <section aria-label="影响范围">
      {impact.length ? <ul>{impact.map(item => <li key={item.tier + "/" + item.model}>
        {item.model} · {item.tier} · {item.priority === 0 ? "主模型" : "备用模型"} · 其他可用候选 {item.availableAlternates} 个
      </li>)}</ul> : <p>暂未分配到使用档位。</p>}
    </section>}
    {health && <p role="status">{health}</p>}
    {error && <p role="alert">{error}</p>}
    {provider.models.map(model => <details key={model.id}>
      <summary>{model.name}</summary>
      <ModelForm provider={provider} model={model} refresh={refresh} />
    </details>)}
    <details>
      <summary>为此供应商添加模型</summary>
      <ModelForm provider={provider} refresh={refresh} />
    </details>
  </details>;
}

function ProviderForm({ provider, refresh }: { provider?: ModelProvider; refresh: () => Promise<void> }) {
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return <form className="admin-price-form" onSubmit={async event => {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await api(route + (provider ? "/" + provider.id : ""), provider ? "PUT" : "POST", {
        name: data.get("name"), kind: provider?.kind ?? data.get("kind"), endpoint: data.get("endpoint"),
        enabled: data.has("enabled"),
        ...(data.has("clear") ? { key: "" } : data.get("key") ? { key: data.get("key") } : {}),
        ...(provider ? { expectedRevision: provider.revision } : {}),
      });
      if (!provider) form.reset();
      setNotice("供应商已保存");
      await refresh();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }}>
    <label>供应商名称<input name="name" required maxLength={120} defaultValue={provider?.name} /></label>
    {!provider && <label>接口类型<select name="kind">
      {Object.entries(protocols).map(([id, caption]) => <option key={id} value={id}>{caption}</option>)}
    </select></label>}
    <label>服务地址<input name="endpoint" required type="url" defaultValue={provider?.endpoint} /></label>
    <label>{provider?.hasKey ? "替换访问凭据" : "访问凭据"}<input name="key" type="password" autoComplete="new-password" /></label>
    {provider?.hasKey && <label><input name="clear" type="checkbox" />清除已有凭据</label>}
    <label><input name="enabled" type="checkbox" defaultChecked={provider?.enabled ?? true} />启用供应商</label>
    {notice && <p role="status">{notice}</p>}
    {error && <p role="alert">{error}</p>}
    <button type="submit" disabled={busy}>保存供应商</button>
  </form>;
}

function ModelForm({ provider, model, refresh }: { provider: ModelProvider; model?: GlobalModel; refresh: () => Promise<void> }) {
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  return <form className="admin-price-form" onSubmit={async event => {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setBusy(true);
    setMessage("");
    setError("");
    try {
      const rpm = data.get("rpm");
      const tpm = data.get("tpm");
      await api(route + "/" + provider.id + "/models" + (model ? "/" + model.id : ""), model ? "PUT" : "POST", {
        name: model?.name ?? data.get("name"), capabilities: data.getAll("capabilities"), enabled: data.has("enabled"),
        contextTokens: Number(data.get("contextTokens")), maxOutputTokens: Number(data.get("maxOutputTokens")),
        rpm: rpm ? Number(rpm) : null, tpm: tpm ? Number(tpm) : null,
      });
      if (!model) form.reset();
      await refresh();
      setMessage("模型已保存");
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }}>
    {!model && <label>模型标识<input name="name" required maxLength={240} /></label>}
    <fieldset>
      <legend>模型能力</legend>
      {Object.entries(capabilityNames).map(([key, label]) => <label key={key}>
        <input name="capabilities" type="checkbox" value={key} defaultChecked={model?.capabilities.includes(key) ?? (key === "text" || key === "tool_call")} />{label}
      </label>)}
    </fieldset>
    <label>对话容量<input name="contextTokens" type="number" min={0} required defaultValue={model?.contextTokens ?? 32768} /></label>
    <label>单次回复上限<input name="maxOutputTokens" type="number" min={0} required defaultValue={model?.maxOutputTokens ?? 4096} /></label>
    <label>每分钟请求上限<input name="rpm" type="number" min={1} defaultValue={model?.rpm} /></label>
    <label>每分钟 token 上限<input name="tpm" type="number" min={1} defaultValue={model?.tpm} /></label>
    <label><input name="enabled" type="checkbox" defaultChecked={model?.enabled ?? true} />启用模型</label>
    {message && <p role="status">{message}</p>}
    {error && <p role="alert">{error}</p>}
    <button type="submit" disabled={busy}>保存模型</button>
  </form>;
}

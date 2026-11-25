import { useEffect, useState } from "react";
import { api } from "./client";
import { microCny } from "./format-money";
import "./initial-workspace-setup.css";

type SetupStatus = { complete: boolean; hasProvider: boolean; sources: { id: string; name: string }[] };
type Receipt = { tiers: string[]; invitations: string[]; expires: string };

const protocols = {
  openai: "OpenAI 兼容", anthropic: "Anthropic", bailian: "阿里百炼", ark: "火山方舟",
  deepseek: "DeepSeek", gemini: "Gemini 兼容", vllm: "vLLM", ollama: "Ollama",
};
const capabilities = {
  text: "对话", tool_call: "工具调用", reasoning: "深度思考", vision: "看图", image: "生成图片", embedding: "资料检索",
};
const tiers: Record<string, string> = {
  default: "通用", fast: "快速", reasoning: "深度思考", vision: "识图", image: "生图", embedding: "向量检索",
};
const costs = {
  InputPerMillion: "每百万输入 token", OutputPerMillion: "每百万输出 token",
  CachedPerMillion: "每百万缓存 token", PerImage: "每张图片",
};

export function InitialWorkspaceSetup({ close, manage, refresh }: {
  close: () => void;
  manage: () => void;
  refresh: () => Promise<void>;
}) {
  const [status, setStatus] = useState<SetupStatus>();
  const [models, setModels] = useState([0]);
  const [nextModel, setNextModel] = useState(1);
  const [result, setResult] = useState<Receipt>();
  const [problem, setProblem] = useState("");
  const [busy, setBusy] = useState(false);
  const [attempt, setAttempt] = useState<{ key: string; request: string }>();
  useEffect(() => {
    let active = true;
    void api<SetupStatus>("admin/onboarding").then(value => {
      if (active) setStatus(value);
    }).catch(error => { if (active) setProblem((error as Error).message); });
    return () => { active = false; };
  }, []);
  if (result) return (
    <section className="initial-setup" aria-label="设置已完成">
      <p>已启用 {result.tiers.map(tier => tiers[tier] ?? tier).join("、")}。</p>
      {result.invitations.length > 0 && <div>
        <p>将每个邀请凭证交给一位同事，凭证在 48 小时内有效。</p>
        {result.invitations.map((token, index) => <label key={token}>邀请凭证 {index + 1}
          <input value={token} readOnly onFocus={event => event.currentTarget.select()} />
        </label>)}
      </div>}
      {problem && <p role="alert">{problem}</p>}
      <button type="button" onClick={close}>开始使用</button>
    </section>
  );
  if (!status) return problem
    ? <p role="alert">{problem}</p>
    : <p role="status">正在读取工作空间设置…</p>;
  if (status.complete) return <section className="initial-setup">
    <p>首次设置已完成。</p><button type="button" onClick={manage}>查看模型设置</button>
  </section>;
  return (
    <form className="initial-setup" onSubmit={async event => {
      event.preventDefault();
      const form = event.currentTarget;
      const data = new FormData(form);
      setBusy(true);
      setProblem("");
      try {
        const reader = (key: string) => String(data.get(key) ?? "");
        const provider = status.hasProvider ? undefined : {
          Name: reader("provider"),
          Kind: reader("protocol"),
          Endpoint: reader("address"),
          Key: reader("credential"),
          Models: models.map(id => ({
            Name: reader("model" + id),
            Capabilities: data.getAll("capabilities" + id),
            ContextTokens: Number(reader("context" + id)),
            MaxOutputTokens: Number(reader("output" + id)),
            ...Object.fromEntries(Object.keys(costs).map(key => [key, microCny(reader(key + id))])),
          })),
        };
        const body = { provider, sources: data.getAll("sources"), inviteCount: Number(reader("invites")) };
        const request = JSON.stringify(body);
        const key = attempt?.request === request ? attempt.key : crypto.randomUUID();
        setAttempt({ key, request });
        const configured = await api<Receipt>("admin/onboarding", "POST", body, { "Idempotency-Key": key });
        setAttempt(undefined);
        setResult(configured);
        form.reset();
        await refresh();
      } catch (error) {
        setProblem((error as Error).message);
      } finally {
        setBusy(false);
      }
    }}>
      {!status.hasProvider && <fieldset disabled={busy}>
        <legend>接入模型供应商</legend>
        <div className="setup-fields">
          <label>供应商名称<input name="provider" required maxLength={120} /></label>
          <label>接口类型<select name="protocol">
            {Object.entries(protocols).map(([key, title]) => <option key={key} value={key}>{title}</option>)}
          </select></label>
          <label>HTTPS 服务地址<input name="address" required type="url" placeholder="https://" spellCheck={false} /></label>
          <label>访问凭据<input name="credential" type="password" autoComplete="new-password" spellCheck={false} /></label>
        </div>
        <p>系统会按模型能力分配六个使用档位。对应能力可以稍后补充。</p>
        {models.map((id, index) => <fieldset key={id}>
          <legend>模型 {index + 1}</legend>
          <div className="setup-fields">
            <label>模型名称<input name={"model" + id} required maxLength={240} /></label>
            <label>对话容量<input name={"context" + id} type="number" min={1} required defaultValue={32768} /></label>
            <label>单次回复上限<input name={"output" + id} type="number" min={1} required defaultValue={4096} /></label>
          </div>
          <div className="setup-options">
            {Object.entries(capabilities).map(([capability, title]) => <label key={capability}>
              <input name={"capabilities" + id} value={capability} type="checkbox" defaultChecked={id === 0 && (capability === "text" || capability === "tool_call")} />
              {title}
            </label>)}
          </div>
          <p>按供应商价格填写人民币单价。未收费的项目填 0。</p>
          <div className="setup-fields">
            {Object.entries(costs).map(([key, title]) => <label key={key}>{title}（元）
              <input name={key + id} required inputMode="decimal" />
            </label>)}
          </div>
          {models.length > 1 && <button type="button" onClick={() => setModels(previous => previous.filter(model => model !== id))}>
            移除模型 {index + 1}
          </button>}
        </fieldset>)}
        {models.length < 6 && <button type="button" onClick={() => {
          setModels(previous => [...previous, nextModel]);
          setNextModel(previous => previous + 1);
        }}>再添加一个模型</button>}
      </fieldset>}
      <fieldset disabled={busy}>
        <legend>启用生态扫描源</legend>
        <div className="setup-options">
          {status.sources.map(source => <label key={source.id}>
            <input type="checkbox" value={source.id} name="sources" />{source.name}
          </label>)}
        </div>
      </fieldset>
      <label>生成团队邀请凭证<input type="number" name="invites" min={0} max={10} defaultValue={0} required /></label>
      {problem && <p role="alert">{problem}</p>}
      <div className="setup-options">
        <button type="submit" disabled={busy}>{busy ? "正在保存…" : "完成设置"}</button>
        <button type="button" disabled={busy} onClick={close}>稍后设置</button>
      </div>
    </form>
  );
}

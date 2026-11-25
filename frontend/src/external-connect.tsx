import { useEffect, useState, type FormEvent } from "react";
import { Bot, Cable, GitFork, Globe, Send, Sparkles } from "lucide-react";
import { api } from "./client";
import type { AgentTeam } from "./agent-team";
import type { ExternalAgent } from "./external-agents";
import "./external-connect.css";

const templates = [
  { id: "a2a", name: "A2A", variants: ["a2a"], icon: Cable },
  { id: "openai", name: "OpenAI 兼容", variants: ["openai", "fastgpt"], icon: Sparkles },
  { id: "dify", name: "Dify", variants: ["dify"], icon: Bot },
  { id: "coze", name: "Coze", variants: ["coze"], icon: Bot },
  { id: "langgraph", name: "LangGraph", variants: ["langgraph"], icon: GitFork },
  { id: "webhook", name: "Webhook", variants: ["webhook", "manus"], icon: Globe },
] as const;

const initialWebhook = {
  submit: "/tasks", poll: "/tasks/{task}", cancel: "", pollMethod: "GET", cancelMethod: "POST", auth: "bearer",
  inputField: "input", taskPath: "$.task_id", statePath: "$.status", resultPath: "$.output",
};

type AdapterTemplate = { id: string; name: string; route: string; model?: string; requiresStreaming: boolean; protocol?: string };
type Saved = { id: string; revision: number };
type Test = { id: string; status: string; output?: string };
type Card = { name: string; url: string; preferredTransport?: string; additionalInterfaces?: { url: string; transport: string }[] };
type Candidate = { id: string; name: string };

const phases = { draft: "待测试", beta: "Beta 试用", pending_verify: "待认证", verified: "全企可用" };
const describeError = (error: unknown) => error instanceof Error ? error.message : "操作失败，请稍后重试";

export function ExternalConnect({ teams, preferredTeam, refresh, openConversation }: {
  teams: AgentTeam[];
  preferredTeam: string;
  refresh: () => Promise<void>;
  openConversation: (agent?: ExternalAgent) => void;
}) {
  const [catalog, setCatalog] = useState<AdapterTemplate[]>([]);
  const [owned, setOwned] = useState<ExternalAgent[]>([]);
  const [step, setStep] = useState(1);
  const [selection, selectTemplate] = useState<string>();
  const [provider, setProvider] = useState("");
  const [team, setTeam] = useState("");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [credential, setCredential] = useState("");
  const [model, setModel] = useState("");
  const [stream, setStream] = useState(false);
  const [webhook, setWebhook] = useState(initialWebhook);
  const [card, setCard] = useState<Card>();
  const [cardEndpoint, setCardEndpoint] = useState("");
  const [connection, setConnection] = useState<Saved>();
  const [sample, setSample] = useState("");
  const [authorized, setAuthorized] = useState(false);
  const [trial, setTrial] = useState<Test>();
  const [candidates, setCandidates] = useState<Candidate[]>();
  const [testers, setTesters] = useState<string[]>([]);
  const [savedTesters, setSavedTesters] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const template = templates.find(item => item.id === selection);
  const adapter = catalog.find(item => item.id === provider);
  const selectedTeam = teams.some(item => item.id === team) ? team
    : teams.some(item => item.id === preferredTeam) ? preferredTeam
      : teams.find(item => item.default)?.id || (teams.length === 1 ? teams[0].id : "");
  const active = owned.find(item => item.id === connection?.id && item.revision === connection?.revision);
  const phase = active?.phase || (trial?.status === "passed" ? "beta" : "draft");
  const endpoints = card ? Array.from(new Set([
    ...(!card.preferredTransport || card.preferredTransport === "JSONRPC" ? [card.url] : []),
    ...(card.additionalInterfaces || []).filter(item => item.transport === "JSONRPC").map(item => item.url),
  ])) : [];

  async function loadConnections() {
    const connections = (await api<ExternalAgent[]>("agents/admissions")).filter(item => item.owner);
    setOwned(connections);
    return connections;
  }

  useEffect(() => {
    let active = true;
    Promise.all([api<AdapterTemplate[]>("agents/adapter-templates"), api<ExternalAgent[]>("agents/admissions")])
      .then(([values, existing]) => {
        if (active) {
          setCatalog(values);
          setOwned(existing.filter(item => item.owner));
        }
      }, error => { if (active) setError(describeError(error)); });
    return () => { active = false; };
  }, []);

  async function run(action: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (error) {
      setError(describeError(error));
    } finally {
      setBusy(false);
    }
  }

  function chooseProvider(id: string) {
    setProvider(id);
    setEndpoint("");
    setCredential("");
    setModel("");
    setCard(undefined);
    setCardEndpoint("");
    setStream(catalog.find(item => item.id === id)?.requiresStreaming ?? false);
  }

  function findCard() {
    return run(async () => {
      const found = await api<Card>("agents/cards/preview", "POST", { url: endpoint.trim() });
      setCard(found);
      setCardEndpoint(!found.preferredTransport || found.preferredTransport === "JSONRPC" ? found.url
        : found.additionalInterfaces?.find(item => item.transport === "JSONRPC")?.url || "");
    });
  }

  function saveConnection(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void run(async () => {
      const shared = { team: selectedTeam, key: credential };
      const result = provider === "a2a"
        ? await api<Saved>("agents/cards", "POST", { ...shared, endpoint: cardEndpoint, card, enterpriseAdmission: true })
        : await api<Saved>("agents/adapters", "POST", {
          ...shared, name: name.trim(), description: description.trim(), endpoint: endpoint.trim(),
          adapter: {
            protocol: adapter?.protocol || "openai_compat", template: provider,
            ...(adapter?.model ? { model: model.trim() } : {}),
            ...(!adapter?.protocol ? { stream: adapter?.requiresStreaming || stream } : {}),
            ...(provider === "webhook" ? { webhook: {
              submit: { path: webhook.submit, method: "POST" },
              poll: { path: webhook.poll, method: webhook.pollMethod },
              ...(webhook.cancel ? { cancel: { path: webhook.cancel, method: webhook.cancelMethod } } : {}),
              auth: webhook.auth, inputField: webhook.inputField, taskPath: webhook.taskPath,
              statePath: webhook.statePath, resultPath: webhook.resultPath,
            } } : {}),
          },
        });
      setConnection(result);
      setCredential("");
      setStep(3);
      await loadConnections();
    });
  }

  function resume(agent: ExternalAgent) {
    setConnection({ id: agent.id, revision: agent.revision });
    setTrial(agent.activeTrial ? { id: agent.activeTrial, status: "remote" } : undefined);
    setCandidates(undefined);
    setTesters(agent.testers || []);
    setSavedTesters(false);
    setError("");
    setStep(3);
  }

  async function testConnection(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!connection) return;
    await run(async () => {
      const key = crypto.randomUUID();
      const request = () => api<Test>(`agents/${connection.id}/validate`, "POST", {
        revision: connection.revision, input: sample.trim(), admissionTest: true, authorizeOneCall: authorized,
      }, { "Idempotency-Key": key });
      let response: Test;
      try {
        response = await request();
      } catch (error) {
        if (!(error instanceof TypeError)) throw error;
        response = await request();
      }
      await showTest(response);
    });
  }

  async function showTest(test: Test) {
    setTrial(test);
    setAuthorized(false);
    await loadConnections();
    if (test.status === "passed") await refresh();
  }

  function verifyConnection() {
    if (!connection || !active) return;
    void run(async () => {
      const path = "agents/" + connection.id;
      const review = await api<{ build: { digest: string }; evidence: { digest: string; passed: boolean } }>(`${path}/builds/${connection.revision}`);
      if (!review.evidence.passed) throw new Error("当前版本需要重新通过测试。");
      await api(path + "/release-requests", "POST", {
        revision: connection.revision, buildDigest: review.build.digest, evidenceDigest: review.evidence.digest,
        previous: active.release?.id || "",
      });
      await loadConnections();
    });
  }

  return (
    <div className="external-connect">
      <ol className="external-steps" aria-label="接入步骤">
        {["选择接入方式", "填写连接信息", "发送测试消息"].map((label, index) =>
          <li key={label} aria-current={step === index + 1 ? "step" : undefined}>{label}</li>)}
      </ol>
      {error && <p role="alert">{error}</p>}
      {step === 1 && <>
        <div className="external-templates" role="group" aria-label="接入方式">
          {templates.map(({ id, name, icon: Icon }) => <button type="button" key={id} aria-pressed={selection === id}
            onClick={() => { selectTemplate(id); chooseProvider(id); }}>
            <Icon aria-hidden="true" /> <span>{name}</span>
          </button>)}
        </div>
        <button className="primary" type="button" disabled={!selection || (provider !== "a2a" && !adapter)}
          onClick={() => setStep(2)}>继续</button>
        {owned.length > 0 && <section aria-label="我的外部 Agent">
          <h2>我的外部 Agent</h2>
          <ul className="external-connections">
            {owned.map(agent => <li key={agent.id + "/" + agent.revision}>
              <button type="button" onClick={() => resume(agent)}>{agent.name}</button>
              <span>{phases[agent.phase]}</span>
            </li>)}
          </ul>
        </section>}
      </>}
      {step === 2 && <form className="external-form" onSubmit={saveConnection}>
        {template && template.variants.length > 1 && <label>服务模板
          <select value={provider} onChange={event => chooseProvider(event.target.value)} disabled={busy}>
            {template.variants.map(id => <option key={id} value={id}>{catalog.find(item => item.id === id)?.name || id}</option>)}
          </select>
        </label>}
        {provider !== "a2a" && <>
          <label>Agent 名称<input required value={name} maxLength={120} onChange={event => setName(event.target.value)} disabled={busy} /></label>
          <label>用途说明<textarea rows={2} value={description} maxLength={1000} onChange={event => setDescription(event.target.value)} disabled={busy} /></label>
        </>}
        <label>所属团队<select value={selectedTeam} onChange={event => setTeam(event.target.value)} required disabled={busy}>
          <option value="">请选择</option>
          {teams.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select></label>
        <label>{provider === "a2a" ? "公开 Agent Card 地址" : "服务地址"}
          <input type="url" value={endpoint} required placeholder={"https://agent.example.com" + (adapter?.route || "")}
            onChange={event => { setEndpoint(event.target.value); setCard(undefined); setCardEndpoint(""); }} disabled={busy} />
        </label>
        {provider === "a2a" && <>
          <button type="button" disabled={busy || !endpoint.trim()} onClick={() => void findCard()}>读取 Agent Card</button>
          {card && <label>选择接收消息的接口
            <select value={cardEndpoint} required onChange={event => setCardEndpoint(event.target.value)} disabled={busy}>
              <option value="">请选择</option>
              {endpoints.map(url => <option key={url} value={url}>{url}</option>)}
            </select>
          </label>}
        </>}
        <label>服务密钥（可选）
          <input type="password" autoComplete="new-password" maxLength={4096} value={credential}
            onChange={event => setCredential(event.target.value)} disabled={busy} />
        </label>
        {adapter?.model && <label>{adapter.model}<input required value={model} maxLength={256}
          onChange={event => setModel(event.target.value)} disabled={busy} /></label>}
        {adapter && !adapter.protocol && <label className="external-check">
          <input type="checkbox" checked={stream || adapter.requiresStreaming} disabled={busy || adapter.requiresStreaming} onChange={event => setStream(event.target.checked)} />
          接收流式答复
        </label>}
        {provider === "webhook" && <div className="external-webhook">
          <label>提交路径<input required value={webhook.submit} onChange={event => setWebhook({ ...webhook, submit: event.target.value })} disabled={busy} /></label>
          <label>查询路径<input required value={webhook.poll} onChange={event => setWebhook({ ...webhook, poll: event.target.value })} disabled={busy} /></label>
          <details>
            <summary>回执字段与鉴权方式</summary>
            <label>鉴权请求头<select value={webhook.auth} onChange={event => setWebhook({ ...webhook, auth: event.target.value })} disabled={busy}>
              <option value="bearer">Authorization · Bearer</option>
              <option value="api_key">API_KEY</option>
              <option value="x-api-key">X-API-Key</option>
            </select></label>
            <label>查询方法<select value={webhook.pollMethod} onChange={event => setWebhook({ ...webhook, pollMethod: event.target.value })} disabled={busy}>
              <option>GET</option><option>POST</option>
            </select></label>
            <label>取消路径（可选）<input value={webhook.cancel} onChange={event => setWebhook({ ...webhook, cancel: event.target.value })} disabled={busy} /></label>
            {webhook.cancel && <label>取消方法<select value={webhook.cancelMethod} onChange={event => setWebhook({ ...webhook, cancelMethod: event.target.value })} disabled={busy}>
              <option>POST</option><option>DELETE</option>
            </select></label>}
            {([["inputField", "消息字段"], ["taskPath", "任务编号字段"], ["statePath", "状态字段"], ["resultPath", "答复字段"]] as const)
              .map(([key, label]) => <label key={key}>{label}<input required value={webhook[key]}
                onChange={event => setWebhook({ ...webhook, [key]: event.target.value })} disabled={busy} /></label>)}
          </details>
        </div>}
        <details>
          <summary>用量与运行记录（可选）</summary>
          <p>服务可在响应体提供标准用量，或返回 <code>X-Alliance-Usage</code> 和 <code>X-Alliance-Trace-Url</code> 响应头。未收到用量时会标为「外部未披露」。</p>
        </details>
        <div className="row"><button type="button" disabled={busy} onClick={() => setStep(1)}>上一步</button>
          <button type="submit" className="primary" disabled={busy || !selectedTeam || (provider === "a2a" ? !cardEndpoint : !adapter)}>
            保存并测试
          </button></div>
      </form>}
      {step === 3 && connection && <section className="external-form" aria-label="连接测试">
        <h2>{active?.name || name || card?.name || "外部 Agent"}</h2>
        <p role="status">{phases[phase]}</p>
        {phase === "draft" && trial?.status !== "remote" && <form className="external-form" onSubmit={testConnection}>
          <label>测试消息<textarea required rows={3} value={sample} maxLength={2000}
            onChange={event => setSample(event.target.value)} disabled={busy} /></label>
          <label className="external-check"><input type="checkbox" checked={authorized} onChange={event => setAuthorized(event.target.checked)} disabled={busy} />
            同意发送这条测试消息，费用由服务方决定
          </label>
          <button type="submit" className="primary" disabled={busy || !authorized || !sample.trim()}><Send size={17} aria-hidden="true" /> 发送测试</button>
        </form>}
        {trial?.output && <pre className="external-reply">{trial.output}</pre>}
        {trial?.status === "remote" && <>
          <p>服务已接收任务。查询会沿用这次的任务编号。</p>
          <button type="button" disabled={busy} onClick={() => void run(async () => {
            const result = await api<Test>(`agents/${connection.id}/evidence/${trial.id}/receipt`, "POST", { action: "check" });
            await showTest({ ...result, id: trial.id });
          })}>查询答复</button>
        </>}
        {phase !== "draft" && <div className="row">
          <button type="button" onClick={() => openConversation(active)}>在对话中使用</button>
          {phase === "beta" && active && <button type="button" disabled={busy} onClick={verifyConnection}>申请全企认证</button>}
        </div>}
        {active?.release?.state === "returned" && active.release.reason && <p>退回原因 {active.release.reason}</p>}
        {active && phase !== "verified" && (candidates ? <fieldset disabled={busy}>
          <legend>测试组</legend>
          {candidates.length === 0 && <p>当前团队没有可添加的其他成员。</p>}
          {candidates.map(member => <label className="external-check" key={member.id}>
            <input type="checkbox" checked={testers.includes(member.id)} onChange={event => {
              setSavedTesters(false);
              setTesters(value => event.target.checked ? [...value, member.id] : value.filter(id => id !== member.id));
            }} />
            {member.name}
          </label>)}
          <button type="button" disabled={testers.length > 50} onClick={() => void run(async () => {
            await api(`agents/${connection.id}/external-testers`, "PUT", {
              revision: connection.revision, expected: active.testersRevision, members: testers,
            });
            await loadConnections();
            setSavedTesters(true);
          })}>保存测试组</button>
          {savedTesters && <p role="status">测试组已保存</p>}
        </fieldset> : <button type="button" disabled={busy} onClick={() => void run(async () => {
          const available = await api<Candidate[]>(`agents/${connection.id}/external-testers?revision=${connection.revision}`);
          setTesters((active.testers || []).filter(id => available.some(candidate => candidate.id === id)));
          setCandidates(available);
        })}>选择测试成员</button>)}
        <button type="button" disabled={busy} onClick={() => {
          setTrial(undefined);
          setConnection(undefined);
          setCandidates(undefined);
          setSample("");
          setAuthorized(false);
          setError("");
          setStep(1);
        }}>返回接入列表</button>
      </section>}
    </div>
  );
}

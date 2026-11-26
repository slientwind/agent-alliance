import { useEffect, useRef, useState, type FormEvent } from "react";
import { ArrowLeft, FileText, Paperclip, X } from "lucide-react";
import { api } from "./client";
import type { ExternalAgent } from "./external-agents";
import { buildDraft, scopeLabel, tryDraft, type AgentScope, type CapabilityOption, type PublishedAssistant,
  type Refinement, type StudioDraft, type StudioTrial } from "./studio-client";

const maxSamples = 3;
const maxSampleBytes = 25 * 1024 * 1024;
const tiers: Record<string, string> = { default: "通用", fast: "快速", reasoning: "深度思考" };
const publishChoices: { scope: AgentScope; label: string }[] = [
  { scope: "personal", label: "保存自用" },
  { scope: "team", label: "共享给团队" },
  { scope: "enterprise", label: "申请全企上架" },
];

type Release = { id: string; revision: number; scope: AgentScope; state: string };
type LoadedSkill = { id: string; name: string; version: string };

async function retry<T>(action: () => Promise<T>) {
  try {
    return await action();
  } catch (error) {
    if (!(error instanceof TypeError)) throw error;
    return action();
  }
}

function Diff({ diff }: { diff: Refinement["diff"] }) {
  return (
    <div className="studio-diff">
      <div><h4>修改前</h4><pre>{diff.before.slice(0, diff.offset)}{diff.removed && <del>{diff.removed}</del>}
        {diff.before.slice(diff.offset + diff.removed.length)}</pre></div>
      <div><h4>修改后</h4><pre>{diff.after.slice(0, diff.offset)}{diff.added && <ins>{diff.added}</ins>}
        {diff.after.slice(diff.offset + diff.added.length)}</pre></div>
    </div>
  );
}

export function StudioEditor({ id, back, use }: {
  id: string;
  back: () => void;
  use: (assistant: PublishedAssistant) => void;
}) {
  const path = "studio/" + id;
  const [draft, setDraft] = useState<StudioDraft>();
  const [options, setOptions] = useState<CapabilityOption[]>([]);
  const [availableTiers, setAvailableTiers] = useState<string[]>([]);
  const [targets, setTargets] = useState<ExternalAgent[]>([]);
  const [loadedSkills, setLoadedSkills] = useState<LoadedSkill[]>([]);
  const [skillError, setSkillError] = useState("");
  const [selectedTargets, selectTargets] = useState<string[]>([]);
  const [mode, setMode] = useState<"chat" | "config">("chat");
  const [message, setMessage] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [trialMessage, setTrialMessage] = useState("");
  const [trial, setTrial] = useState<StudioTrial>();
  const [trialPreview, setTrialPreview] = useState("");
  const [trialPassed, setTrialPassed] = useState(false);
  const [selection, setSelection] = useState({ start: 0, end: 0 });
  const [feedback, setFeedback] = useState("");
  const [proposal, setProposal] = useState<Refinement>();
  const [released, setReleased] = useState<PublishedAssistant>();
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const conversation = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let active = true;
    Promise.all([
      api<StudioDraft>(path),
      api<CapabilityOption[]>(path + "/capabilities"),
      api<ExternalAgent[]>("agents/admissions").catch((): ExternalAgent[] => []),
      api<string[]>("general-tiers").catch((): string[] => []),
    ]).then(([loaded, caps, external, available]) => {
      if (active) {
        setDraft(loaded);
        setOptions(caps);
        setAvailableTiers(available);
        setTargets(external.filter(agent => agent.phase !== "draft"));
      }
    }).catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, [path]);

  useEffect(() => {
    if (conversation.current) conversation.current.scrollTop = conversation.current.scrollHeight;
  }, [draft?.messages.length, mode]);

  useEffect(() => {
    if (mode !== "config") return;
    let active = true;
    const refresh = () => {
      api<LoadedSkill[]>(path + "/skills").then(value => {
        if (!active) return;
        setSkillError("");
        setLoadedSkills(value);
      }).catch(error => { if (active) setSkillError((error as Error).message); });
    };
    refresh();
    window.addEventListener("focus", refresh);
    return () => {
      active = false;
      window.removeEventListener("focus", refresh);
    };
  }, [mode, path]);

  async function run(label: string, action: () => Promise<void>) {
    setBusy(label);
    setError("");
    setNotice("");
    try {
      await action();
    } catch (error) {
      setError((error as Error).message || "操作失败，请稍后重试");
    } finally {
      setBusy("");
    }
  }

  function pickFiles(incoming: File[]) {
    const added = [...files, ...incoming];
    if (added.length > maxSamples || added.reduce((size, file) => size + file.size, 0) > maxSampleBytes) {
      setError("最多选择 3 份标杆文件，合计不超过 25 MiB。");
      return;
    }
    if (added.some(file => !/\.(docx|xlsx|pptx|csv|tsv|md|txt)$/i.test(file.name))) {
      setError("请选择 Word、Excel、PPT、CSV 或文本文件。");
      return;
    }
    setError("");
    setFiles(added);
  }

  function chat(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || !message.trim()) return;
    const request = crypto.randomUUID();
    void run("正在生成草稿…", async () => {
      const turn = await retry(() => buildDraft(id, draft.revision, message.trim(), files, request));
      setDraft(current => current && { ...current, revision: turn.revision, spec: turn.spec,
        messages: [...current.messages, { ...turn, message: message.trim() }] });
      setMessage("");
      setFiles([]);
    });
  }

  function trialRun(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || !trialMessage.trim()) return;
    const request = crypto.randomUUID();
    void run("助手正在试聊…", async () => {
      try {
        setTrial(undefined);
        setTrialPassed(false);
        setSelection({ start: 0, end: 0 });
        setProposal(undefined);
        setFeedback("");
        const value = await retry(() => {
          setTrialPreview("");
          return tryDraft(id, draft.revision, trialMessage.trim(), request, chunk => setTrialPreview(preview => preview + chunk));
        });
        setTrial(value);
      } finally {
        setTrialPreview("");
      }
    });
  }

  function refine(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || !trial || selection.end <= selection.start || !feedback.trim()) return;
    void run("正在生成局部修改…", async () => {
      const headers = { "Idempotency-Key": crypto.randomUUID() };
      const result = await retry(() => api<Refinement>(path + "/refine", "POST", {
        expected: draft.revision, trial: trial.id, ...selection, feedback: feedback.trim(),
      }, headers));
      setProposal(result);
    });
  }

  function apply() {
    if (!draft || !proposal) return;
    void run("正在保存修改…", async () => {
      const result = await api<{ revision: number }>(`${path}/refinements/${proposal.id}/apply`, "POST", {
        expected: proposal.revision,
      });
      setDraft(current => current && { ...current, revision: result.revision,
        spec: { ...current.spec, instructions: proposal.diff.after } });
      setProposal(undefined);
      setFeedback("");
      setTrialPassed(false);
      setNotice("已应用修改。可在新版本上再试一次。");
    });
  }

  function saveConfiguration(changes: { capabilities: string[] } | { tier: string }) {
    if (!draft) return;
    void run("正在保存配置…", async () => {
      const saved = await api<StudioDraft>(path + "/capabilities", "PUT", { expected: draft.revision, ...changes });
      setDraft(current => current && { ...current, revision: saved.revision, spec: saved.spec });
      if (!saved.spec.capabilities.includes("external")) selectTargets([]);
    });
  }

  function toggleCapability(id: string, enabled: boolean) {
    if (!draft) return;
    const current = draft.spec.capabilities;
    saveConfiguration({ capabilities: enabled ? [...current, id] : current.filter(item => item !== id) });
  }

  function getDependencies() {
    return targets.filter(agent => selectedTargets.includes(`${agent.id}/${agent.revision}`));
  }

  function captureSelection(field: HTMLTextAreaElement) {
    setSelection({ start: field.selectionStart, end: field.selectionEnd });
  }

  function publish(scope: AgentScope) {
    if (!draft) return;
    const external = getDependencies().map(({ id, revision }) => ({ id, revision }));
    void run("正在保存发布版本…", async () => {
      const receipt = await api<Release>(path + "/publish", "POST", {
        expected: draft.revision, scope, externalAgents: external,
        ...(scope !== "personal" && trial ? { trial: trial.id } : {}),
      });
      if (receipt.state === "published") {
        setReleased({ id: receipt.id, draft: draft.id, revision: receipt.revision, team: draft.team,
          name: draft.spec.name, purpose: draft.spec.purpose, scope, owner: true, capabilities: draft.spec.capabilities, externalAgents: external });
        setNotice(`已保存为${scopeLabel[scope]}助手`);
      } else if (receipt.state === "pending") {
        setNotice("已提交全企认证，管理员批准后即可使用。");
      } else {
        setNotice("此版本已处理。更新草稿后可以重新申请。");
      }
    });
  }

  if (!draft) return error ? <p role="alert">{error}</p> : <p role="status">正在读取草稿…</p>;
  const currentTrial = trial?.revision === draft.revision;
  const sharedTargets = getDependencies().every(agent => agent.phase === "verified");
  const canPublish = draft.spec.name.trim() && (draft.spec.instructions.trim() || draft.spec.steps.length);
  const activeSelection = currentTrial && trial && selection.end > selection.start;
  return (
    <div className="studio-editor">
      <div className="studio-toolbar">
        <button type="button" onClick={back}><ArrowLeft size={17} aria-hidden="true" /> 助手列表</button>
        <strong>{draft.spec.name || "未命名助手"}</strong>
      </div>
      {error && <p role="alert">{error}</p>}
      {busy && <p role="status">{busy}</p>}
      {notice && <p role="status">{notice}</p>}
      <div className="studio-split">
        <section className="studio-side" aria-label="构建助手">
          <div className="row" role="group" aria-label="构建视图">
            <button type="button" aria-pressed={mode === "chat"} onClick={() => setMode("chat")}>构建对话</button>
            <button type="button" aria-pressed={mode === "config"} onClick={() => setMode("config")}>配置清单</button>
          </div>
          {mode === "chat" ? (
            <>
              <div className="studio-history" ref={conversation} aria-label="构建消息">
                {draft.messages.map(turn => <article key={turn.revision}>
                  <p className="studio-user-message">{turn.message}</p>
                  <p>{turn.reply}</p>
                  {turn.questions?.length ? <ul>{turn.questions.map((question, i) => <li key={i}>{question}</li>)}</ul> : null}
                </article>)}
              </div>
              <form className="studio-form" onSubmit={chat}>
                <label>构建需求<textarea rows={3} maxLength={3500} value={message} onChange={event => setMessage(event.target.value)}
                  placeholder="例如：核对供应商报价，标出缺少有效期的条目" disabled={!!busy} required /></label>
                <div className="studio-dropzone" onDragOver={event => event.preventDefault()} onDrop={event => {
                  event.preventDefault();
                  if (!busy) pickFiles(Array.from(event.dataTransfer.files));
                }}>
                  <input type="file" multiple hidden ref={input} accept=".docx,.xlsx,.pptx,.csv,.tsv,.md,.txt"
                    onChange={event => {
                      pickFiles(Array.from(event.target.files || []));
                      event.target.value = "";
                    }} />
                  <button type="button" disabled={!!busy} onClick={() => input.current?.click()}>
                    <Paperclip size={16} aria-hidden="true" /> 添加标杆文件
                  </button>
                  <small>可拖入 1–3 份文件，用于提取大纲、列数和排版结构。</small>
                </div>
                {!!files.length && <ul className="studio-files">
                  {files.map((file, index) => <li key={index}><FileText size={16} aria-hidden="true" /> {file.name}
                    <button type="button" aria-label={"移除 " + file.name} disabled={!!busy}
                      onClick={() => setFiles(value => value.filter((_, i) => i !== index))}><X size={16} /></button></li>)}
                </ul>}
                <button type="submit" className="primary" disabled={!!busy || !message.trim()}>发送构建需求</button>
              </form>
            </>
          ) : (
            <div className="studio-config">
              {draft.spec.purpose && <p>{draft.spec.purpose}</p>}
              {draft.spec.instructions && <p className="studio-instructions">{draft.spec.instructions}</p>}
              {draft.spec.steps.length > 0 && <section><h3>执行步骤</h3><ol>{draft.spec.steps.map((step, i) => <li key={i}>{step}</li>)}</ol></section>}
              {draft.spec.checks.length > 0 && <section><h3>交付前检查</h3><ul>{draft.spec.checks.map((value, i) => <li key={i}>{value}</li>)}</ul></section>}
              <label>模型档位<select value={draft.spec.tier} disabled={!!busy}
                onChange={event => saveConfiguration({ tier: event.target.value })}>
                {Object.entries(tiers).map(([value, title]) => <option key={value} value={value}
                  disabled={value !== draft.spec.tier && !availableTiers.includes(value)}>{title}</option>)}
              </select></label>
              <fieldset disabled={!!busy}>
                <legend>能力</legend>
                {options.map(option => <label key={option.id} className="studio-check">
                  <input type="checkbox" checked={draft.spec.capabilities.includes(option.id)}
                    onChange={event => toggleCapability(option.id, event.target.checked)} />
                  <span>{option.label}{!option.available && <small> · 当前未配置</small>}</span>
                </label>)}
              </fieldset>
              {skillError && <p role="alert">{skillError}</p>}
              {loadedSkills.length > 0 && <section>
                <h3>已加载的 Skill</h3>
                <ul>{loadedSkills.map(skill => <li key={skill.id}>{skill.name} · {skill.version}</li>)}</ul>
              </section>}
              {targets.length > 0 && draft.spec.capabilities.includes("external") && <fieldset disabled={!!busy}>
                <legend>可委派的外部 Agent</legend>
                {targets.map(agent => {
                  const key = `${agent.id}/${agent.revision}`;
                  return <label key={key} className="studio-check">
                    <input type="checkbox" checked={selectedTargets.includes(key)} onChange={event => {
                      selectTargets(value => event.target.checked ? [...value, key] : value.filter(item => item !== key));
                    }} />
                    {agent.name}{agent.phase === "verified" ? "" : " · Beta"}
                  </label>;
                })}
              </fieldset>}
            </div>
          )}
        </section>
        <section className="studio-side" aria-label="试聊与微调">
          <h2>试聊</h2>
          <form className="studio-form" onSubmit={trialRun}>
            <label>试聊消息<textarea rows={3} maxLength={3500} value={trialMessage} onChange={event => setTrialMessage(event.target.value)}
              placeholder="输入一条助手实际会收到的需求" disabled={!!busy} required /></label>
            <button type="submit" className="primary" disabled={!!busy || !trialMessage.trim() || (!draft.spec.instructions && !draft.spec.steps.length)}>
              发送试聊
            </button>
          </form>
          {trialPreview && <p className="studio-instructions" aria-label="试聊生成中">{trialPreview}</p>}
          {trial && <>
            <label className="studio-form">试聊答复
              <textarea key={trial.id} readOnly rows={7} defaultValue={trial.output} onSelect={event => captureSelection(event.currentTarget)}
                onKeyUp={event => captureSelection(event.currentTarget)} onPointerUp={event => captureSelection(event.currentTarget)} />
            </label>
            {currentTrial ? <label className="studio-check">
              <input type="checkbox" checked={trialPassed} onChange={event => setTrialPassed(event.target.checked)} disabled={!!busy} /> 这份试聊达到了预期
            </label> : <p>草稿已更新。请在当前版本上重新试聊。</p>}
            {currentTrial && <small>选中答复中的一段文字即可提出调整。</small>}
          </>}
          {activeSelection && <form className="studio-form" onSubmit={refine}>
            <label>调整意见<textarea rows={2} maxLength={2000} value={feedback} onChange={event => setFeedback(event.target.value)}
              placeholder="例如：以后金额列右对齐" disabled={!!busy} required /></label>
            <button type="submit" disabled={!!busy || !feedback.trim()}>预览修改</button>
          </form>}
          {proposal && <section aria-label="指令修改对比">
            <h3>指令修改对比</h3>
            <Diff diff={proposal.diff} />
            <div className="row">
              <button type="button" className="primary" disabled={!!busy || proposal.revision !== draft.revision} onClick={apply}>应用修改</button>
              <button type="button" disabled={!!busy} onClick={() => setProposal(undefined)}>放弃此次修改</button>
            </div>
          </section>}
        </section>
      </div>
      <footer className="studio-publish" aria-label="发布范围">
        <div className="row">
          {publishChoices.map(({ scope, label }) => <button key={scope} type="button" className={scope === "personal" ? "primary" : undefined}
            disabled={!!busy || !canPublish || (scope !== "personal" && (!currentTrial || !trialPassed || !sharedTargets))}
            onClick={() => publish(scope)}>{label}</button>)}
          {released && <button type="button" onClick={() => use(released)}>在对话中使用</button>}
        </div>
        {!currentTrial || !trialPassed ? <small>共享给团队或全企前，请完成并确认一次当前版本的试聊。</small>
          : !sharedTargets ? <small>共享版本只可委派全企认证的外部 Agent。</small> : null}
      </footer>
    </div>
  );
}

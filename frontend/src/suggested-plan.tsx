import { AggregationScope } from "./aggregation-scope";
import { RequestRelease } from "./agent-release";
import { fileFormat, plannedOutputs } from "./output-operations";
import { planningFileLabel } from "./planning-inputs";
import { CandidateComparison } from "./candidate-comparison";
import { SimilarAgents } from "./similar-agents";
import { AgentChecks } from "./agent-checks";
import {
  AggregateColumns,
  describeTool,
  suggestedTool,
  toolChoices,
  type ToolChoice,
} from "./tool-creation";
import { TrialEvidence } from "./trial-evidence";
import { authorizeTrial } from "./trial-request";
import { useEffect, useState } from "react";
import {
  api,
  RequestError,
  type Agent,
  type Model,
  type Member,
  type Project,
} from "./client";

type Props = {
  sources: { id: string; name: string; version: number; revoked: boolean }[];
  project: Project;
  agents: Agent[];
  models: Model[];
  user: Member;
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
  reloadCatalog: () => Promise<void>;
  openLibrary: () => void;
};

export function SuggestedPlan(props: Props) {
  const { project: p, agents, busy, perform } = props;
  const suggestion = p.suggestion!,
    plan = suggestion.result!,
    existing = plan.agents || [],
    missing = plan.missing || [];
  const formats =
    plan.outputFormats ?? (plan.outputFormat ? [plan.outputFormat] : []);
  const [independentSelection, setIndependentSelection] = useState("");
  const availableAgents = [...agents, ...(p.sharedAgents || [])];
  const route = plan.toolChoice;
  const alternative = route && availableAgents.find((a) => a.id === route.alternative);
  const routeReady = !route || (!!alternative?.publishedReady && alternative.published === route.revision);
  const positions: number[] = [];
  const sequence: {
    name: string;
    id: string;
    version: number;
    ready: boolean;
    operation?: string;
    reason?: string;
    lookup?: boolean;
    calculate?: boolean;
  }[] = [];
  for (let pos = 0; pos <= existing.length; pos++) {
    for (const [slot, gap] of missing.entries()) {
      const draft = plan.drafts?.find((d) => d.gap === gap);
      if ((draft?.after ?? existing.length) !== pos) continue;
      const binding = suggestion.bindings.find((b) => b.slot === slot);
      const producer =
        binding &&
        availableAgents.find(
          (a) => a.id === binding.agent && a.published === binding.version,
        );
      sequence.push({
        name: binding?.name || gap,
        operation: producer?.publishedOperation,
        id: binding?.agent || "",
        version: binding?.version || 0,
        ready: !!binding?.ready,
      });
    }
    if (pos < existing.length) {
      positions.push(sequence.length + 1);
      const agent = availableAgents.find((a) => a.id === existing[pos]),
        version = plan.revisions?.[pos] ?? agent?.published ?? 0;
      sequence.push({
        name:
          agent?.published === version
            ? agent.publishedName || agent.name
            : "原推荐能力",
        id: existing[pos],
        operation:
          agent?.published === version ? agent.publishedOperation : undefined,
        reason: plan.reasons?.[pos],
        calculate: plan.calculationSteps?.includes(pos + 1),
        lookup: plan.lookupSteps?.includes(pos + 1),
        version,
        ready:
          !!version && !!agent?.publishedReady && agent.published === version,
      });
    }
  }
  const independentFiles =
    formats.length > 1 &&
    !!suggestion.inputSources?.length &&
    sequence.length > 1 &&
    sequence.every(
      (step) =>
        step.ready &&
        (step.operation === "docx" ||
          step.operation === "xlsx" ||
          step.operation === "png"),
    );
  const selection = JSON.stringify([
    suggestion.id,
    sequence.map((s) => [s.id, s.version]),
  ]);
  const independent = independentFiles && independentSelection === selection;
  return (
    <>
      {route && (
        <section className="card" aria-label="建议自动选择汇总口径">
          <strong>运行时选择一种汇总口径</strong>
          <p>根据本次目标和 CSV 表头选择口径，再执行一项汇总。最多两次能力调用。</p>
          <p>
            <strong>{route.primaryName} v{plan.revisions?.[route.step - 1]}</strong>{" "}
            <AggregationScope config={route.primaryScope} />
          </p>
          <p>
            <strong>{route.alternativeName} v{route.revision}</strong>{" "}
            <AggregationScope config={route.alternativeScope} />
          </p>
          {!routeReady && <p role="status">备选汇总已换版或暂不可用，请重新规划。</p>}
        </section>
      )}
      {formats.length > 0 && (
        <p>
          <strong>交付成果</strong> ·{" "}
          {formats.map((f) => plannedOutputs[f]).join("、")}
        </p>
      )}
      {(existing.length > 0 || sequence.length > 1) && (
        <ol>
          {sequence.map((s, i) => {
            const produced = fileFormat(s.operation) ?? "";
            return (
              <li key={i}>
                {route && positions[route.step - 1] === i + 1
                  ? "执行所选汇总并生成 Excel"
                  : s.name + " · " + (s.ready ? "已发布 v" + s.version : "待补齐或复查")}
                {formats.length > 1 && formats.includes(produced) && (
                  <small>产出 · {plannedOutputs[produced]}</small>
                )}
                {s.calculate && <small>核算资料中的金额</small>}
                {s.reason && <small>规划助手建议 · {s.reason}</small>}
              </li>
            );
          })}
        </ol>
      )}
      {!!suggestion.inputSources?.length && (
        <div>
          使用资料
          {suggestion.inputSources.map((pin) => {
            const file = props.sources.find((f) => f.id === pin.id);
            return (
              <div key={pin.id}>
                {planningFileLabel(file, pin.version)}
                {file && !file.revoked && file.version !== pin.version && (
                  <>
                    {" "}
                    ·{" "}
                    <a
                      href={`/api/projects/${p.id}/sources/${pin.id}/content?version=${pin.version}`}
                    >
                      查看本次使用的 v{pin.version} 原件
                    </a>
                  </>
                )}
              </div>
            );
          })}
        </div>
      )}
      {plan.executionBrief && (
        <details>
          <summary>查看本次执行要求</summary>
          <p style={{ whiteSpace: "pre-wrap" }}>{plan.executionBrief}</p>
        </details>
      )}
      {existing.length > 0 && (
        <CandidateComparison
          project={p.id}
          recommended={existing.map((id, i) => ({
            id,
            revision: plan.revisions?.[i] || 0,
          }))}
        />
      )}
      {missing.map((gap, slot) => (
        <Gap
          key={suggestion.id + ":" + slot}
          {...props}
          gap={gap}
          slot={slot}
        />
      ))}
      {independentFiles && (
        <label>
          <input
            type="checkbox"
            checked={independent}
            onChange={(e) =>
              setIndependentSelection(e.target.checked ? selection : "")
            }
          />
          各文件分别使用已选原件制作
        </label>
      )}
      <button
        disabled={busy || !routeReady || !sequence.length || sequence.some((s) => !s.ready)}
        onClick={() =>
          perform(async () => {
            try {
              await api("projects/" + p.id + "/plan", "POST", {
                expected: p.revision,
                suggestion: suggestion.id,
                toolChoices: route ? [{
                  step: positions[route.step - 1],
                  selector: positions[route.selector - 1],
                  alternative: route.alternative,
                  revision: route.revision,
                }] : undefined,
                sources: suggestion.inputSources ?? p.plan?.sources,
                calculationSteps: sequence.flatMap((s, i) =>
                  s.calculate ? [i + 1] : [],
                ),
                lookupSteps: sequence.flatMap((s, i) =>
                  s.lookup ? [i + 1] : [],
                ),
                preferences: p.plan?.preferences,
                brief: plan.executionBrief || plan.text,
                dependencies: independent ? sequence.map(() => []) : undefined,
                agents: sequence.map((s) => s.id),
                agentRevisions: sequence.map((s) => s.version),
              });
            } catch (error) {
              if (route && error instanceof RequestError && error.status === 409) {
                await props.reloadCatalog().catch(() => {});
              }
              throw error;
            }
          })
        }
      >
        采用为待确认方案
      </button>
    </>
  );
}

function Gap({
  project: p,
  sources,
  agents,
  models,
  user,
  busy,
  perform,
  reloadCatalog,
  openLibrary,
  gap,
  slot,
}: Props & { gap: string; slot: number }) {
  const format =
    p.suggestion?.result?.outputGaps?.[gap] ??
    (p.suggestion?.result?.outputGap === gap
      ? p.suggestion.result.outputFormat
      : undefined);
  const pins = p.suggestion?.inputSources || [];
  const hasNonCSV = pins.some((pin) => {
    const file = sources.find(
      (f) => f.id === pin.id && f.version === pin.version,
    );
    return file && !file.name.toLowerCase().endsWith(".csv");
  });
  const suitable = (op?: string) =>
    !format ||
    (fileFormat(op) === format && !(op === "aggregate" && hasNonCSV));
  const tools = Object.entries(toolChoices).filter(([key]) => suitable(key));
  const available = [...agents, ...(p.sharedAgents || [])].filter(
    (a) => a.publishedReady && suitable(a.publishedOperation),
  );
  const imageModels = models.filter((m) => m.operation === "png");
  const modelVersion = (m: Model) => m.id + "/" + m.revision;
  const teamImageModels = imageModels.filter(
    (m) => m.team === p.team && !m.credentialBlocked && !m.budgetBlocked,
  );
  const defaultImage =
    teamImageModels.length === 1 ? modelVersion(teamImageModels[0]) : "";
  const [imageChoice, selectImage] = useState(defaultImage);
  const [modelOptions, showModelOptions] = useState(false);
  const [reviewerEmail, setReviewerEmail] = useState(user.email);
  useEffect(() => {
    if (defaultImage && !modelOptions)
      selectImage((current) => current || defaultImage);
  }, [defaultImage, modelOptions]);
  const chosenImage = imageModels.find((m) => modelVersion(m) === imageChoice);
  const [method, setMethod] = useState(
      format === "png" && !available.length
        ? "image"
        : format === "text" && !available.length
          ? "draft"
          : format && !available.length && tools.length
            ? "tool"
            : "reuse",
    ),
    [evidence, setEvidence] = useState("");
  const s = p.suggestion!,
    d = s.result?.drafts?.find((x) => x.gap === gap),
    binding = s.bindings.find((b) => b.slot === slot);
  const boundAgent = binding && agents.find(a => a.id === binding.agent && a.revision === binding.version);
  const [catalogError, setCatalogError] = useState("");
  const needsImageModel = method === "image" && !binding;
  useEffect(() => {
    if (!needsImageModel) return;
    let current = true;
    void reloadCatalog()
      .then(() => {
        if (current) setCatalogError("");
      })
      .catch((error) => {
        if (current) setCatalogError(error.message);
      });
    return () => {
      current = false;
    };
  }, [needsImageModel, reloadCatalog]);
  const [operation, setOperation] = useState<ToolChoice | "">(
    (tools.length === 1 ? (tools[0][0] as ToolChoice) : undefined) ??
      suggestedTool(gap + " " + (d?.instructions || "")) ??
      (/word|docx/i.test(gap) ? "docx" : ""),
  );
  const [trialVersion, setTrialVersion] = useState(0);
  const [picked, setPicked] = useState("");
  const [name, setName] = useState(d?.name || gap),
    [description, setDescription] = useState(d?.instructions || "");

  const url = "projects/" + p.id + "/suggestions/" + s.id + "/gaps/" + slot;
  async function bind(data: FormData) {
    const reuse = data.get("source") === "reuse";
    const a = available.find((a) => a.id === data.get("agent"));
    const m =
      method === "image"
        ? chosenImage
        : models.find((m) => m.id === data.get("resource"));
    await api(url, "PUT", {
      mode: data.get("source"),
      expected: p.revision,
      bindingRevision: binding?.revision || 0,
      ...(reuse
        ? { agent: a?.id, revision: a?.published }
        : {
            tool:
              method === "tool" && operation
                ? describeTool(operation, name, data)
                : undefined,
            draft: {
              name: data.get("name"),
              instructions: data.get("instructions"),
              resource: m?.id,
              resourceRevision: m?.revision,
              key: data.get("key") || undefined,
            },
            ...(method === "draft" && data.has("exact") ? { exact: true } : {}),
            example: data.get("example"),
            expectedOutput: data.get("expected"),
            reviewer: method === "image" ? data.get("reviewer") : undefined,
          }),
    });
    setEvidence("");
    await reloadCatalog();
  }
  const choose = (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const data = new FormData(e.currentTarget);
        void perform(() => bind(data));
      }}
    >
      <label>
        补齐方式
        <select
          value={method}
          name="source"
          onChange={(e) => setMethod(e.target.value)}
        >
          <option value="reuse">复用已发布的 Agent</option>
          {(!format || format === "text") && (
            <option value="draft">配置新的模型 Agent</option>
          )}
          {(!format || format === "png") && (
            <option value="image">创建生图 Agent</option>
          )}
          {tools.length > 0 && <option value="tool">用内置工具补齐能力</option>}
        </select>
      </label>
      {method === "reuse" ? (
        <label>
          已发布能力
          <select
            name="agent"
            required
            value={picked}
            onChange={(e) => setPicked(e.target.value)}
          >
            <option value="">选择 Agent</option>
            {available.map((a) => (
              <option key={a.id} value={a.id}>
                {a.publishedName || a.name} v{a.published}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <>
          <label>
            草稿名称
            <input
              name="name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </label>
          <label>
            输入、处理规则与交付要求
            <textarea
              name="instructions"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              required
            />
          </label>
          {method === "tool" ? (
            <>
              <label>
                工具操作
                <select
                  required
                  value={operation}
                  onChange={(e) => setOperation(e.target.value as ToolChoice)}
                >
                  <option value="">请选择工具操作</option>
                  {tools.map(([value, label]) => (
                    <option value={value} key={value}>
                      {label}
                    </option>
                  ))}
                </select>
              </label>
              {operation === "aggregate" && <AggregateColumns files={[]} />}
              <small>
                草稿归属本项目所在团队。请用可共享的样例验证所选操作。
              </small>
            </>
          ) : method === "image" ? (
            <>
              {chosenImage && !modelOptions ? (
                <div>
                  <small>
                    使用 {chosenImage.name} v{chosenImage.revision} ·{" "}
                    {chosenImage.teamName}承担费用
                  </small>{" "}
                  <button type="button" onClick={() => showModelOptions(true)}>
                    更换生图模型
                  </button>
                </div>
              ) : (
                <>
                  <label>
                    生图模型
                    <select
                      aria-label="生图模型"
                      required
                      value={chosenImage ? imageChoice : ""}
                      onChange={(e) => selectImage(e.target.value)}
                    >
                      <option value="">选择已授权的生图模型</option>
                      {imageModels.map((m) => (
                        <option key={modelVersion(m)} value={modelVersion(m)}>
                          {m.name} v{m.revision} · {m.teamName || "所属团队"}
                          承担费用{m.credentialBlocked && " · 团队凭据待修复"}
                          {m.budgetBlocked && " · 资源额度不足"}
                        </option>
                      ))}
                    </select>
                  </label>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() =>
                      void perform(async () => {
                        await reloadCatalog();
                        setCatalogError("");
                      })
                    }
                  >
                    刷新生图模型
                  </button>
                </>
              )}
              {imageModels.length === 0 && (
                <p>请管理员接入或恢复生图模型，可在配置完成后回到此方案。</p>
              )}
              {catalogError && <p role="alert">{catalogError}</p>}
              {chosenImage &&
                (modelOptions || chosenImage.credentialBlocked) && (
                  <details
                    key={imageChoice}
                    open={chosenImage.credentialBlocked || undefined}
                  >
                    <summary>此 Agent 的专用凭据</summary>
                    <label>
                      模型密钥
                      <input
                        name="key"
                        type="password"
                        autoComplete="new-password"
                      />
                    </label>
                    <small>
                      {chosenImage.credentialBlocked
                        ? "团队凭据待修复。可提供此 Agent 的密钥，或在生成样图前恢复团队凭据。"
                        : "留空时使用团队模型凭据。"}
                    </small>
                  </details>
                )}
              <details>
                <summary>
                  样图验收 ·{" "}
                  {reviewerEmail === user.email
                    ? "我"
                    : reviewerEmail || "请选择"}
                </summary>
                <label>
                  看图验收人
                  <input
                    name="reviewer"
                    required
                    type="email"
                    value={reviewerEmail}
                    onChange={(e) => setReviewerEmail(e.target.value)}
                  />
                </label>
              </details>
              <small>
                样图要求由模型所属团队共享。保存草稿后可单独授权生成样图。
              </small>
            </>
          ) : (
            <>
              <label>
                使用模型
                <select name="resource" required defaultValue="">
                  <option value="">选择已授权模型</option>
                  {models
                    .filter((m) => m.operation === "text")
                    .map((m) => (
                      <option
                        key={m.id}
                        value={m.id}
                        disabled={m.credentialBlocked}
                      >
                        {m.name} v{m.revision}
                        {m.credentialBlocked && " · 团队凭据待修复"}
                        {m.budgetBlocked && " · 资源额度不足"}
                      </option>
                    ))}
                </select>
              </label>
              <label>
                Agent 专用凭据（留空沿用团队配置）
                <input type="password" name="key" autoComplete="off" />
              </label>
            </>
          )}
          <SimilarAgents
            agents={
              format
                ? agents.filter((a) => suitable(a.publishedOperation))
                : agents
            }
            publishedOutput={
              method === "image"
                ? "png"
                : method === "draft"
                  ? "text"
                  : operation || undefined
            }
            source={{ name, description }}
            template={(spec) => {
              setName(spec.name || "");
              setDescription(spec.description || "");
            }}
            choose={(a) => {
              setPicked(a.id);
              setMethod("reuse");
            }}
          />
          <label>
            {method === "image" ? "样图要求" : "合成试运行样例"}
            <textarea name="example" required defaultValue={d?.example} />
          </label>
          <label>
            {method === "image" ? "看图时要确认什么？" : "预期产出应包含"}
            <input name="expected" required defaultValue={d?.expected} />
          </label>
          {method === "draft" && <label>
            <input type="checkbox" name="exact"/>将样例与预期共享给团队，发布前要求整段输出逐字一致
          </label>}
        </>
      )}
      <button disabled={busy || (method === "image" && !chosenImage)}>
        {method === "reuse" ? "绑定所选版本" : "保存为待验证草稿"}
      </button>
    </form>
  );
  return (
    <section aria-label={"能力补齐 · " + gap}>
      <h3>
        {binding?.ready ? "已补齐" : "待补能力"} · {gap}
      </h3>
      {d?.reason && <p>{d.reason}</p>}
      {binding ? (
        <>
          <p>
            {binding.name} v{binding.version} ·{" "}
            {binding.ready
              ? "可用于方案"
              : binding.validated
                ? "已验证，待授权发布"
                : "待验证"}
          </p>
          <details>
            <summary>更换能力</summary>
            {choose}
          </details>
          {!binding.ready && (
            <details>
              <summary>验证并发布此草稿</summary>
              {boundAgent && boundAgent.resource && boundAgent.published !== binding.version && binding.example && (
                <details key={"instructions/" + binding.agent + "/" + binding.version}>
                  <summary>调整本步 Agent 职责</summary>
                  <form onSubmit={e => {
                    e.preventDefault();
                    const instructions = String(new FormData(e.currentTarget).get("instructions"));
                    void perform(async () => {
                      await api(url, "PUT", {mode:"revise", expected:p.revision, bindingRevision:binding.revision,
                        revision:binding.version, instructions});
                      setEvidence("");
                      await reloadCatalog();
                    });
                  }}>
                    <label>调整后的职责<textarea required name="instructions" defaultValue={boundAgent.instructions}/></label>
                    <small>沿用这个缺口的样例、模型和凭据。新修订需在本项目重新验证。</small>
                    <button disabled={busy}>保存新修订到此缺口</button>
                  </form>
                </details>
              )}
              {binding.operation !== "png" && (
                <form
                  key={binding.revision}
                  onSubmit={(e) => {
                    e.preventDefault();
                    const data = new FormData(e.currentTarget);
                    void perform(async () => {
                      const message = await authorizeTrial(
                        binding.agent,
                        binding.version,
                        String(data.get("example")),
                        String(data.get("expected")),
                        binding.exactCheck || undefined,
                        p.id,
                      );
                      setEvidence(message);
                      setTrialVersion((v) => v + 1);
                      await reloadCatalog();
                    });
                  }}
                >
                  <label>
                    验证输入
                    <textarea
                      name="example"
                      required
                      readOnly={!!binding.exactCheck}
                      defaultValue={binding.example}
                    />
                  </label>
                  <label>
                    {binding.exactCheck ? "逐字验收的预期全文" : "验收片段"}
                    <input
                      name="expected"
                      required
                      readOnly={!!binding.exactCheck}
                      defaultValue={binding.expected}
                    />
                  </label>
                  <label>
                    <input type="checkbox" required />
                    授权一次试运行，费用由团队承担，计入本项目预算。
                  </label>
                  <button disabled={busy}>执行缺口试运行</button>
                </form>
              )}
              {(binding.operation !== "png" || binding.imagePayer) && (
                <AgentChecks
                  key={binding.agent + "/" + binding.version}
                  imageSetup={
                    binding.operation === "png" && binding.imagePayer
                      ? {
                          reviewer: user.email,
                          payer: binding.imagePayer,
                          estimate: binding.imageEstimate,
                        }
                      : undefined
                  }
                  project={p.id}
                  agent={binding.agent}
                  revision={binding.version}
                  refresh={reloadCatalog}
                />
              )}
              {(binding.operation === "text" || boundAgent?.tool) && (
                <TrialEvidence
                  key={binding.agent}
                  agent={binding.agent}
                  refresh={reloadCatalog}
                  version={trialVersion}
                />
              )}
              {evidence && <p role="status">{evidence}</p>}
              {!binding.validated && boundAgent?.release?.state === "pending" && (
                <small>当前验证已失效，请先复验。</small>
              )}
              {boundAgent && user.role !== "admin" && (binding.validated || boundAgent.release) && (
                <RequestRelease agent={boundAgent} refresh={() => perform(reloadCatalog)} />
              )}
              {binding.validated && user.role === "admin" && (
                <button
                  disabled={busy}
                  onClick={() => perform(async () => {
                    await api("agents/" + binding.agent + "/publish", "POST", { revision: binding.version });
                    await reloadCatalog();
                  })}
                >
                  发布此能力修订
                </button>
              )}
            </details>
          )}
        </>
      ) : (
        choose
      )}
      <button type="button" onClick={openLibrary}>
        查看能力库与工具连接器
      </button>
    </section>
  );
}

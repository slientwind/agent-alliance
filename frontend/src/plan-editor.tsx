import { CandidateComparison } from "./candidate-comparison";
import { useState } from "react";
import type { PlanAgent, Project, WorkflowTemplateUpdate } from "./client";
import type { Source, Preference } from "./knowledge";
import { outputOperations } from "./output-operations";

type Step = {
  lookup?: boolean;
  calculate?: boolean;
  followUp?: boolean;
  toolChoice?: {agent: string; name: string; revision: number; selector: string};
  key: string;
  agent: string;
  name: string;
  revision: number;
  inputs: string[];
  requiredOperation?: string;
  operation?: string;
  outline?: boolean;
  vision?: boolean;
  editsImage?: boolean;
  completion?: string;
  original?: { step: number; agent: string; revision: number };
};
export type PlanDraft = {
  baseRevision: number;
  baseVersion: number;
  brief: string;
  steps: Step[];
  documents: Record<string, number>;
  rules: Record<string, number>;
  allowPartial?: boolean;
  templateUpdate?: {
    fromVersion: number;
    toVersion: number;
    requiresSources: boolean;
  };
};

export function currentPlan(project: Project): PlanDraft {
  const steps: Step[] = [];
  const keys: string[] = [];
  const template = project.plan ? null : project.workflowTemplate;
  const goal = project.messages.filter((m) => m.kind === "user").map((m) => m.body).join("\n");
  for (const node of project.plan?.nodes ?? template?.nodes ?? []) {
    if (node.lookupPhase === "answer" || node.lookupPhase === "answer_calc" || node.lookupPhase === "synthesize") {
      const step = steps[steps.length - 1];
      if (node.lookupPhase === "synthesize") step.followUp = true;
      else step.calculate = node.lookupPhase === "answer_calc";
      keys.push(keys[keys.length - 1]);
      continue;
    }
    const key = String(steps.length + 1);
    steps.push({
      key,
      agent: node.agent,
      name: node.name || "待选择 Agent",
      revision: node.revision,
      requiredOperation: template ? node.operation : undefined,
      operation: node.operation,
      outline: "outline" in node ? node.outline : undefined,
      vision: node.vision,
      editsImage: node.editsImage,
      original: template ? undefined : { step: steps.length + 1, agent: node.agent, revision: node.revision },
      lookup: node.lookupPhase === "search",
      toolChoice: node.toolChoice
        ? {...node.toolChoice, name: node.toolChoice.name || "待选择备选工具", selector: keys[node.toolChoice.selector - 1]}
        : undefined,
      inputs: node.dependsOn.map((n) => keys[n - 1]),
    });
    keys.push(key);
  }
  if (project.plan && project.deliveryCoverage) {
    for (const item of project.deliveryCoverage.items) {
      const step = steps.find(s => s.key === keys[item.step - 1]);
      if (step) step.completion = [step.completion, item.label].filter(Boolean).join("\n");
    }
  }
  return {
    baseRevision: project.revision,
    baseVersion: project.planVersion,
    brief: project.plan?.brief ?? (template ? [goal, template.instructions, `输入约定\n${template.inputs}`, `交付约定\n${template.outcomes}`].join("\n\n") : goal),
    steps,
    allowPartial: project.plan ? project.deliveryCoverage?.allowPartial : undefined,
    documents: Object.fromEntries(
      (project.plan?.sources ?? []).map((s) => [s.id, s.version]),
    ),
    rules: Object.fromEntries(
      (project.plan?.preferences ?? []).map((s) => [s.id, s.revision]),
    ),
  };
}

export function planFromTemplateUpdate(project: Project, update: WorkflowTemplateUpdate): PlanDraft {
  const previous = currentPlan(project);
  const updated = currentPlan({ ...project, plan: null, workflowTemplate: update.target });
  return {
    ...updated,
    steps: updated.steps.map((step, i) => ({
      ...step,
      completion: previous.steps[i]?.completion,
    })),
    allowPartial: previous.allowPartial,
    documents: Object.fromEntries((project.plan?.sources ?? []).map(source => [source.id, source.version])),
    rules: Object.fromEntries((project.plan?.preferences ?? []).map(pref => [pref.id, pref.revision])),
    templateUpdate: {
      fromVersion: update.fromVersion,
      toVersion: update.target.version,
      requiresSources: update.target.requiresSources,
    },
  };
}

function DraftSummary({
  value,
  sources,
  preferences,
}: {
  value: PlanDraft;
  sources: Source[];
  preferences: Preference[];
}) {
  return (
    <>
      <p className="preserve">{value.brief}</p>
      <ol>
        {value.steps.map((s) => (
          <li key={s.key}>
            {s.name} · v{s.revision}
            {s.vision && " · 读取图片"}
            {s.editsImage && " · 编辑参考图"}
            {s.toolChoice && " 或 " + s.toolChoice.name + " · v" + s.toolChoice.revision}
            {s.lookup && (s.calculate ? " · 引文金额核算" : " · 检索原文后回答")} ·{" "}
            {s.followUp && "补查有新依据时续答 · "}
            {s.inputs.length
              ? "引用步骤 " +
                s.inputs
                  .map((k) => value.steps.findIndex((t) => t.key === k) + 1)
                  .join("、")
              : "可独立开始"}
            {s.completion && <p className="preserve">{s.completion}</p>}
          </li>
        ))}
      </ol>
      {Object.entries(value.documents)
        .filter(([, v]) => v > 0)
        .map(([id, v]) => (
          <small key={id}>
            资料 · {sources.find((s) => s.id === id)?.name || "原绑定资料"} · v
            {v}
          </small>
        ))}
      {Object.entries(value.rules)
        .filter(([, v]) => v > 0)
        .map(([id, v]) => (
          <small key={id}>
            偏好 · {preferences.find((s) => s.id === id)?.body || "原绑定偏好"}{" "}
            · v{v}
          </small>
        ))}
    </>
  );
}

function StepAgentReplacement({
  step,
  index,
  steps,
  agents,
  projectTeam,
  onReplace,
}: {
  step: Step;
  index: number;
  steps: Step[];
  agents: PlanAgent[];
  projectTeam: string;
  onReplace: (agent: PlanAgent) => void;
}) {
  const selectsTool = steps.some(other => other.toolChoice?.selector === step.key);
  const needsModel = step.lookup || selectsTool;
  const scopedToProjectTeam = !!step.toolChoice || selectsTool;
  const options = agents.filter(agent =>
    agent.publishedOperation === step.operation &&
    !!agent.publishedVision === !!step.vision &&
    !!agent.publishedImageEdit === !!step.editsImage &&
    (step.operation !== "pptx" || !!agent.publishedOutline === !!step.outline) &&
    (!needsModel || agent.publishedLookup) &&
    (!scopedToProjectTeam || agent.team === projectTeam) &&
    agent.id !== step.toolChoice?.agent &&
    (agent.id !== step.agent || agent.published !== step.revision),
  );
  const affected = new Set([step.key]);
  const downstream = steps.flatMap((other, i) => {
    if (i <= index || !other.inputs.some(key => affected.has(key))) return [];
    affected.add(other.key);
    return [i + 1];
  });
  return (
    <div className="step-replacement">
      {step.operation && <small>{outputOperations[step.operation]}</small>}
      {needsModel && <small>此步骤需要可执行原文核对或工具选择的文本模型。</small>}
      {scopedToProjectTeam && <small>工具选择使用项目所属团队的能力。</small>}
      {step.outline && <small>需要接收分页提纲的课件 Agent。</small>}
      {options.length > 0 ? (
        <label>
          步骤 {index + 1} 的兼容 Agent
          <select value="" onChange={event => {
            const next = options.find(agent => agent.id + "/" + agent.published === event.target.value);
            if (next) onReplace(next);
          }}>
            <option value="">选择已发布修订</option>
            {options.map(agent => (
              <option key={agent.id + "/" + agent.published} value={agent.id + "/" + agent.published}>
                {agent.publishedName || agent.name} · v{agent.published}
              </option>
            ))}
          </select>
        </label>
      ) : <small role="status">当前没有满足这一步输入与成果约定的其他已发布修订。</small>}
      {downstream.length > 0 && <small>步骤 {downstream.join("、")} 将使用替换后的结果。</small>}
      <small>保存方案后重新核对费用与执行授权。</small>
    </div>
  );
}

type Props = {
  project: Project;
  draft?: PlanDraft;
  change: (next?: PlanDraft) => void;
  agents: PlanAgent[];
  sources: Source[];
  preferences: Preference[];
  busy: boolean;
  openLibrary: () => void;
  save: (body: object) => Promise<void>;
};

export function PlanEditor({
  project,
  draft,
  change,
  agents,
  sources,
  preferences,
  busy,
  openLibrary,
  save,
}: Props) {
  const [replacing, setReplacing] = useState("");
  const latest = currentPlan(project);
  const value = draft ?? latest;
  const { steps, documents, rules } = value;
  const requiredItems = steps.flatMap((step, index) =>
    (step.completion || "").split("\n").map(line => line.trim()).filter(Boolean)
      .map(label => ({ step: index + 1, label })),
  );
  const invalidDeliveryContract = requiredItems.length > 32 ||
    requiredItems.some(item => Array.from(item.label).length > 180) ||
    !!value.allowPartial && requiredItems.length === 0;
  const replacements = steps.flatMap(({ original, agent, revision }, i) =>
    original && (original.agent !== agent || original.revision !== revision)
      ? [{ from: original.step, step: i + 1 }]
      : [],
  );
  const staleReplacement = replacements.length > 0 && value.baseVersion !== project.planVersion;
  const hasSources = Object.values(documents).some((v) => v > 0);
  const calls = steps.length + steps.filter(s => s.lookup).length + steps.filter(s => s.lookup && s.followUp).length;
  const selectedFiles = Object.entries(documents).filter(([, version]) => version > 0);
  const picturePin = (id: string, version: number) =>
    sources.some(source => source.id === id && source.version === version && source.picture) ||
    project.plan?.sources.some(source => source.id === id && source.version === version && source.picture);
  const editsImage = steps.some(step => step.editsImage);
  const usesPictures = editsImage || steps.some(step => step.vision);
  const pictureCount = selectedFiles.filter(([id, version]) => picturePin(id, version)).length;
  const hasText = selectedFiles.some(([id, version]) => !picturePin(id, version));
  const missingLookupSources = !hasText && steps.some((s) => s.lookup);
  const needsPictureAgent = pictureCount > 0 && !usesPictures;
  const tooManyReferences = editsImage && pictureCount > 1;
  const missingTemplateSources = !hasSources && (value.templateUpdate?.requiresSources ?? project.workflowTemplate?.requiresSources);
  const csvOnly = selectedFiles.length > 0 && selectedFiles.every(([id]) =>
    /\.csv$/i.test(sources.find(source => source.id === id)?.name || ""),
  );
  function selectorFor(step: Step) {
    if (step.inputs.length !== 1) return;
    const selector = steps.find(item => item.key === step.inputs[0]);
    if (selector && !selector.inputs.length && !selector.lookup && !selector.toolChoice &&
        agents.some(a => a.id === selector.agent && a.published === selector.revision && a.publishedLookup)) {
      return selector;
    }
  }
  const invalidToolChoice = steps.some(step => step.toolChoice &&
    (!csvOnly || selectorFor(step)?.key !== step.toolChoice.selector ||
      (step.requiredOperation && !agents.some(a => a.id === step.toolChoice?.agent && a.published === step.toolChoice.revision))));
  const unavailableTemplateStep = steps.some(step => step.requiredOperation && !agents.some(agent =>
    agent.id === step.agent && agent.published === step.revision && agent.publishedOperation === step.requiredOperation &&
    !!agent.publishedVision === !!step.vision && !!agent.publishedImageEdit === !!step.editsImage && (!step.lookup || agent.publishedLookup),
  ));
  const staleUpdate = value.templateUpdate && (project.workflowTemplate?.version !== value.templateUpdate.fromVersion ||
    project.workflowTemplate?.updateVersion !== value.templateUpdate.toVersion || value.baseRevision !== project.revision);
  const stale = (!!draft && draft.baseRevision !== project.revision) || staleUpdate || staleReplacement;
  const editable = ["brief", "plan"].includes(project.stage);
  function edit(patch: Partial<PlanDraft>) {
    change({ ...value, ...patch });
  }
  function addStep(agent: string, name: string, revision: number) {
    const selected = agents.find(item => item.id === agent && item.published === revision);
    edit({
      steps: [
        ...steps,
        {
          key: crypto.randomUUID(),
          agent,
          name,
          revision,
          operation: selected?.publishedOperation,
          outline: selected?.publishedOutline,
          vision: selected?.publishedVision,
          editsImage: selected?.publishedImageEdit,
          inputs: steps.length ? [steps[steps.length - 1].key] : [],
        },
      ],
    });
  }
  function replaceStep(step: Step, agent: PlanAgent) {
    edit({
      steps: steps.map(current => current.key === step.key ? {
        ...current,
        agent: agent.id,
        name: agent.publishedName || agent.name,
        revision: agent.published,
        operation: current.operation ?? agent.publishedOperation,
        outline: current.outline ?? agent.publishedOutline,
      } : current),
    });
    setReplacing("");
  }
  if (!editable)
    return draft ? (
      <section>
        <p role="status">项目已离开方案编辑阶段。这份本页草稿可供查阅。</p>
        <DraftSummary
          value={draft}
          sources={sources}
          preferences={preferences}
        />
        <button type="button" onClick={() => change()}>
          丢弃本页草稿
        </button>
      </section>
    ) : null;
  // Preserve explicit selections, including stale bindings until their owner removes or replaces them.
  const sourceIds = Array.from(
    new Set([...sources.map((s) => s.id), ...Object.keys(documents)]),
  );
  const ruleIds = Array.from(
    new Set([...preferences.map((s) => s.id), ...Object.keys(rules)]),
  );
  if (!agents.length && !steps.length && !draft)
    return (
      <>
        <CandidateComparison key={project.id} project={project.id} />
        <p>团队暂无已发布能力。创建并验证第一个 Agent 后可以继续此对话。</p>
        <button onClick={openLibrary}>打开团队能力库</button>
      </>
    );
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (stale || busy || !steps.length || needsPictureAgent || tooManyReferences || missingLookupSources || missingTemplateSources || invalidToolChoice || unavailableTemplateStep || invalidDeliveryContract) return;
        void save({
          expected: value.baseRevision,
          ...(replacements.length > 0 && { replacements, replacementBase: value.baseVersion }),
          calculationSteps: steps.flatMap((step, i) => step.lookup && step.calculate ? [i + 1] : []),
          followUpSteps: steps.flatMap((step, i) => step.lookup && step.followUp ? [i + 1] : []),
          lookupSteps: steps.flatMap((step, i) => (step.lookup ? [i + 1] : [])),
          brief: value.brief,
          agents: steps.map((s) => s.agent),
          agentRevisions: steps.map((s) => s.revision),
          deliveryContract: { allowPartial: !!value.allowPartial, items: requiredItems },
          dependencies: steps.map((s) =>
            s.inputs.map((key) => steps.findIndex((t) => t.key === key) + 1),
          ),
          toolChoices: steps.flatMap((step, i) => step.toolChoice ? [{
            step: i+1,
            selector: steps.findIndex(target => target.key === step.toolChoice!.selector)+1,
            alternative: step.toolChoice.agent,
            revision: step.toolChoice.revision,
          }] : []),
          sources: Object.entries(documents)
            .filter(([, version]) => version)
            .map(([id, version]) => ({ id, version })),
          preferences: Object.entries(rules)
            .filter(([, revision]) => revision)
            .map(([id, revision]) => ({ id, revision })),
        });
      }}
    >
      {draft && (
        <small role="status">
          {draft.templateUpdate ? `模板 v${draft.templateUpdate.toVersion} 的修改暂存在本页 · 基于` : "方案草稿仅在本页暂存 · 基于"}
          {draft.baseVersion ? "方案 v" + draft.baseVersion : "初始需求"} ·
          项目修订 {draft.baseRevision}
        </small>
      )}
      {stale && (
        <section className="card">
          <strong>项目已更新，请核对后继续编辑</strong>
          <p>
            当前项目修订 {project.revision}
            。下面是当前已保存内容，编辑区保留你的草稿。
          </p>
          <DraftSummary
            value={latest}
            sources={sources}
            preferences={preferences}
          />
          {staleReplacement ? (
            <p>原方案已另存新版。请先核对新版，再选择要替换的步骤。</p>
          ) : value.templateUpdate ? <p>可查阅或丢弃这份草稿，再比较当前可用的模板版本。</p> : <button
            type="button"
            disabled={busy}
            onClick={() =>
              edit({
                baseRevision: project.revision,
                baseVersion: project.planVersion,
              })
            }
          >
            已核对，继续使用我的草稿
          </button>}
        </section>
      )}
      <fieldset className="plan-fields" disabled={busy}>
        <label>
          交付目标与确认要求
          <textarea
            name="brief"
            required
            value={value.brief}
            onChange={(e) => edit({ brief: e.target.value })}
          />
        </label>
        {!!steps.length && (
          <details open={requiredItems.length || value.allowPartial ? true : undefined}>
            <summary>必交项与缺项处理</summary>
            {steps.map((step, i) => (
              <label key={step.key}>
                步骤 {i + 1} · {step.name} 的必交项
                <textarea rows={2} value={step.completion || ""} placeholder="每行填写一个要核对的对象或条件"
                  onChange={e => edit({ steps: steps.map(current => current.key === step.key
                    ? { ...current, completion: e.target.value } : current) })} />
              </label>
            ))}
            <label>
              <input type="checkbox" checked={!!value.allowPartial} onChange={e => edit({ allowPartial: e.target.checked })} />
              缺项时允许部分交付，并为每项指定接手人及继续条件
            </label>
            {invalidDeliveryContract && <small role="status">最多 32 项，每项 180 字。部分交付需要至少一个必交项。</small>}
          </details>
        )}
        <label>
          添加执行步骤
          <select
            value=""
            disabled={calls >= 12}
            onChange={(e) => {
              const a = agents.find((item) => item.id === e.target.value);
              if (a) addStep(a.id, a.publishedName || a.name, a.published);
            }}
          >
            <option value="">选择 Agent</option>
            {agents.map((a) => (
              <option key={a.id} value={a.id}>
                {a.publishedName || a.name} · 已发布 v{a.published}{a.publishedVision && " · 读取图片"}
                {a.publishedImageEdit && " · 编辑参考图"}
              </option>
            ))}
          </select>
        </label>
        {missingLookupSources && (
          <small role="status">原文核对需要至少引用一份可读资料。</small>
        )}
        {needsPictureAgent && <small role="status">请选择能读取图片的 Agent，或取消引用图片。</small>}
        {usesPictures && !pictureCount && <small role="status">执行前请引用 PNG 或 JPEG 图片。</small>}
        {tooManyReferences && <small role="status">参考图编辑只接受一张图片。</small>}
        {missingTemplateSources && !missingLookupSources && <small role="status">请引用本次任务的可读资料。</small>}
        {invalidToolChoice && (
          <small role="status">自动选择需要已引用的 CSV，并依赖一个可独立开始的已发布文本 Agent。</small>
        )}
        {unavailableTemplateStep && (
          <small role="status">原模板有待替换或复验的 Agent，请核对执行步骤。</small>
        )}
        <CandidateComparison
          key={project.id}
          project={project.id}
          add={addStep}
          disabled={calls >= 12}
        />
        <ol className="plan-steps">
          {steps.map((s, i) => {
            const selector = selectorFor(s);
            const primary = agents.find(a => a.id === s.agent && a.published === s.revision);
            const alternatives = primary?.publishedOperation === "aggregate" && selector
              ? agents.filter(a => a.id !== s.agent && a.publishedOperation === "aggregate")
              : [];
            const operation = s.operation || primary?.publishedOperation;
            const canReplace = !s.requiredOperation && !!operation;
            return (
            <li key={s.key}>
              <strong>{s.name}</strong>{s.revision > 0 && s.agent && " · v" + s.revision}{" "}
              {s.vision && <small>读取图片 · 文本成果</small>}
              {s.editsImage && <small>编辑参考图 · PNG 成果</small>}
              {canReplace && (
                <button
                  type="button"
                  aria-label={"更换步骤 " + (i + 1) + " 的 Agent"}
                  aria-expanded={replacing === s.key}
                  onClick={() => setReplacing(replacing === s.key ? "" : s.key)}
                >
                  更换 Agent
                </button>
              )}
              {canReplace && replacing === s.key && (
                <StepAgentReplacement
                  step={{ ...s, operation, outline: s.outline ?? primary?.publishedOutline }}
                  index={i}
                  steps={steps}
                  agents={agents}
                  projectTeam={project.team}
                  onReplace={(agent) => replaceStep(s, agent)}
                />
              )}
              {s.requiredOperation && (
                <label>
                  步骤 {i + 1} 执行 Agent
                  <select
                    value={primary ? s.agent + "/" + s.revision : ""}
                    onChange={(e) => {
                      const replacement = agents.find(agent => agent.id + "/" + agent.published === e.target.value);
                      if (replacement) edit({ steps: steps.map(t => t.key === s.key ? {
                        ...t, agent: replacement.id, name: replacement.publishedName || replacement.name, revision: replacement.published,
                      } : t) });
                    }}
                  >
                    <option value="" disabled>选择当前可用的 Agent</option>
                    {agents.filter(a => a.publishedOperation === s.requiredOperation && !!a.publishedVision === !!s.vision && !!a.publishedImageEdit === !!s.editsImage && (!s.lookup || a.publishedLookup)).map(a => (
                      <option key={a.id} value={a.id + "/" + a.published}>{a.publishedName || a.name} · v{a.published}</option>
                    ))}
                  </select>
                  <small>{outputOperations[s.requiredOperation]}</small>
                </label>
              )}
              {(s.lookup ||
                (hasText &&
                  agents.some(
                    (a) =>
                      a.id === s.agent &&
                      a.published === s.revision &&
                      a.publishedLookup,
                  ))) && (
                <label>
                  <input
                    type="checkbox"
                    checked={!!s.lookup}
                    disabled={!s.lookup && calls >= 12}
                    onChange={(e) =>
                      edit({
                        steps: steps.map((t) =>
                          t.key === s.key
                            ? { ...t, lookup: e.target.checked, calculate: false, followUp: false }
                            : t,
                        ),
                      })
                    }
                  />
                  检索原文后回答（最多两次调用）
                </label>
              )}
              {s.lookup && (
                <label>
                  <input type="checkbox" checked={!!s.calculate} onChange={e =>
                    edit({steps: steps.map(t => t.key === s.key ? {...t, calculate: e.target.checked, followUp: false} : t)})
                  } />
                  允许按原文金额求和或算差额
                </label>
              )}
              {s.lookup && !s.calculate && (
                <label>
                  <input type="checkbox" checked={!!s.followUp} disabled={!s.followUp && calls >= 12}
                    onChange={e => edit({steps: steps.map(t => t.key === s.key ? {...t, followUp: e.target.checked} : t)})} />
                  补查有新依据时续答（最多追加一次调用）
                </label>
              )}
              {(s.toolChoice || alternatives.length > 0) && (
                <details open={s.toolChoice ? true : undefined}>
                  <summary>允许 Agent 在两个汇总工具中选择</summary>
                  <label>
                    自动选择的备选汇总工具（步骤 {i+1}）
                    <select
                      value={s.toolChoice ? s.toolChoice.agent + "/" + s.toolChoice.revision : ""}
                      onChange={(e) => {
                        const alternative = alternatives.find(a => a.id + "/" + a.published === e.target.value);
                        edit({steps: steps.map(t => t.key === s.key ? {
                          ...t,
                          toolChoice: alternative && selector ? {
                            agent: alternative.id, revision: alternative.published,
                            name: alternative.publishedName || alternative.name, selector: selector.key,
                          } : undefined,
                        } : t)});
                      }}
                    >
                      <option value="">固定使用 {s.name}</option>
                      {s.toolChoice && !alternatives.some(a => a.id === s.toolChoice?.agent && a.published === s.toolChoice?.revision) && (
                        <option value={s.toolChoice.agent + "/" + s.toolChoice.revision}>{s.toolChoice.name} v{s.toolChoice.revision}（原绑定）</option>
                      )}
                      {alternatives.map(a => (
                        <option key={a.id} value={a.id + "/" + a.published}>{a.publishedName || a.name} v{a.published}</option>
                      ))}
                    </select>
                  </label>
                  <small>前一步的文本 Agent 只读取目标和 CSV 表头，从两个版本中选一个。汇总仍交付 XLSX。</small>
                </details>
              )}
              <button
                type="button"
                aria-label={"移除步骤 " + (i + 1)}
                onClick={() =>
                  edit({
                    steps: steps
                      .filter((t) => t.key !== s.key)
                      .map((t) => ({
                        ...t,
                        inputs: t.inputs.filter((key) => key !== s.key),
                      })),
                  })
                }
              >
                移除
              </button>
              {i > 0 && (
                <details>
                  <summary>
                    {s.inputs.length
                      ? "引用步骤 " +
                        s.inputs
                          .map(
                            (key) => steps.findIndex((t) => t.key === key) + 1,
                          )
                          .join("、")
                      : "可独立开始"}{" "}
                    · 调整
                  </summary>
                  {steps.slice(0, i).map((prev, j) => (
                    <label key={prev.key}>
                      <input
                        type="checkbox"
                        checked={s.inputs.includes(prev.key)}
                        onChange={(e) => {
                          const inputs = e.target.checked
                            ? steps
                                .slice(0, i)
                                .map((t) => t.key)
                                .filter(
                                  (key) =>
                                    key === prev.key || s.inputs.includes(key),
                                )
                            : s.inputs.filter((key) => key !== prev.key);
                          edit({
                            steps: steps.map((t) =>
                              t.key === s.key ? { ...s, inputs } : t,
                            ),
                          });
                        }}
                      />
                      引用步骤 {j + 1} · {prev.name}
                    </label>
                  ))}
                </details>
              )}
            </li>
          );
          })}
        </ol>
        {sourceIds.map((id) => {
          const source = sources.find((s) => s.id === id),
            pinned = documents[id] || 0;
          const usable = source && !source.revoked && (source.readable || source.picture);
          if (!pinned && !usable) return null;
          return (
            <label key={id}>
              引用资料 · {source?.name || "原绑定资料"}
              <select
                value={pinned}
                onChange={(e) =>
                  edit({
                    documents: { ...documents, [id]: Number(e.target.value) },
                  })
                }
              >
                <option value={0}>不引用</option>
                {pinned > 0 &&
                  (source?.version !== pinned ||
                    !usable) && (
                    <option value={pinned}>原绑定 v{pinned}</option>
                  )}
                {source && usable && (
                  <option value={source.version}>v{source.version}{source.picture && " · 图片"}</option>
                )}
              </select>
            </label>
          );
        })}
        {ruleIds.map((id) => {
          const pref = preferences.find((f) => f.id === id),
            pinned = rules[id] || 0,
            valid = pref?.enabled && Date.parse(pref.expiresAt) > Date.now();
          if (!valid && !pinned) return null;
          return (
            <label key={id}>
              个人偏好 · {pref?.body || "原绑定偏好"}
              <select
                value={pinned}
                onChange={(e) =>
                  edit({ rules: { ...rules, [id]: Number(e.target.value) } })
                }
              >
                <option value={0}>不采用</option>
                {pinned > 0 && (!valid || pref?.revision !== pinned) && (
                  <option value={pinned}>原绑定 v{pinned} · 需重新选择</option>
                )}
                {valid && (
                  <option value={pref.revision}>v{pref.revision}</option>
                )}
              </select>
            </label>
          );
        })}
      </fieldset>
      <div className="actions">
        <button disabled={busy || stale || !steps.length || needsPictureAgent || tooManyReferences || missingLookupSources || missingTemplateSources || invalidToolChoice || unavailableTemplateStep || invalidDeliveryContract}>
          {value.templateUpdate ? "采用新版并保存待确认方案" : "保存待确认方案"}
        </button>
        {draft && (
          <button type="button" disabled={busy} onClick={() => change()}>
            丢弃草稿，使用当前内容
          </button>
        )}
      </div>
    </form>
  );
}

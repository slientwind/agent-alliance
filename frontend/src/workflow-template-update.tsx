import { useState } from "react";
import { api, type Project, type WorkflowTemplate, type WorkflowTemplateReference, type WorkflowTemplateUpdate } from "./client";
import { fileFormat, plannedOutputs } from "./output-operations";

type Step = Pick<WorkflowTemplate["nodes"][number], "agent" | "name" | "revision" | "operation" | "lookupPhase" | "dependsOn"> & {
  toolChoice?: { agent: string; name: string; revision: number; selector: number } | null;
};

const fields = [
  ["name", "名称"], ["instructions", "执行要求"], ["inputs", "输入约定"], ["outcomes", "交付约定"],
] as const;

const phases = { search: "检索原文", answer: "据原文回答", answer_calc: "引文核算", synthesize: "有新增原文时续答", "": "" };

function StepDescription({ step }: { step?: Step }) {
  if (!step) return <span>无此步骤</span>;
  const format = step.operation === "aggregate" ? "XLSX 汇总工作簿" : plannedOutputs[fileFormat(step.operation) ?? ""];
  return <span>
    {step.name ? `${step.name} · v${step.revision}` : "待选择 Agent"}
    <small>{format || step.operation}{step.lookupPhase && " · " + phases[step.lookupPhase]} · {step.dependsOn.length ? "引用步骤 " + step.dependsOn.join("、") : "可独立开始"}</small>
    {step.toolChoice && <small>备选 {step.toolChoice.name || "待选择工具"}{step.toolChoice.name && " · v" + step.toolChoice.revision}，由步骤 {step.toolChoice.selector} 选择</small>}
  </span>;
}

function sameStep(old?: Step, next?: Step) {
  if (!old || !next) return old === next;
  const signature = (step: Step) => [step.agent, step.revision, step.operation, step.lookupPhase || "", step.dependsOn,
    step.toolChoice ? [step.toolChoice.agent, step.toolChoice.revision, step.toolChoice.selector] : null];
  return JSON.stringify(signature(old)) === JSON.stringify(signature(next));
}

export function WorkflowTemplateVersionReview({ project, owner, hasDraft, busy, refresh, prepare, openTemplate }: {
  project: Project;
  owner: boolean;
  hasDraft: boolean;
  busy: boolean;
  refresh: () => Promise<void>;
  prepare: (update: WorkflowTemplateUpdate) => void;
  openTemplate: (ref: WorkflowTemplateReference) => void;
}) {
  const [preview, setPreview] = useState<WorkflowTemplateUpdate>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const current = project.workflowTemplate;
  if (!current?.updateVersion && !preview) return null;
  if (!current) return null;
  const stale = preview && (preview.fromVersion !== current.version || preview.target.version !== current.updateVersion ||
    preview.planVersion !== project.planVersion || preview.projectRevision !== project.revision);
  const baseNodes = project.plan?.nodes ?? current.nodes;
  const changed = preview && Array.from({ length: Math.max(baseNodes.length, preview.target.nodes.length) }, (_, i) => i)
    .filter(i => preview.changedSteps.includes(i + 1) || !sameStep(baseNodes[i], preview.target.nodes[i]));

  async function load() {
    setLoading(true);
    setError("");
    try {
      await refresh();
      setPreview(await api<WorkflowTemplateUpdate>(`projects/${project.id}/workflow-template-update`));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <section className="card" aria-label="模板版本对比">
      <div className="actions">
        <button type="button" onClick={() => void load()} disabled={loading || busy || !current.updateVersion}>
          {preview ? "重新比较版本" : `查看模板新版 v${current.updateVersion}`}
        </button>
        {preview && <button type="button" onClick={() => { setPreview(undefined); setError(""); }}>收起对比</button>}
      </div>
      {loading && <p role="status">正在读取模板版本…</p>}
      {error && <p role="alert">{error}</p>}
      {preview && (
        <>
          <h3>模板 v{preview.fromVersion} 与 v{preview.target.version}</h3>
          {!preview.published && <p>新版是你维护的个人草稿。</p>}
          {preview.target.manifestDigest && <small>新版构建清单 {preview.target.manifestDigest.slice(0, 12)}</small>}
          {stale && <p role="status">项目或可用版本已变化，请重新比较。</p>}
          {fields.filter(([field]) => current[field] !== preview.target[field]).map(([field, name]) => (
            <div key={field}>
              <strong>{name}</strong>
              <div className="preserve">原版 · {current[field]}</div>
              <div className="preserve">新版 · {preview.target[field]}</div>
            </div>
          ))}
          {current.requiresSources !== preview.target.requiresSources && (
            <p>{preview.target.requiresSources ? "新版要求引用本项目资料。" : "新版可以根据文字目标设置方案。"}</p>
          )}
          {changed?.length ? (
            <ol>
              {changed.map(i => <li key={i} value={i + 1}>
                <div>{project.plan ? "当前方案" : "原模板"} · <StepDescription step={baseNodes[i]} /></div>
                <div>新版 · <StepDescription step={preview.target.nodes[i]} /></div>
                {sameStep(baseNodes[i], preview.target.nodes[i]) && <small>此步骤的构建清单已变化。</small>}
              </li>)}
            </ol>
          ) : <p>步骤及依赖一致。</p>}
          <p>能力调用上限 · 当前 {baseNodes.length} 次，新版 {preview.target.nodes.length} 次。保存时重新核对能力，执行前查看费用。</p>
          {preview.target.nodes.some(step => !step.ready || (step.toolChoice && !step.toolChoice.ready)) && <p>新版有待核对的 Agent，可在方案中选择当前可用的能力。</p>}
          {preview.canAdopt ? (
            <>
              {hasDraft ? <p>请先保存或丢弃当前编辑中的草稿。</p> : <p>新版草稿沿用本项目的资料和偏好选择。可在保存前核对本次目标、人员和执行步骤。</p>}
              <button type="button" className="primary" disabled={!!stale || busy || loading || hasDraft} onClick={() => prepare(preview)}>
                按新版编辑方案
              </button>
            </>
          ) : owner && <>
            <p>当前任务已受理。可以用新版模板开启一项新任务。</p>
            <button type="button" disabled={!!stale || busy || loading} onClick={() => openTemplate(preview.target)}>用新版开启新对话</button>
          </>}
        </>
      )}
    </section>
  );
}

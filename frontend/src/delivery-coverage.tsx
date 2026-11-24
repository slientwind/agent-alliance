import { useState } from "react";
import type { CoverageItem, Project } from "./client";

export type Assessment = {
  expected: number;
  item: number;
  run: string;
  artifact?: string;
  version?: number;
  state: "covered" | "missing";
  reason: string;
  responsible?: string;
  continueWhen?: string;
};

function RequirementAssessment({
  item, project, busy, save,
}: {
  item: CoverageItem;
  project: Project;
  busy: boolean;
  save: (data: Assessment) => void;
}) {
  const [state, setState] = useState<Assessment["state"] | "">(
    item.state === "unchecked" ? "" : item.state,
  );
  const [responsible, setResponsible] = useState(item.responsibleActive ? item.responsible || "" : "");
  return (
    <details>
      <summary>核对此必交项{item.version ? " · v" + item.version : ""}</summary>
      <form onSubmit={event => {
        event.preventDefault();
        if (!state) return;
        const fields = new FormData(event.currentTarget);
        save({
          expected: project.revision,
          item: item.item,
          run: item.run!,
          artifact: item.artifact,
          version: item.version,
          state,
          reason: String(fields.get("reason") || "").trim(),
          responsible: state === "missing" && responsible ? responsible : undefined,
          continueWhen: state === "missing" && responsible ? String(fields.get("continueWhen") || "").trim() : undefined,
        });
      }}>
        <label>
          核对结果
          <select required value={state} onChange={e => {
            const value = e.target.value;
            if (value === "" || value === "missing" || value === "covered") setState(value);
          }}>
            <option value="">请选择核对结果</option>
            {item.artifact && <option value="covered">已满足</option>}
            <option value="missing">仍缺项</option>
          </select>
        </label>
        <label>
          {state === "missing" ? "缺少什么" : "依据位置或说明"}
          <textarea name="reason" rows={2} maxLength={1000} defaultValue={item.reason || ""} required />
        </label>
        {state === "missing" && project.deliveryCoverage?.allowPartial && (
          <>
            <label>
              接手人
              <select value={responsible} onChange={e => setResponsible(e.target.value)}>
                <option value="">先记录缺项</option>
                {project.deliveryParticipants?.map(member => <option key={member.id} value={member.id}>{member.name}</option>)}
              </select>
            </label>
            {responsible && <label>
              继续条件
              <textarea name="continueWhen" rows={2} maxLength={1000} defaultValue={item.continueWhen || ""} required />
            </label>}
          </>
        )}
        <button disabled={busy || !state}>保存必交项核对</button>
      </form>
    </details>
  );
}

function RequirementEvidence({ evidence, reviewed }: { evidence: NonNullable<CoverageItem["evidence"]>; reviewed: boolean }) {
  if (evidence.gap) return <p className="preserve"><strong>建议补材料</strong> · {evidence.gap}</p>;
  return (
    <details>
      <summary>{reviewed ? "核对引用" : "找到原句，待核对 · 核对引用"}</summary>
      {(evidence.citations ?? []).map((quote, index) => (
        <div key={index}>
          <small>{quote.name} · v{quote.version} · 字符 {quote.start}–{quote.end}</small>
          <blockquote className="preserve">{quote.text}</blockquote>
        </div>
      ))}
    </details>
  );
}

export function CoverageReview({ project, busy, save }: {
  project: Project;
  busy: boolean;
  save: (data: Assessment) => void;
}) {
  const coverage = project.deliveryCoverage;
  if (!coverage || project.stage === "delivered") return null;
  const blocked = new Set(project.artifacts.filter(a => a.draftBlock).map(a => a.id));
  const covered = coverage.items.filter(item => item.state === "covered" && !blocked.has(item.artifact ?? "")).length;
  const missing = coverage.items.filter(item => item.state === "missing");
  return (
    <section className="card" aria-label="必交项核对" id="delivery-coverage" tabIndex={-1}>
      <h2>必交项核对</h2>
      <p role="status">
        已核对通过 {covered}/{coverage.items.length} 项
        {missing.length > 0 && ` · 缺 ${missing.length} 项`}
        {missing.length > 0 && !coverage.allowPartial && " · 等待补齐后交付"}
      </p>
      <ol>
        {coverage.items.map(item => (
          <li key={item.item}>
            <strong>{item.label}</strong>{" · "}
            {blocked.has(item.artifact ?? "") ? "候选待重做" : item.state === "covered" ? "已满足" : item.state === "missing" ? "缺项" : "待核对"}
            {item.version && <small>步骤 {item.step} · 成果 v{item.version}</small>}
            {item.evidence && !blocked.has(item.artifact ?? "") && (
              <RequirementEvidence key={item.artifact + ":" + item.version} evidence={item.evidence} reviewed={item.state !== "unchecked"} />
            )}
            {item.reason && <p className="preserve">{item.reason}</p>}
            {item.state === "missing" && item.responsible && <p>
              {item.responsibleActive ? `接手人 ${item.responsibleName}` : "接手权限已失效"}
              {item.continueWhen && ` · ${item.continueWhen}`}
            </p>}
            {project.stage === "review" && project.canReview && item.run && !blocked.has(item.artifact ?? "") && (
              <RequirementAssessment
                key={[item.run,item.artifact,item.version,item.state,item.reason,item.responsible,item.continueWhen].join(":")}
                item={item} project={project} busy={busy} save={save} />
            )}
          </li>
        ))}
      </ol>
    </section>
  );
}

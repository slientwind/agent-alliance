import { useState } from "react";
import type { CoverageItem, Project, RemoteReviewEvidence } from "./client";
import { FileEvidencePicker } from "./delivery-file-evidence";
import { UpstreamReviewEvidence } from "./upstream-review-evidence";

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
  fileEvidence?: string;
  remoteEvidence?: boolean;
  upstreamEvidence?: number;
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
  const [fileChoice, setFileChoice] = useState<(NonNullable<CoverageItem["fileEvidence"]> & { proof: string }) | null>();
  const [remoteChoice, setRemoteChoice] = useState<boolean>();
  const [upstreamChoice, setUpstreamChoice] = useState<number>();
  const fileEvidence = fileChoice === undefined ? item.fileEvidence : fileChoice;
  return (
    <details>
      <summary>核对此项{item.version ? " · v" + item.version : ""}</summary>
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
          fileEvidence: item.fileEvidenceAvailable ? fileChoice?.proof ?? (fileChoice === null ? "" : undefined) : undefined,
          remoteEvidence: item.remoteEvidence ? remoteChoice : undefined,
          upstreamEvidence: item.upstreamEvidenceReady ? upstreamChoice : undefined,
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
        {item.fileEvidenceAvailable && (
          <>
            <FileEvidencePicker project={project.id} item={item} selected={fileChoice?.proof} choose={setFileChoice} />
            {fileEvidence && (
              <div>
                <p>选定依据 · {fileEvidence.name} · {fileEvidence.location}</p>
                <blockquote className="preserve">{fileEvidence.text}</blockquote>
                <button type="button" onClick={() => setFileChoice(null)}>移除所选依据</button>
              </div>
            )}
          </>
        )}
        {item.remoteEvidence && (
          <label>
            <input type="checkbox" checked={remoteChoice ?? !!item.remoteEvidenceRecorded}
              onChange={event => setRemoteChoice(event.target.checked)} />
            将第三方回执记为本项依据
          </label>
        )}
        {item.artifact && item.version && item.upstreamEvidenceReady !== undefined && (
          <UpstreamReviewEvidence key={String(item.upstreamEvidenceReady)} project={project.id}
            artifact={item.artifact} version={item.version} ready={item.upstreamEvidenceReady}
            selected={upstreamChoice ?? item.upstreamEvidenceStep} choose={setUpstreamChoice} />
        )}
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
        <button disabled={busy || !state}>保存核对结果</button>
      </form>
    </details>
  );
}

const remoteReturnWay = { submit: "提交时返回", query: "查询原任务后返回", supplement: "补件后返回" };

function RemoteEvidenceDetails({ receipt, recorded, version }: { receipt: RemoteReviewEvidence; recorded?: boolean; version?: number }) {
  return (
    <details>
      <summary>{recorded ? "已记录的第三方回执" : "查看第三方回执"} · 第三方任务已完成</summary>
      <p>{receipt.agent} v{receipt.agentRevision} · 成果 v{version} · {remoteReturnWay[receipt.way]}</p>
      <p className="preserve">原任务编号 <bdi>{receipt.task}</bdi></p>
      <small>步骤 {receipt.step} · 第 {receipt.attempt} 次尝试 · 接收于 {new Date(receipt.completedAt).toLocaleString("zh-CN")}</small>
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
  const reviewing = project.stage === "review";
  const delivered = project.stage === "delivered" && coverage?.items.some(item => item.fileEvidence || item.remoteEvidenceRecorded || item.upstreamEvidenceStep);
  if (!coverage || (!reviewing && !delivered)) return null;
  const blocked = new Set(project.artifacts.filter(a => a.draftBlock).map(a => a.id));
  const covered = coverage.items.filter(item => item.state === "covered" && !blocked.has(item.artifact ?? "")).length;
  const missing = coverage.items.filter(item => item.state === "missing");
  return (
    <section className="card" aria-label="交付清单" id="delivery-coverage" tabIndex={-1}>
      <h2>交付清单（已齐 {covered}/{coverage.items.length}）</h2>
      <p role="status">
        已核对 {covered}/{coverage.items.length} 项
        {missing.length > 0 && ` · 缺 ${missing.length} 项`}
        {missing.length > 0 && !coverage.allowPartial && " · 等待补齐后交付"}
      </p>
      <ol>
        {coverage.items.map(item => {
          const editable = project.stage === "review" && project.canReview && item.run && !blocked.has(item.artifact ?? "");
          return (
          <li key={item.item}>
            <strong>{item.label}</strong>{" · "}
            {blocked.has(item.artifact ?? "") ? "成果待更新" : item.state === "covered" ? "已满足" : item.state === "missing" ? "缺项" : "待核对"}
            {item.version && <small>步骤 {item.step} · 成果 v{item.version}</small>}
            {item.evidence && !blocked.has(item.artifact ?? "") && (
              <RequirementEvidence key={item.artifact + ":" + item.version} evidence={item.evidence} reviewed={item.state !== "unchecked"} />
            )}
            {item.reason && <p className="preserve">{item.reason}</p>}
            {item.fileEvidence && (
              <details>
                <summary>已记录的成果依据 · {item.fileEvidence.location}</summary>
                <small>{item.fileEvidence.name} · v{item.version}</small>
                <blockquote className="preserve">{item.fileEvidence.text}</blockquote>
              </details>
            )}
            {item.remoteEvidence && (project.stage !== "delivered" || item.remoteEvidenceRecorded) && (
              <RemoteEvidenceDetails receipt={item.remoteEvidence} recorded={item.remoteEvidenceRecorded} version={item.version} />
            )}
            {!editable && item.artifact && item.version && item.upstreamEvidenceReady !== undefined && (project.stage !== "delivered" || item.upstreamEvidenceStep) && (
              <UpstreamReviewEvidence key={[item.artifact, item.version, item.upstreamEvidenceReady].join(":")}
                project={project.id} artifact={item.artifact} version={item.version}
                ready={item.upstreamEvidenceReady} selected={item.upstreamEvidenceStep} />
            )}
            {item.state === "missing" && item.responsible && <p>
              {item.responsibleActive ? `接手人 ${item.responsibleName}` : "接手权限已失效"}
              {item.continueWhen && ` · ${item.continueWhen}`}
            </p>}
            {editable && (
              <RequirementAssessment
                key={[item.run,item.artifact,item.version,item.reviewedAt,item.state,item.reason,item.responsible,item.continueWhen].join(":")}
                item={item} project={project} busy={busy} save={save} />
            )}
          </li>
          );
        })}
      </ol>
    </section>
  );
}

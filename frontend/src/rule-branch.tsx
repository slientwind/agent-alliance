import { useState } from "react";
import { api, RequestError, type RuleBranchStep } from "./client";

type Preview = {
  digest: string;
  calls: number;
  chargeTeam: string;
  skipUnavailablePreferences?: boolean;
  steps: RuleBranchStep[];
  rules: { step: number; rule: string; previous: number; replacement: number; body: string }[];
};

const labels: Record<RuleBranchStep["mode"], string> = {
  rebuild: "需要重做",
  reuse: "沿用已完成成果",
  continue: "继续未完成的步骤",
};

function BranchSteps({ steps }: { steps: RuleBranchStep[] }) {
  return <ul>{steps.map(step => (
    <li key={step.step}>
      步骤 {step.step} · {step.name} · {labels[step.mode]}
      {step.mode === "reuse" && step.version ? ` v${step.version}` : ""}
    </li>
  ))}</ul>;
}

export function RuleBranchReview({ run, busy, perform }: {
  run: string;
  busy: boolean;
  perform: (action: () => Promise<unknown>) => Promise<void>;
}) {
  const [request, setRequest] = useState<{ preview: Preview; key: string } | null>(null);
  const path = `runs/${run}/rules/rebuild`;

  async function confirm(current: NonNullable<typeof request>) {
    try {
      await api(path, "POST", { digest: current.preview.digest, authorizeCalls: current.preview.calls },
        { "Idempotency-Key": current.key });
    } catch (error) {
      if (error instanceof RequestError && error.status === 409) setRequest(null);
      throw error;
    }
  }

  return <section className="card" aria-label="执行依据失效后的分支恢复">
    <h2>恢复受影响的工作</h2>
    <p>仅更新受影响的部分。请核对要更新的步骤和可沿用的成果。</p>
    {request ? <>
      <BranchSteps steps={request.preview.steps}/>
      {request.preview.skipUnavailablePreferences && <p>重做会略过已失效的个人偏好，继续使用仍有效的原修订。</p>}
      {request.preview.rules.map(rule => (
        <details key={`${rule.step}:${rule.rule}`}>
          <summary>步骤 {rule.step} · {rule.previous === rule.replacement
            ? `使用当前可用规则 v${rule.replacement}`
            : `规则 v${rule.previous} 更新为 v${rule.replacement}`}</summary>
          <p className="preserve">{rule.body}</p>
        </details>
      ))}
      <p>项目费用团队为 {request.preview.chargeTeam}，跨团队费用沿用方案中的授权。</p>
      <button disabled={busy} onClick={() => void perform(() => confirm(request))}>
        授权最多 {request.preview.calls} 次调用并继续
      </button>{" "}
      <button disabled={busy} onClick={() => setRequest(null)}>返回</button>
    </> : <button disabled={busy} onClick={() => void perform(async () => {
      const preview = await api<Preview>(path);
      setRequest({ preview, key: crypto.randomUUID() });
    })}>核对重做范围</button>}
  </section>;
}

export function RuleBranchSummary({ steps }: { steps: RuleBranchStep[] }) {
  return <details className="card" open>
    <summary>本次分支恢复范围</summary>
    <BranchSteps steps={steps}/>
  </details>;
}

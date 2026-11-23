import { useState, type FormEvent } from "react";
import {
  api,
  type Project,
  type DistributionCase,
  type DistributionState,
} from "./client";

export const distributionLabel: Record<DistributionState, string> = {
  clear: "可分发",
  pending: "等待核对分发范围",
  reference: "仅原资料无法访问，成果可分发",
  invalid: "结论需修订，已暂停分发",
  restricted: "包含受限内容，已暂停分发",
};
export function held(state: DistributionState) {
  return state !== "clear" && state !== "reference";
}

function Decision({
  item,
  project,
  refresh,
}: {
  item: DistributionCase;
  project: string;
  refresh: () => Promise<void>;
}) {
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    setBusy(true);
    setError("");
    try {
      await api("projects/" + project + "/distribution-reviews", "POST", {
        source: item.source,
        artifact: item.artifact,
        version: item.version,
        expected: item.revision,
        decision: form.get("decision"),
        reason: form.get("reason"),
      });
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="card">
      <strong>
        步骤 {item.step} · {item.name} v{item.version}
      </strong>
      <p>
        《{item.sourceName}》 · {distributionLabel[item.state]}
      </p>
      <small>
        {item.editions.length > 0 && "涉及交付批次 " + item.editions.join("、")}
        {item.current && " · 当前候选版本"}
      </small>
      <small>
        可处理 · {Array.from(new Set([item.maintainer, item.owner])).join("、")}
      </small>
      {item.canManage && (
        <details>
          <summary>核对分发范围</summary>
          <form onSubmit={save}>
            {item.history?.map((h) => (
              <p key={h.revision}>
                <strong>
                  {h.actor} · {distributionLabel[h.state]}
                </strong>
                <br />
                {new Date(h.time).toLocaleString()} · {h.reason}
              </p>
            ))}
            <label>
              分发决定
              <select name="decision" required defaultValue="">
                <option value="" disabled>
                  选择处理方式
                </option>
                {(["reference", "invalid", "restricted"] as const).map((s) => (
                  <option key={s} value={s}>
                    {distributionLabel[s]}
                  </option>
                ))}
              </select>
            </label>
            <label>
              处理依据
              <textarea name="reason" required maxLength={2000} />
            </label>
            <button disabled={busy}>保存分发决定</button>
          </form>
        </details>
      )}
      {error && <p role="alert">{error}</p>}
    </section>
  );
}
export function DistributionReview({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  return (
    <details className="card" id="delivery-distribution" tabIndex={-1}>
      <summary>
        资料撤权后的交付分发 ·{" "}
        {project.distributionCases.filter((c) => held(c.state)).length} 项受限
      </summary>
      <p>
        资料负责人或项目发起人按成果版本核对。只要包内仍有受限项，整批暂停下载。已经下载的副本需由接收人处理。
      </p>
      {project.distributionCases.map((c) => (
        <Decision
          key={[c.source, c.artifact, c.version, c.revision].join("-")}
          item={c}
          project={project.id}
          refresh={refresh}
        />
      ))}
    </details>
  );
}

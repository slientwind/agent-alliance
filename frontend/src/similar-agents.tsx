import { RequestImprovement } from "./improvements";
import type { Agent } from "./client";
import { capabilityMatcher, normalizeCapabilityText, substantiveTerms } from "./capability-match";
export type CapabilityDescription = {
  name?: string;
  description?: string;
  operation?: string;
  endpoint?: string;
};
function clean(value?: string) {
  return normalizeCapabilityText(value || "");
}
export function SimilarAgents({
  agents,
  source,
  exclude,
  choose,
  template,
  publishedOutput,
  choiceLabel = "使用此 Agent",
}: {
  agents: Agent[];
  source: CapabilityDescription;
  exclude?: string;
  publishedOutput?: string;
  choiceLabel?: string;
  choose?: (agent: Agent) => void;
  template?: (description: CapabilityDescription) => void;
}) {
  const title = clean(source.name),
    description = clean(source.description);
  const match = publishedOutput
    ? capabilityMatcher([title, description].join(" "))
    : null;
  const matches = agents
    .flatMap((a) => {
      if (a.id === exclude) return [];
      if (
        publishedOutput &&
        (!a.publishedReady ||
          !a.published ||
          a.publishedOperation !== publishedOutput)
      )
        return [];
      const names = publishedOutput
        ? [a.publishedName]
        : [a.name, a.publishedName];
      const instructions = publishedOutput
        ? [a.publishedInstructions]
        : [a.instructions, a.publishedInstructions];
      const reasons = [];
      if (title && names.some((name) => title === clean(name)))
        reasons.push("名称相同");
      else if (title) {
        const subject = (label: string) =>
          label.replace(/(?:助手|智能体|agent)$/u, "").trim();
        const target = subject(title);
        if (
          target.length >= 3 &&
          names.some((label) => {
            const other = subject(clean(label));
            return (
              other.length >= 3 &&
              (other.includes(target) || target.includes(other))
            );
          })
        )
          reasons.push("名称含相同任务词");
      }
      if (
        description.length >= 4 &&
        instructions.some((text) => description === clean(text))
      )
        reasons.push("任务说明相同");
      if (!publishedOutput && source.endpoint && a.endpoint === source.endpoint)
        reasons.push("调用地址相同");
      if (
        !publishedOutput &&
        source.operation &&
        source.operation === a.tool?.operation
      )
        reasons.push("工具操作相同");
      const { score, words } = match?.(
        a.publishedName || "",
        a.publishedInstructions || "",
      ) || { score: 0, words: [] };
      const matched = substantiveTerms(words);
      const explicit = reasons.length > 0;
      if (!explicit && publishedOutput && matched.length >= 2)
        reasons.push("任务词重合：" + matched.slice(0, 3).join("、"));
      return reasons.length ? [{ a, reasons, explicit, score }] : [];
    })
    .sort(
      (left, right) =>
        Number(right.explicit) - Number(left.explicit) ||
        right.score - left.score,
    );
  if (!matches.length) return null;
  if (publishedOutput) {
    return (
      <details className="similar-agents">
        <summary>可直接使用的相似 Agent（{matches.length}）</summary>
        {matches.map(({ a, reasons }) => (
          <div key={a.id} role="group" aria-label={`${a.publishedName} v${a.published} · ${a.teamName}`}>
            <strong>
              {a.publishedName} · v{a.published}
            </strong>
            <small>
              {reasons.join("、")} · {a.teamName}承担费用
            </small>
            {a.publishedInstructions &&
              clean(a.publishedInstructions) !== clean(a.publishedName) && (
                <p>{a.publishedInstructions}</p>
              )}
            <button
              type="button"
              disabled={!choose}
              onClick={() => choose?.(a)}
            >
              {choiceLabel}
            </button>
            {template && a.publishedInstructions && (
              <>
                <button
                  type="button"
                  onClick={() =>
                    template({
                      name: (a.publishedName || a.name) + " 副本",
                      description: a.publishedInstructions,
                    })
                  }
                >
                  套用发布版任务说明
                </button>
                <RequestImprovement
                  key={[a.id, a.revision, a.published].join("/")}
                  agent={a}
                />
              </>
            )}
          </div>
        ))}
      </details>
    );
  }
  return (
    <details className="similar-agents">
      <summary>发现相似能力 · {matches.length}</summary>
      <p>可核对已有能力，也可以保留独立草稿。</p>
      {matches.map(({ a, reasons }) => (
        <div key={a.id}>
          <strong>
            {a.name} · 草稿 v{a.revision}
          </strong>
          <small>
            {reasons.join("、")} ·{" "}
            {a.published
              ? "已发布 " + (a.publishedName || a.name) + " v" + a.published
              : "尚未发布"}
          </small>
          <p>草稿说明 · {a.instructions}</p>
          {a.publishedName && a.published !== a.revision && (
            <p>发布版说明 · {a.publishedInstructions}</p>
          )}
          <small>所属团队 · {a.teamName}</small>
          <RequestImprovement
            key={[a.id, a.revision, a.published].join("/")}
            agent={a}
          />
          {template && a.publishedReady && a.publishedInstructions && (
            <button
              type="button"
              onClick={() =>
                template({
                  name: (a.publishedName || a.name) + " 副本",
                  description: a.publishedInstructions,
                })
              }
            >
              套用发布版任务说明
            </button>
          )}
          {choose && a.publishedReady ? (
            <button type="button" onClick={() => choose(a)}>
              选择已有发布版本
            </button>
          ) : (
            !choose && (
              <button
                type="button"
                onClick={() =>
                  document.getElementById("library-agent-" + a.id)?.focus()
                }
              >
                定位到库中条目
              </button>
            )
          )}
        </div>
      ))}
    </details>
  );
}

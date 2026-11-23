import { useState } from "react";
import { api, type Artifact, type Project } from "./client";

type Target = { artifact: string; version: number };
type Assignment = {
  body: string;
  kind: string;
  targets: Target[];
  deferred: boolean;
};
export type ProjectFeedbackDraft = {
  original: string;
  items: Assignment[];
  key: string;
};

// Only user-authored separators outside quotations start a new instruction.
function splitFeedback(text: string): string[] {
  const closing: Record<string, string> = { "“": "”", "「": "」", '"': '"' };
  let end = "",
    part = "";
  const parts: string[] = [];
  for (const char of text) {
    if (!end && (char === "；" || char === ";" || char === "\n")) {
      if (part.trim()) parts.push(part.trim());
      part = "";
    } else {
      part += char;
      if (end && char === end) end = "";
      else if (!end) end = closing[char] || "";
    }
  }
  if (part.trim()) parts.push(part.trim());
  return parts;
}

function recommend(body: string, artifacts: Artifact[]): Target[] {
  const steps = new Set(
    Array.from(body.matchAll(/步骤\s*(\d+(?:\s*[、和与]\s*\d+)*)/g), (m) =>
      m[1].split(/[、和与]/).map(Number),
    ).flat(),
  );
  if (steps.size > 1) return [];
  if (steps.size === 1)
    return artifacts.filter((a) => steps.has(a.ordinal)).map(pin);
  const quotes = Array.from(
    body.matchAll(/[“「"]([^”」"\n]+)[”」"]/g),
    (m) => m[1],
  );
  const matches = artifacts.filter(
    (a) =>
      body.toLocaleLowerCase().includes(a.name.toLocaleLowerCase()) ||
      quotes.some((quote) => a.body.includes(quote)),
  );
  return matches.length === 1 ? matches.map(pin) : [];
}
function pin(a: Artifact): Target {
  return { artifact: a.id, version: a.version };
}
function downstream(p: Project, targets: Target[]): string[] {
  const direct = new Set(
    targets.map((t) => p.artifacts.find((a) => a.id === t.artifact)?.ordinal),
  );
  const affected = new Set(direct);
  // Plan dependencies always point to earlier steps.
  for (const [index, node] of (p.plan?.nodes || []).entries()) {
    if (node.dependsOn.some((n) => affected.has(n))) affected.add(index + 1);
  }
  return p.artifacts
    .filter((a) => affected.has(a.ordinal) && !direct.has(a.ordinal))
    .map((a) => `步骤 ${a.ordinal} · ${a.name}`);
}

export function ProjectFeedback({
  p,
  drafts,
  busy,
  perform,
}: {
  p: Project;
  drafts: Map<string, ProjectFeedbackDraft>;
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
}) {
  const [draft, setDraft] = useState<ProjectFeedbackDraft>(
    () =>
      drafts.get(p.id) || { original: "", items: [], key: crypto.randomUUID() },
  );
  const [receipt, setReceipt] = useState("");
  function save(original: string, items: Assignment[]) {
    const next = { original, items, key: crypto.randomUUID() };
    if (original) drafts.set(p.id, next);
    else drafts.delete(p.id);
    setDraft(next);
    setReceipt("");
  }
  function update(index: number, changes: Partial<Assignment>) {
    save(
      draft.original,
      draft.items.map((item, i) =>
        i === index ? { ...item, ...changes } : item,
      ),
    );
  }
  const selected = draft.items.filter((item) => !item.deferred);
  const outdated = (target: Target) =>
    p.artifacts.find((a) => a.id === target.artifact)?.version !==
    target.version;
  const canSubmit =
    selected.length > 0 &&
    selected.length <= 32 &&
    selected.every(
      (item) =>
        item.body.trim() &&
        item.targets.length &&
        !item.targets.some(outdated) &&
        (item.kind !== "change" || p.canReview),
    ) &&
    selected.reduce((n, item) => n + item.targets.length, 0) <= 64;

  async function submit() {
    const result = await api<{ assignments: number }>(
      "projects/" + p.id + "/feedback-assignments",
      "POST",
      {
        original: draft.original,
        items: selected.map(({ body, kind, targets }) => ({
          body,
          kind,
          targets,
        })),
      },
      { "Idempotency-Key": draft.key },
    );
    if (drafts.get(p.id)?.key !== draft.key) return;
    const remaining = draft.items
      .filter((item) => item.deferred)
      .map((item) => ({ ...item, deferred: false }));
    save(remaining.map((item) => item.body).join("\n"), remaining);
    setReceipt(
      `已提交 ${result.assignments} 个成果反馈项。${remaining.length ? "暂缓项已留在草稿中。" : ""}`,
    );
  }
  return (
    <details className="card project-feedback">
      <summary>项目反馈</summary>
      {receipt && <p role="status">{receipt}</p>}
      <fieldset className="plan-fields" disabled={busy}>
        {!draft.original && p.messages.some((m) => m.kind === "user") && (
          <label>
            引用对话里的意见
            <select
              value=""
              onChange={(e) => {
                const message = p.messages.find(
                  (m) => String(m.seq) === e.target.value,
                );
                if (message) save(message.body, []);
              }}
            >
              <option value="">选择一条消息</option>
              {p.messages
                .filter((m) => m.kind === "user")
                .slice()
                .reverse()
                .map((m) => (
                  <option value={m.seq} key={m.seq}>
                    {m.body.slice(0, 100)}
                  </option>
                ))}
            </select>
          </label>
        )}
        <label>
          整组意见
          <textarea
            value={draft.original}
            maxLength={16000}
            readOnly={!!draft.items.length}
            placeholder="写下需要调整和保留的内容。多项意见可换行，或用分号分开。"
            onChange={(e) => save(e.target.value, [])}
          />
        </label>
        {!draft.items.length && (
          <button
            disabled={!splitFeedback(draft.original).length}
            onClick={() =>
              save(
                draft.original,
                splitFeedback(draft.original).map((body) => ({
                  body,
                  kind: "suggestion",
                  targets: recommend(body, p.artifacts),
                  deferred: false,
                })),
              )
            }
          >
            查看分配建议
          </button>
        )}
        {draft.items.map((item, i) => {
          const related = downstream(p, item.targets);
          return (
            <fieldset key={i}>
              <legend>意见 {i + 1}</legend>
              <label>
                修改或保留要求
                <textarea
                  maxLength={4000}
                  value={item.body}
                  onChange={(e) => update(i, { body: e.target.value })}
                />
              </label>
              <label>
                处理方式
                <select
                  value={item.kind}
                  onChange={(e) => update(i, { kind: e.target.value })}
                >
                  <option value="suggestion">建议，交由审核人判断</option>
                  <option value="issue">验收问题，待核实</option>
                  {p.canReview && <option value="change">要求修改</option>}
                </select>
              </label>
              {!item.targets.length && !item.deferred && (
                <p className="action-needed">这条意见针对哪些成果？</p>
              )}
              {p.artifacts.map((a) => {
                const target = item.targets.find((t) => t.artifact === a.id);
                return (
                  <div key={a.id}>
                    <label>
                      <input
                        type="checkbox"
                        checked={!!target}
                        onChange={(e) =>
                          update(i, {
                            targets: e.target.checked
                              ? [...item.targets, pin(a)]
                              : item.targets.filter((t) => t.artifact !== a.id),
                          })
                        }
                      />
                      步骤 {a.ordinal} · {a.name} · v
                      {target?.version ?? a.version}
                    </label>
                    {target && (
                      <small>
                        负责 Agent ·{" "}
                        {p.plan?.nodes[a.ordinal - 1]?.name || a.name} ·{" "}
                        {a.editMode === "feedback"
                          ? "可发起单步改稿"
                          : "可由人员修订"}
                      </small>
                    )}
                    {target && outdated(target) && (
                      <details>
                        <summary>目标已更新 · 查看 v{a.version}</summary>
                        <p className="preserve">{a.body}</p>
                        <button
                          onClick={() =>
                            update(i, {
                              targets: item.targets.map((t) =>
                                t.artifact === a.id ? pin(a) : t,
                              ),
                            })
                          }
                        >
                          改为针对 v{a.version}
                        </button>
                      </details>
                    )}
                  </div>
                );
              })}
              {!!related.length && (
                <small>修改后需关联复核 · {related.join("、")}</small>
              )}
              <label>
                <input
                  type="checkbox"
                  checked={item.deferred}
                  onChange={(e) => update(i, { deferred: e.target.checked })}
                />
                暂缓此项
              </label>
            </fieldset>
          );
        })}
        {!!draft.items.length && (
          <>
            <small>
              提交后可在各成果下处理反馈。调用 Agent
              改稿需单独授权。一次最多提交 32 项意见、64 个目标。
            </small>
            <div className="actions">
              <button
                className="primary"
                disabled={!canSubmit}
                onClick={() => void perform(submit)}
              >
                确认分配
              </button>
              <button onClick={() => save(draft.original, [])}>
                返回原文重新拆分
              </button>
            </div>
          </>
        )}
      </fieldset>
    </details>
  );
}

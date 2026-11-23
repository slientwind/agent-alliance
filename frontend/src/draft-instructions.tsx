import { useState } from "react";
import { api, RequestError, type Agent } from "./client";

export function DraftInstructions({
  agent,
  revision,
  busy,
  act,
  refresh,
  saved,
}: {
  agent: Agent;
  revision: number;
  busy: boolean;
  act: (action: () => Promise<void>) => Promise<void>;
  refresh: () => Promise<void>;
  saved: (revision: number) => void;
}) {
  const [base, setBase] = useState(revision);
  const [binding] = useState(() =>
    agent.revision === revision
      ? { resource: agent.resource, revision: agent.resourceRevision }
      : null,
  );
  const [text, setText] = useState(agent.instructions);
  const stale = agent.revision !== base;
  const sameModel =
    binding &&
    agent.resource === binding.resource &&
    agent.resourceRevision === binding.revision;
  return (
    <details>
      <summary>调整 Agent 职责</summary>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void act(async () => {
            try {
              const next = await api<{ revision: number }>(
                "agents/" + agent.id + "/instructions",
                "POST",
                {
                  expected: base,
                  instructions: text.trim(),
                },
              );
              await refresh();
              saved(next.revision);
            } catch (err) {
              if (err instanceof RequestError && err.status === 409)
                await refresh();
              throw err;
            }
          });
        }}
      >
        <label>
          调整后的职责
          <textarea
            required
            value={text}
            disabled={busy}
            onChange={(e) => setText(e.target.value)}
          />
        </label>
        {stale ? (
          <>
            <strong>v{agent.revision} 已保存的职责</strong>
            <p className="preserve">{agent.instructions}</p>
            {sameModel ? (
              <button
                type="button"
                disabled={busy}
                onClick={() => setBase(agent.revision)}
              >
                以 v{agent.revision} 为基础继续编辑
              </button>
            ) : (
              <small>
                模型绑定已变化。请保留所填文字，返回能力库核对新配置。
              </small>
            )}
          </>
        ) : (
          <small>
            沿用 v{base}{" "}
            的模型、凭据和费用承担方。保存后可为新版本单独授权试运行。
          </small>
        )}
        <button
          type="submit"
          disabled={
            busy ||
            stale ||
            !text.trim() ||
            text.trim() === agent.instructions.trim()
          }
        >
          保存职责为新草稿
        </button>
      </form>
    </details>
  );
}

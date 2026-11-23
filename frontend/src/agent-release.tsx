import { useState } from "react";
import { api, type Agent } from "./client";
import { TrialEvidence } from "./trial-evidence";

type Props = { agent: Agent; refresh: () => Promise<void> };

export function RequestRelease({ agent, refresh }: Props) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = agent.release;
  const pending = request?.state === "pending";
  async function update(submit: boolean) {
    setBusy(true);
    setError("");
    try {
      if (submit) {
        await api("agents/" + agent.id + "/release-requests", "POST", {
          revision: agent.revision,
          previous: request?.id || "",
        });
      }
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      {pending && (
        <small>
          已申请发布 v{agent.revision} ·{" "}
          {request.hasPublisher
            ? "等待管理员处理"
            : "当前团队缺少可处理的管理员，请联系企业管理员安排。"}
        </small>
      )}
      {request?.state === "returned" && <p>退回原因 · {request.reason}</p>}
      {!pending && agent.validated && (
        <button type="button" disabled={busy} onClick={() => void update(true)}>
          {request?.state === "returned" ? "重新申请发布" : "申请发布此版本"}
        </button>
      )}
      <button type="button" disabled={busy} onClick={() => void update(false)}>
        查看发布状态
      </button>
      {error && <p role="alert">{error}</p>}
    </div>
  );
}

export function ReleaseQueue({
  agents,
  refresh,
}: {
  agents: Agent[];
  refresh: Props["refresh"];
}) {
  const pending = agents.filter(
    (a) => a.release?.state === "pending" && a.revision !== a.published,
  );
  if (!pending.length) return null;
  return (
    <section aria-label="待处理的 Agent 发布申请">
      <h3>发布申请 · {pending.length}</h3>
      {pending.map((agent) => (
        <ReleaseDecision key={agent.release!.id} agent={agent} refresh={refresh} />
      ))}
    </section>
  );
}

function ReleaseDecision({ agent, refresh }: Props) {
  const request = agent.release!;
  const [busy, setBusy] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  async function decide(action: "publish" | "return") {
    setBusy(true);
    setError("");
    try {
      await api(
        "agents/" + agent.id + "/release-requests/" + request.id,
        "POST",
        { revision: agent.revision, action, reason: reason.trim() },
      );
      await refresh();
    } catch (e) {
      setError((e as Error).message);
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" aria-label={agent.name + " v" + agent.revision + " 发布申请"}>
      <strong>{agent.name} · v{agent.revision}</strong>
      <small>{request.requester} 提交 · {agent.teamName}</small>
      <p className="preserve">{agent.instructions}</p>
      <TrialEvidence agent={agent.id} revision={agent.revision} refresh={refresh} version={0} />
      <button
        type="button"
        onClick={() => document.getElementById("library-agent-" + agent.id)?.focus()}
      >
        查看此版验收记录
      </button>
      <button
        type="button"
        disabled={busy || !agent.validated}
        onClick={() => void decide("publish")}
      >
        确认发布 v{agent.revision}
      </button>
      {!agent.validated && <small>必要验证已失效，需重新验证。</small>}
      <details>
        <summary>退回修改</summary>
        <label>
          需要修改的地方
          <textarea value={reason} onChange={(e) => setReason(e.target.value)} />
        </label>
        <button type="button" disabled={busy || !reason.trim()} onClick={() => void decide("return")}>
          确认退回
        </button>
      </details>
      {error && <p role="alert">{error}</p>}
    </section>
  );
}

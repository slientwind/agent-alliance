import { useState } from "react";
import { BuildReview, buildSelection } from "./agent-build";
import { api, type Agent } from "./client";
import { TrialEvidence } from "./trial-evidence";

type Props = { agent: Agent; refresh: () => Promise<void> };

export function RequestRelease({ agent, refresh }: Props) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = agent.release;
  const pending = request?.state === "pending";
  async function update(selection: ReturnType<typeof buildSelection>, reload: () => void) {
    setBusy(true);
    setError("");
    try {
      await api("agents/" + agent.id + "/release-requests", "POST", {
        revision: agent.revision,
        previous: request?.id || "",
        ...selection,
      });
      await refresh();
    } catch (e) {
      setError((e as Error).message);
      reload();
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
      <BuildReview agent={agent}>
        {(snapshot, reload) => (!pending || snapshot.review?.stale) && snapshot.evidence.passed && (
          <button type="button" disabled={busy} onClick={() => void update(buildSelection(snapshot), reload)}>
            {pending ? "更新发布申请" : request?.state === "returned" ? "重新申请发布" : "申请发布此版本"}
          </button>
        )}
      </BuildReview>
      {error && <p role="alert">{error}</p>}
    </div>
  );
}

export function PublishDraft({ agent, refresh }: Props) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function publish(selection: ReturnType<typeof buildSelection>, reload: () => void) {
    setBusy(true);
    setError("");
    try {
      await api("agents/" + agent.id + "/publish", "POST", { revision: agent.revision, ...selection });
      await refresh();
    } catch (e) {
      setError((e as Error).message);
      reload();
    } finally {
      setBusy(false);
    }
  }
  return (
    <BuildReview agent={agent}>
      {(snapshot, reload) => <>
        <button type="button" disabled={busy || !snapshot.evidence.passed || !!snapshot.review}
          onClick={() => void publish(buildSelection(snapshot), reload)}>
          发布此版本
        </button>
        {error && <p role="alert">{error}</p>}
      </>}
    </BuildReview>
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
  async function decide(action: "publish" | "return", selection?: ReturnType<typeof buildSelection>) {
    setBusy(true);
    setError("");
    try {
      await api(
        "agents/" + agent.id + "/release-requests/" + request.id,
        "POST",
        { revision: agent.revision, action, reason: reason.trim(), ...selection },
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
      <BuildReview agent={agent} openInitially>
        {(snapshot) => (
          <button type="button"
            disabled={busy || !snapshot.evidence.passed || snapshot.review?.id !== request.id || snapshot.review.stale}
            onClick={() => void decide("publish", buildSelection(snapshot))}>
            确认发布 v{agent.revision}
          </button>
        )}
      </BuildReview>
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

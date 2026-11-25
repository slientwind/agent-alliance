import { useState } from "react";
import { AlarmClock, Bot, Copy, RotateCcw, ThumbsDown, ThumbsUp } from "lucide-react";
import { api, type Artifact, type Project } from "./client";

export function ReplyActions({ artifact, project, owner, busy, refresh, openStudio, schedule }: {
  artifact: Artifact;
  project: Project;
  owner: boolean;
  busy: boolean;
  refresh: () => Promise<void>;
  openStudio: (id: string) => void;
  schedule: () => void;
}) {
  const [pending, setPending] = useState("");
  const [status, setStatus] = useState("");
  const [error, setError] = useState("");
  const [feedback, setFeedback] = useState<"feedback_thumbs_up" | "feedback_thumbs_down">();
  const run = artifact.replyRun;
  if (!run || artifact.draftBlock) return null;
  const current = project.run?.id === run && project.run.status === "completed";
  const limited = busy || !!pending;

  async function perform(action: string, work: () => Promise<void>) {
    setPending(action);
    setStatus("");
    setError("");
    try {
      await work();
    } catch (error) {
      setError((error as Error).message || "操作失败，请稍后重试");
    } finally {
      setPending("");
    }
  }

  async function signal(kind: string) {
    await api("telemetry/signals", "POST", { runId: run, kind });
  }

  function copy() {
    return perform("copy", async () => {
      await navigator.clipboard.writeText(artifact.body);
      setStatus("已复制答复");
      await signal("reply_copied");
    });
  }

  function vote(kind: NonNullable<typeof feedback>) {
    return perform(kind, async () => {
      await signal(kind);
      setFeedback(kind);
      setStatus("已记录反馈");
    });
  }

  function retry() {
    return perform("retry", async () => {
      const path = `projects/${project.id}/general-runs/${run}/retry`;
      const headers = { "Idempotency-Key": crypto.randomUUID() };
      const start = () => api(path, "POST", { expected: project.revision }, headers);
      try {
        await start();
      } catch (error) {
        if (!(error instanceof TypeError)) throw error;
        await start();
      }
      await refresh();
      setStatus("已重新开始");
    });
  }

  function save() {
    return perform("studio", async () => {
      const draft = await api<{ id: string }>("studio/from-run", "POST", { run });
      openStudio(draft.id);
    });
  }

  return (
    <div className="reply-actions" role="group" aria-label="答复操作">
      <div className="row">
        <button type="button" disabled={limited} onClick={() => void copy()} aria-label="复制答复" title="复制答复"><Copy size={17} /></button>
        {owner && current && project.stage !== "delivered" && <button type="button" disabled={limited} onClick={() => void retry()} aria-label="重新生成答复" title="重新生成答复">
          <RotateCcw size={17} />
        </button>}
        <button type="button" disabled={limited} aria-label="答复有帮助" title="答复有帮助" aria-pressed={feedback === "feedback_thumbs_up"}
          onClick={() => void vote("feedback_thumbs_up")}><ThumbsUp size={17} /></button>
        <button type="button" disabled={limited} aria-label="答复没帮助" title="答复没帮助" aria-pressed={feedback === "feedback_thumbs_down"}
          onClick={() => void vote("feedback_thumbs_down")}><ThumbsDown size={17} /></button>
        {owner && <>
          <button type="button" disabled={limited} onClick={() => void save()}><Bot size={17} aria-hidden="true" /> 存为助手</button>
          {current && <button type="button" disabled={limited} onClick={schedule}><AlarmClock size={17} aria-hidden="true" /> 设为定时</button>}
        </>}
      </div>
      {status && <p role="status">{status}</p>}
      {error && <p role="alert">{error}</p>}
    </div>
  );
}

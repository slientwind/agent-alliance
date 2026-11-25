import { useEffect, useState } from "react";
import { api, type Project } from "./client";

type ToolApproval = {
  id: string;
  level: "L1" | "L2";
  revision: number;
  tool: string;
  recipient: string;
  planVersion: number;
  external?: boolean;
  arguments?: Record<string, unknown>;
};

export function ToolApprovals({
  project,
  refresh,
}: {
  project: Project;
  refresh: () => Promise<void>;
}) {
  const path = `projects/${project.id}/tool-approvals`;
  const [approvals, setApprovals] = useState<ToolApproval[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    let current = true;
    api<ToolApproval[]>(path).then(
      (value) => { if (current) setApprovals(value); },
      () => { if (current) setApprovals([]); },
    );
    return () => { current = false; };
  }, [path, project.revision]);

  async function decide(approval: ToolApproval, accept: boolean) {
    setBusy(approval.id);
    setError("");
    try {
      await api(`${path}/${approval.id}`, "POST", { expected: approval.revision, accept });
      setApprovals((prior) => prior.filter((item) => item.id !== approval.id));
      await refresh();
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : "操作失败，请稍后重试");
    } finally {
      setBusy("");
    }
  }

  return (
    <>
      {approvals.map((approval) => (
        <section key={approval.id} className="card" aria-labelledby={`approval-${approval.id}`}>
          <h2 id={`approval-${approval.id}`}>
            {approval.external ? "确认外部委派" : approval.level === "L1" ? "确认付费查询" : "审核写入请求"}
          </h2>
          <p>助手准备向 <strong>{approval.recipient}</strong> 发送本次请求，使用 {approval.tool}。</p>
          <p>{approval.external || approval.level === "L1"
            ? "该服务可能产生费用，金额由服务方确定。"
            : "允许后，服务可能修改业务记录。"}</p>
          {approval.level === "L2" && <p>任务方案第 {approval.planVersion} 版</p>}
          {approval.arguments && (
            <details>
              <summary>查看发送内容</summary>
              <pre className="tool-approval-arguments">{JSON.stringify(approval.arguments, null, 2)}</pre>
            </details>
          )}
          <div className="row">
            <button type="button" disabled={!!busy} onClick={() => void decide(approval, true)}>
              {approval.external ? "确认发送" : approval.level === "L1" ? "确认并继续" : "允许写入"}
            </button>
            <button type="button" disabled={!!busy} onClick={() => void decide(approval, false)}>
              跳过这一步
            </button>
          </div>
        </section>
      ))}
      {error && <p role="alert">{error}</p>}
    </>
  );
}

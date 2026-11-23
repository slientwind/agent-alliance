import { api } from "./client";
export type Remote = {
  step: number;
  state: string;
  phase: string;
  detail: string;
  deadline: string;
  checks: number;
  cancelRequested: boolean;
};
const states: Record<string, string> = {
  submitted: "已受理",
  working: "执行中",
  "input-required": "待补充输入",
  "auth-required": "待授权",
  completed: "已完成",
  failed: "执行失败",
  rejected: "已拒绝",
  canceled: "已取消",
  unknown: "待核对",
};
const reasons: Record<string, string> = {
  remote_no_text: "回执未包含本操作声明的文本成果",
  remote_deadline: "已到核对期限",
  remote_unknown: "结果待核对",
  remote_query_failed: "连续查询失败",
  remote_denied: "访问授权已失效",
  remote_query_retry: "正在重试查询",
  remote_legacy: "等待首次核对",
};
export function RemoteReceipt({
  remote,
  run,
  revision,
  canCheck,
  perform,
}: {
  remote: Remote;
  run: string;
  revision: number;
  canCheck: boolean;
  perform: (action: () => Promise<unknown>) => Promise<void>;
}) {
  return (
    <section>
      <strong>
        步骤 {remote.step} · 远端{states[remote.state] || "状态未知"}
      </strong>
      <small>
        {remote.cancelRequested && "远端取消请求已登记 · "}
        {reasons[remote.detail] ||
          (remote.phase === "checking"
            ? "正在核对原任务"
            : "已记录远端回执")}{" "}
        · 已核对 {remote.checks} 次
      </small>
      {remote.phase === "checking" && (
        <small>
          本轮核对截至 {new Date(remote.deadline).toLocaleString("zh-CN")}
        </small>
      )}
      {remote.phase === "manual" && canCheck && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const reason = new FormData(e.currentTarget).get("reason");
            void perform(() =>
              api("runs/" + run + "/remote/" + remote.step + "/check", "POST", {
                reason,
                expected: revision,
              }),
            );
          }}
        >
          <label>
            继续核对原因
            <input required name="reason" />
          </label>
          <button>再次核对原任务</button>
        </form>
      )}
    </section>
  );
}

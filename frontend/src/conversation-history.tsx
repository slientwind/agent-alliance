import { useEffect, useState } from "react";
import { ReviewReminders } from "./review-reminders";
import { ProjectAgentGrants } from "./project-agent-grants";
import { BudgetInbox } from "./budget-inbox";
import { api, statusLabel, type Summary } from "./client";

export type HistoryView = "recent" | "review" | "budget" | "grant";
export type ReviewTask = {
  project: string;
  title: string;
  kind:
    | "review"
    | "deliver"
    | "coverage"
    | "distribution"
    | "tool"
    | "mention"
    | "suggested"
    | "required"
    | "investigate"
    | "verification";
  name: string;
  subject: string;
  version: number;
  revision: number;
  step: number;
  position?: string;
  artifact?: string;
};
const actionLabels: Record<ReviewTask["kind"], string> = {
  review: "审核成果",
  deliver: "最终确认交付",
  coverage: "核对交付清单",
  distribution: "查看资料撤权影响",
  tool: "处理助手请求",
  mention: "同事在评论中提到了你",
  suggested: "处理建议",
  required: "跟进修改",
  investigate: "核实验收问题",
  verification: "复核修改",
};
export function taskAnchor(t: ReviewTask) {
  if (t.kind === "distribution") return "delivery-distribution";
  if (t.kind === "tool") return "approval-" + t.subject;
  if (t.kind === "mention") return "mention-" + t.subject;
  if (t.kind === "review") return "review-" + t.subject + "-" + t.version;
  if (t.kind === "deliver") return "deliver-" + t.revision;
  if (t.kind === "coverage") return "delivery-coverage";
  return "feedback-" + t.subject + "-" + t.version + "-" + t.revision;
}
export function ConversationHistory({
  current,
  recent,
  query,
  setQuery,
  inbox,
  setInbox,
  open,
}: {
  current: string;
  recent: string[];
  query: string;
  setQuery: (q: string) => void;
  inbox: HistoryView;
  setInbox: (v: HistoryView) => void;
  open: (id: string, task?: ReviewTask) => void;
}) {
  const [rows, setRows] = useState<Summary[]>([]);
  const [tasks, setTasks] = useState<ReviewTask[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (inbox === "budget" || inbox === "grant") return;
    let request = 0;
    setLoading(true);
    const load = async () => {
      if (document.hidden) return;
      const n = ++request;
      setError("");
      try {
        if (inbox === "review") {
          const data = await api<ReviewTask[]>("projects/review-inbox");
          if (n === request) setTasks(data);
        } else {
          const data = await api<Summary[]>("projects");
          if (n === request) setRows(data);
        }
      } catch (e) {
        if (n === request) setError((e as Error).message);
      } finally {
        if (n === request) setLoading(false);
      }
    };
    void load();
    const timer =
      inbox === "review" ? window.setInterval(load, 30000) : undefined;
    window.addEventListener("focus", load);
    document.addEventListener("visibilitychange", load);
    return () => {
      ++request;
      window.clearInterval(timer);
      window.removeEventListener("focus", load);
      document.removeEventListener("visibilitychange", load);
    };
  }, [inbox, attempt]);
  const needle = query.trim().toLocaleLowerCase();
  const matches = (...parts: string[]) =>
    parts.join(" ").toLocaleLowerCase().includes(needle);
  const rank = (id: string) =>
    recent.includes(id) ? recent.indexOf(id) : recent.length;
  const stage = (x: Summary) => x.isPartial ? "已部分交付" : statusLabel[x.stage] || x.stage;
  const conversations = rows
    .filter((x) => matches(x.title, stage(x)))
    .sort((a, b) => rank(a.id) - rank(b.id));
  const pending = tasks.filter((x) =>
    matches(
      x.title,
      x.name,
      actionLabels[x.kind],
      x.step ? "步骤 " + x.step : "",
      x.position || "",
    ),
  );
  return (
    <>
      <button onClick={() => open("")}>新对话</button>
      <div className="history-mode" aria-label="历史视图" role="group">
        <button
          aria-pressed={inbox === "recent"}
          onClick={() => setInbox("recent")}
        >
          最近对话
        </button>
        <button
          aria-pressed={inbox !== "recent"}
          onClick={() => setInbox(inbox === "recent" ? "review" : inbox)}
        >
          我的待办
        </button>
      </div>
      {inbox !== "recent" && (
        <label>
          待办类型
          <select
            value={inbox}
            onChange={(e) => setInbox(e.target.value as HistoryView)}
          >
            <option value="review">任务与审核</option>
            <option value="budget">预算追加</option>
            <option value="grant">能力授权</option>
          </select>
        </label>
      )}
      {inbox === "grant" ? (
        <ProjectAgentGrants inbox={{ query, setQuery, openProject: open }} />
      ) : inbox === "budget" ? (
        <BudgetInbox query={query} setQuery={setQuery} openProject={open} />
      ) : (
        <>
          <input
            type="search"
            aria-label="搜索历史和待办"
            placeholder={
              inbox === "review" ? "搜索项目、成果或待办" : "搜索对话或状态"
            }
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {inbox === "review" && (
            <ReviewReminders
              openProject={open}
              query={query}
              labels={actionLabels}
            />
          )}
          {error ? (
            <p role="alert">
              {error}{" "}
              <button onClick={() => setAttempt(attempt + 1)}>重试</button>
            </p>
          ) : loading ? (
            <p role="status">正在读取…</p>
          ) : inbox === "review" ? (
            pending.length ? (
              pending.map((t) => (
                <button
                  className="list"
                  key={taskAnchor(t) + t.project}
                  onClick={() => open(t.project, t)}
                >
                  <strong>{actionLabels[t.kind]}</strong> {t.name}
                  {t.version > 0 && " · v" + t.version}
                  <small>
                    {t.title}
                    {t.step > 0 && " · 步骤 " + t.step}
                    {t.position && " · " + t.position}
                  </small>
                </button>
              ))
            ) : (
              <p role="status">
                {needle ? "没有匹配的待办" : "当前没有需要你处理的待办"}
              </p>
            )
          ) : conversations.length ? (
            conversations.map((x) => (
              <button
                className="list"
                key={x.id}
                aria-current={current === x.id ? "page" : undefined}
                onClick={() => open(x.id)}
              >
                {x.title}
                <small>{stage(x)}</small>
              </button>
            ))
          ) : (
            <p role="status">{needle ? "没有匹配的对话" : "还没有对话"}</p>
          )}
        </>
      )}
    </>
  );
}

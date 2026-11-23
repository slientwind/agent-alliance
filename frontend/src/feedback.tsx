import { ChangeReview } from "./change-review";
import type { Source } from "./knowledge";
import { SourceComparison } from "./source-comparison";
import { useEffect, useRef, useState } from "react";
import {
  api,
  type Artifact,
  type Member,
  type Project,
  type FeedbackItem,
  type SourceQuote,
} from "./client";

const states: Record<string, string> = {
  suggested: "待判断建议",
  required: "已要求修改",
  investigate: "验收问题待核实",
  verification: "待审核人复核",
  closed: "已确认解决",
  declined: "已说明不采纳",
  withdrawn: "已撤回",
};
const actions: Record<string, string> = {
  open: "提交反馈",
  transfer: "转交负责审核人",
  withdraw: "撤回建议",
  accept: "采纳为修改项",
  decline: "不采纳",
  submit: "提交当前版本复核",
  resolve: "确认已解决",
  reopen: "要求继续修改",
};
export function blocksApproval(f: FeedbackItem) {
  return ["required", "investigate", "verification"].includes(f.state);
}

export type FeedbackDraft = {
  sourceQuote?: SourceQuote;
  version: number;
  kind: string;
  position: string;
  body: string;
};

function QuotedSource({ url, project }: { url: string; project: string }) {
  const [open, setOpen] = useState(false);
  const [quote, setQuote] = useState<{
    text: string;
    name: string;
    source: string;
    version: number;
    start: number;
    end: number;
  }>();
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    let ticket = 0;
    setQuote(undefined);
    setError("");
    if (!open) return;
    async function load() {
      const request = ++ticket;
      try {
        const value = await api<NonNullable<typeof quote>>(url);
        if (live && request === ticket) {
          setQuote(value);
          setError("");
        }
      } catch (e) {
        if (live && request === ticket) {
          setQuote(undefined);
          setError((e as Error).message);
        }
      }
    }
    const sync = () => {
      if (!document.hidden) void load();
    };
    void load();
    window.addEventListener("focus", sync);
    const timer = window.setInterval(sync, 10000);
    return () => {
      live = false;
      clearInterval(timer);
      window.removeEventListener("focus", sync);
    };
  }, [open, url]);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>查看反馈引用的原句</summary>
      {open &&
        (quote ? (
          <>
            <strong>
              {quote.name} · v{quote.version}
            </strong>
            <small>
              原文第 {quote.start}–{quote.end} 字
            </small>
            <p className="preserve">{quote.text}</p>
            <a
              href={
                "/api/projects/" +
                project +
                "/sources/" +
                quote.source +
                "/content?version=" +
                quote.version
              }
            >
              打开引用的资料版本
            </a>
          </>
        ) : (
          <p role="status">{error || "正在读取原句…"}</p>
        ))}
    </details>
  );
}

export function Feedback({
  artifact: a,
  sources,
  drafts,
  project: p,
  user,
  busy,
  perform,
}: {
  artifact: Artifact;
  sources: Source[];
  drafts: Map<string, FeedbackDraft>;
  project: Project;
  user: Member;
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
}) {
  const [draft, setDraft] = useState<FeedbackDraft | undefined>(() =>
    drafts.get(a.id),
  );
  const feedbackPanel = useRef<HTMLDetailsElement>(null);
  const bodyField = useRef<HTMLTextAreaElement>(null);
  const kind = draft?.kind || "suggestion";
  const draftVersion = draft?.version;
  const stale = draftVersion !== undefined && draftVersion !== a.version;
  function save(change: Partial<FeedbackDraft>) {
    const next = {
      ...{ version: a.version, kind: "suggestion", position: "", body: "" },
      ...draft,
      ...change,
    };
    drafts.set(a.id, next);
    setDraft(next);
  }
  const base = "projects/" + p.id + "/artifacts/" + a.id + "/feedback";
  const review = p.stage === "review";
  return (
    <>
      <SourceComparison
        project={p}
        artifact={a}
        sources={sources}
        onFeedback={
          review && !draft
            ? (note) => {
                if (feedbackPanel.current) feedbackPanel.current.open = true;
                save({ ...note, kind: "issue" });
                bodyField.current?.focus();
              }
            : undefined
        }
      />
      <details className="feedback" ref={feedbackPanel}>
        <summary>
          反馈{a.feedback.length ? " · " + a.feedback.length : ""}
        </summary>
        {a.feedback.map((f) => {
          const successors = p.participants.filter(
            (m) => m.canReview && m.enabled && m.id !== f.reviewer,
          );
          const choices: string[] = [];
          if (review) {
            if (f.state === "suggested" && user.id === f.author)
              choices.push("withdraw");
            if (
              f.state === "suggested" &&
              p.canReview &&
              user.id === f.reviewer
            )
              choices.push("accept", "decline");
            if (
              (p.canReview || p.owner === user.id) &&
              (f.state === "investigate" ||
                (f.state === "required" &&
                  a.version > Math.max(f.base, f.candidate || 0)) ||
                (f.state === "verification" && a.version !== f.candidate))
            )
              choices.push("submit");
            if (
              f.state === "verification" &&
              p.canReview &&
              user.id === f.reviewer &&
              a.version === f.candidate
            )
              choices.push("resolve", "reopen");
          }
          return (
            <section
              key={f.id}
              tabIndex={-1}
              id={
                review && p.canReview && user.id === f.reviewer
                  ? "feedback-" + f.id + "-" + a.version + "-" + f.revision
                  : undefined
              }
            >
              <strong>{states[f.state]}</strong>
              <small>
                负责审核：
                {p.participants.find((m) => m.id === f.reviewer)?.name} · 基于 v
                {f.base}
                {f.position && " · " + f.position}
              </small>
              <p>{f.body ?? "关联资料已撤权，反馈正文受限。"}</p>
              {f.submission &&
                p.feedbackOriginals[f.submission] !== undefined && (
                  <details>
                    <summary>项目反馈原文 · 第 {f.submissionItem} 项</summary>
                    <p className="preserve">
                      {p.feedbackOriginals[f.submission]}
                    </p>
                  </details>
                )}
              {f.quoteAvailable && (
                <QuotedSource
                  key={a.version}
                  project={p.id}
                  url={base + "/" + f.id + "/source-quote?version=" + a.version}
                />
              )}
              <ChangeReview
                project={p}
                artifact={a}
                feedback={f}
                user={user}
                busy={busy}
                perform={perform}
              />
              <details>
                <summary>处理记录</summary>
                <ol>
                  {f.events.map((e, i) => (
                    <li key={i}>
                      {e.actor} · {actions[e.action]} · v{e.version}
                      {e.action !== "open" && e.reason && <p>{e.reason}</p>}
                    </li>
                  ))}
                </ol>
              </details>
              {review &&
                [
                  "suggested",
                  "required",
                  "investigate",
                  "verification",
                ].includes(f.state) &&
                (p.reviewer === user.id ||
                  (f.reviewer === user.id && p.canReview)) &&
                successors.length > 0 && (
                  <details>
                    <summary>转交负责审核人</summary>
                    <form
                      onSubmit={(e) => {
                        e.preventDefault();
                        const data = new FormData(e.currentTarget);
                        void perform(() =>
                          api(base + "/" + f.id, "POST", {
                            version: a.version,
                            expected: f.revision,
                            action: "transfer",
                            assignee: data.get("assignee"),
                            reason: data.get("reason"),
                          }),
                        );
                      }}
                    >
                      <label>
                        接替审核人
                        <select name="assignee">
                          {successors.map((m) => (
                            <option key={m.id} value={m.id}>
                              {m.name}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label>
                        转交原因
                        <input required name="reason" />
                      </label>
                      <button disabled={busy}>确认转交</button>
                    </form>
                  </details>
                )}
              {choices.length > 0 && (
                <form
                  key={f.revision}
                  onSubmit={(e) => {
                    e.preventDefault();
                    const data = new FormData(e.currentTarget);
                    void perform(() =>
                      api(base + "/" + f.id, "POST", {
                        version: a.version,
                        expected: f.revision,
                        action: data.get("action"),
                        reason: data.get("reason"),
                      }),
                    );
                  }}
                >
                  <label>
                    处理方式
                    <select name="action">
                      {choices.map((action) => (
                        <option value={action} key={action}>
                          {actions[action]}
                        </option>
                      ))}
                    </select>
                  </label>
                  <label>
                    处理依据
                    <input name="reason" required maxLength={4000} />
                  </label>
                  <button disabled={busy}>记录决定</button>
                </form>
              )}
            </section>
          );
        })}
        {review && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const data = new FormData(e.currentTarget);
              void perform(async () => {
                await api(base, "POST", {
                  version: draftVersion ?? a.version,
                  kind,
                  position: data.get("position"),
                  sourceQuote: draft?.sourceQuote,
                  body: data.get("body"),
                });
                drafts.delete(a.id);
                setDraft(undefined);
              });
            }}
          >
            <label>
              反馈类型
              <select
                value={kind}
                onChange={(e) => save({ kind: e.target.value })}
              >
                <option value="suggestion">建议优化</option>
                {p.canReview && <option value="change">要求修改</option>}
                <option value="issue">验收问题</option>
              </select>
            </label>
            <label>
              位置（选填）
              <input
                name="position"
                value={draft?.position || ""}
                onChange={(e) => save({ position: e.target.value })}
                placeholder="例如第 2 页或表格 C3"
                maxLength={4000}
              />
            </label>
            <label>
              反馈内容
              <textarea
                name="body"
                ref={bodyField}
                value={draft?.body || ""}
                onChange={(e) => save({ body: e.target.value })}
                required
                maxLength={4000}
              />
              {draft && <small>草稿已在本页暂存 · v{draft.version}</small>}
            </label>
            <small>
              {kind === "suggestion"
                ? "建议由审核人判断是否采纳。"
                : "此项需要审核人复核后才能交付。"}
            </small>
            {draft?.sourceQuote && (
              <button
                type="button"
                onClick={() => save({ sourceQuote: undefined })}
              >
                移除原文引用
              </button>
            )}
            {stale && (
              <p role="status">
                这条意见基于 v{draftVersion}。成果已有 v{a.version}。{" "}
                <button
                  type="button"
                  onClick={() => save({ version: a.version })}
                >
                  已核对新稿，改用 v{a.version}
                </button>
              </p>
            )}
            <button disabled={busy || stale}>提交反馈</button>
          </form>
        )}
      </details>
    </>
  );
}

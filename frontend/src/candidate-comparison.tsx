import { useEffect, useState } from "react";
import { api } from "./client";

type Candidate = {
  id: string;
  name: string;
  teamName: string;
  sourceName: string;
  revision: number;
  ability: string;
  operation: string;
  state: "ready" | "conditional" | "unsuitable";
  published: boolean;
  admissible: boolean;
  estimateCny: string | null;
  lastVerified: string | null;
  trialCount: number;
  medianSeconds: number | null;
  configured: boolean;
  reviewDue: string | null;
  verifiedChecks: string[];
  projectShort: boolean;
  teamShort: boolean;
  resourceShort: boolean;
  match: "check" | "name" | "";
  needsReview: boolean;
};
const states = {
  ready: "可选用",
  conditional: "需补条件",
  unsuitable: "成果格式不符",
};
const operations: Record<string, string> = {
  png: "PNG 文生图",
  vision: "读取图片并生成文本",
  "image-edit": "编辑一张参考图并生成 PNG",
  "model-text": "模型文本",
  "external-text": "外部文本",
  extract: "资料摘录",
  aggregate: "分组汇总并生成 XLSX",
  docx: "生成 DOCX",
  pptx: "生成 PPTX",
  xlsx: "生成 XLSX",
};
export function CandidateComparison({
  project,
  recommended,
  add,
  disabled = false,
}: {
  project: string;
  recommended?: { id: string; revision: number }[];
  add?: (id: string, name: string, revision: number) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false),
    [priority, setPriority] = useState(""),
    [format, setFormat] = useState(""),
    [input, setInput] = useState(""),
    [query, setQuery] = useState(""),
    [epoch, refresh] = useState(0);
  const [items, setItems] = useState<Candidate[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  useEffect(() => {
    if (!open) return;
    let current = true;
    setLoading(true);
    setError("");
    setItems([]);
    api<Candidate[]>(
      "projects/" +
        project +
        "/candidates?" +
        new URLSearchParams({ output: format, q: query, priority }),
    )
      .then((result) => {
        if (current) setItems(result);
      })
      .catch((e) => {
        if (current) setError(e.message);
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [project, open, format, query, priority, epoch]);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>
        {recommended ? "核对推荐版本与替代能力" : "比较候选能力与验证依据"}
      </summary>
      {open && (
        <>
          <label>
            这一步的成果格式
            <select value={format} onChange={(e) => setFormat(e.target.value)}>
              <option value="">全部格式</option>
              {["text", "docx", "xlsx", "pptx", "png"].map((f) => (
                <option key={f} value={f}>
                  {f === "text" ? "文本" : f.toUpperCase()}
                </option>
              ))}
            </select>
          </label>
          <label>
            同等匹配条件下优先
            <select
              value={priority}
              onChange={(e) => setPriority(e.target.value)}
            >
              <option value="">默认顺序</option>
              <option value="cost">单次预估费用较低</option>
              <option value="time">已验证试运行耗时较短</option>
            </select>
          </label>
          <label>
            任务或验收关键词
            <input
              value={input}
              placeholder="留空按当前项目标题匹配"
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  setQuery(input.trim());
                  refresh((n) => n + 1);
                }
              }}
            />
          </label>
          <button
            type="button"
            onClick={() => {
              setQuery(input.trim());
              refresh((n) => n + 1);
            }}
          >
            更新比较
          </button>
          {loading ? (
            <p role="status">正在核对当前条件…</p>
          ) : error ? (
            <p role="alert">{error}</p>
          ) : !items.length ? (
            <p>当前没有可见候选。</p>
          ) : (
            <>
              <small>
                按单次调用核对预算，协作总额见保存后的方案估算。实际耗时和业务质量需结合任务样例验证。
              </small>
              {recommended?.some(
                (old) =>
                  !items.some(
                    (c) => c.id === old.id && c.revision === old.revision,
                  ),
              ) && (
                <p role="status">
                  部分推荐修订已更换或当前不可见，请重新规划或手动选择。
                </p>
              )}
              {items.map((c) => (
                <article className="card" key={c.id}>
                  <strong>
                    {c.name} · v{c.revision}
                  </strong>
                  <p>
                    {states[c.state]} · {operations[c.operation] || c.operation}{" "}
                    ·{" "}
                    {c.estimateCny === null
                      ? "费用待核对"
                      : "预计 ¥" + c.estimateCny + " / 次"}
                  </p>
                  {recommended?.some(
                    (old) => old.id === c.id && old.revision === c.revision,
                  ) && <strong>本次推荐修订</strong>}
                  {c.match && (
                    <small>
                      {c.match === "check"
                        ? "描述命中已通过的验收项"
                        : "描述包含能力名称"}
                    </small>
                  )}
                  <small>
                    {c.verifiedChecks.length
                      ? "已通过的验收项：" + c.verifiedChecks.join("、")
                      : c.admissible
                        ? "已通过本修订试运行，尚无分项验收证据"
                        : "暂无已通过的分项验收证据"}
                  </small>
                  {c.lastVerified && (
                    <small>
                      最近验证 · {new Date(c.lastVerified).toLocaleDateString()}
                    </small>
                  )}
                  <small>
                    {c.medianSeconds == null
                      ? "暂无已验证耗时"
                      : "有效试运行 " +
                        c.trialCount +
                        " 次 · 耗时中位数 " +
                        c.medianSeconds.toFixed(1) +
                        " 秒"}
                  </small>
                  {!c.configured && <small>需配置连接器或授权调用地址</small>}
                  {c.reviewDue && (
                    <small>
                      {c.needsReview ? "已到复查日" : "复查日"} ·{" "}
                      {new Date(c.reviewDue).toLocaleDateString()}
                    </small>
                  )}
                  {!c.published ? (
                    <small>草稿待验证或发布</small>
                  ) : (
                    !c.admissible && <small>需复验、恢复资源或重新授权</small>
                  )}
                  {(c.resourceShort || c.projectShort || c.teamShort) && (
                    <small>
                      额度不足 ·{" "}
                      {[
                        c.projectShort && "项目",
                        c.teamShort && "承担团队",
                        c.resourceShort && "资源",
                      ]
                        .filter(Boolean)
                        .join("、")}
                    </small>
                  )}
                  <details>
                    <summary>能力声明</summary>
                    <p>{c.ability}</p>
                    <small>
                      资源 · {c.sourceName} · 承担团队 · {c.teamName}
                    </small>
                  </details>
                  {add && c.state === "ready" && (
                    <button
                      type="button"
                      disabled={disabled}
                      onClick={() => add(c.id, c.name, c.revision)}
                    >
                      加入方案步骤
                    </button>
                  )}
                </article>
              ))}
            </>
          )}
        </>
      )}
    </details>
  );
}

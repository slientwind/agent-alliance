import { useState } from "react";
import { api, type CallReview, type Project } from "./client";

export function CallRecovery({
  record,
  project,
  run,
  state,
  revision,
  canReview,
  model,
  busy,
  perform,
}: {
  record: CallReview | null;
  project: string;
  run: string;
  state: string;
  revision: number;
  canReview: boolean;
  model?: Project["planningModel"];
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
}) {
  const [decision, setDecision] = useState("hold");
  if (!record || (record.hasReceipt && !record.history.length)) return null;
  const pending =
    (state === "reconcile" ||
      (state === "running" && record.window?.overdue)) &&
    !record.hasReceipt &&
    ["unknown", "submitted"].includes(record.status);
  const issues = record.inputIssues ?? (record.inputIssue ? [record.inputIssue] : []);
  const suggestion = record.columnSuggestion;
  const proposed = new Map(
    (suggestion?.entries ?? []).map(({ file, field, column }) => [
      `column-${file - 1}-${field - 1}`,
      issues[file - 1]?.choices[column - 1],
    ]),
  );
  const comparableColumnName = (value: string) =>
    value.normalize("NFC").trim().toLowerCase();
  const reconfirm = Boolean(record.reconfirmationRequired);
  const canCorrect =
    issues.length > 0 && (state === "reconcile" || reconfirm) && !record.authorized;
  if (
    !canCorrect &&
    !record.authorized &&
    !pending &&
    !record.history.length &&
    !(record.window && state === "running")
  )
    return null;
  return (
    <details open={canCorrect || undefined}>
      <summary>
        步骤 {record.step} · {reconfirm && canCorrect ? "重新确认汇总字段" : canCorrect ? "核对汇总字段" : "调用核对"}
      </summary>
      {canCorrect && (
        <form
          className="column-correction"
          key={record.attempt}
          onSubmit={(event) => {
            event.preventDefault();
            const form = new FormData(event.currentTarget);
            const inputMappings = issues.map((issue, file) => ({
              source: issue.source,
              version: issue.version,
              columns: Object.fromEntries(
                issue.missing.map((field, index) => [
                  field,
                  String(form.get(`column-${file}-${index}`)),
                ]),
              ),
            }));
            void perform(() =>
              api(
                "runs/" + run + "/calls/" + record.step + "/review",
                "POST",
                {
                  attempt: record.attempt,
                  expected: revision,
                  decision: "redo",
                  evidence: "已核对原表列与汇总字段的对应关系",
                  inputMappings,
                  authorizeOneCall: form.get("authorize") === "on",
                },
                { "Idempotency-Key": crypto.randomUUID() },
              ),
            );
          }}
        >
          {reconfirm ? (
            <p role="status">上一次确认的审核职责已变化。请重新核对字段，并授权本次重算。</p>
          ) : (
            <p>请核对以下资料的表头，再一次确认全部字段。</p>
          )}
          {reconfirm && state === "paused" && <p>本次确认会保存，任务继续后才会派发。</p>}
          {model?.enabled && !suggestion && !reconfirm && (
            <div>
              <p>
                可以让 {model.name} 根据字段名和用途推荐原表列。实际费用由
                {model.payer || "团队"}承担{model.estimate != null ? `，预计 ¥${model.estimate}` : "，金额未知"}。
              </p>
              <button
                type="button"
                disabled={busy}
                onClick={() =>
                  void perform(() =>
                    api("runs/" + run + "/calls/" + record.step + "/column-suggestion", "POST", {
                      attempt: record.attempt,
                      expected: revision,
                      authorizeOneCall: true,
                    }),
                  )
                }
              >
                授权一次模型调用并推荐字段
              </button>
            </div>
          )}
          {suggestion?.status === "submitted" && <p role="status">Agent 正在核对字段。你也可以手动选择。</p>}
          {suggestion?.status === "ambiguous" && <p role="status">Agent 无法判断字段含义，请核对原表。</p>}
          {suggestion && ["unknown", "invalid", "stale"].includes(suggestion.status) && (
            <p role="status">
              {suggestion.status === "unknown"
                ? "模型调用结果待核对，你可以继续手动选择。"
                : "本次模型建议不可用，请核对原表。"}
            </p>
          )}
          {suggestion?.status === "ready" && proposed.size > 0 && (
            <div>
              <p role="status">Agent 找到 {proposed.size} 处建议，请核对字段含义。</p>
              <button type="button" disabled={busy} onClick={(event) => {
                const form = event.currentTarget.form;
                for (const [field, choice] of proposed) {
                  const select = form?.elements.namedItem(field);
                  if (select instanceof HTMLSelectElement && select.value === "" && choice) select.value = choice;
                }
              }}>将建议填入空白字段</button>
            </div>
          )}
          {issues.map((issue, file) => (
            <fieldset key={issue.source}>
              <legend>
                <a
                  href={
                    "/api/projects/" +
                    project +
                    "/sources/" +
                    issue.source +
                    "/content?version=" +
                    issue.version
                  }
                  download
                >
                  {issue.name} · v{issue.version}
                </a>
              </legend>
              {issue.missing.map((field, index) => {
                const fieldName = `column-${file}-${index}`;
                const matches = issue.choices.filter(
                  (column) => comparableColumnName(column) === comparableColumnName(field),
                );
                return (
                  <label key={field}>
                    {issue.roles?.[field] && issue.roles[field] + "字段 "}
                    {field} 对应的原表列
                    <select required name={fieldName} defaultValue="">
                      <option value="" disabled>选择原表列</option>
                      {issue.choices.map((column) => (
                        <option key={column} value={column}>
                          {column}{proposed.get(fieldName) === column
                            ? "（Agent 建议）"
                            : matches.length === 1 && column === matches[0] ? "（同名候选）" : ""}
                        </option>
                      ))}
                    </select>
                  </label>
                );
              })}
            </fieldset>
          ))}
          <label>
            <input type="checkbox" required name="authorize" />{" "}
            我已核对字段含义，授权本步骤增加一次计算。
          </label>
          <button className="primary" disabled={busy}>
            {reconfirm ? "重新确认字段并授权重算" : "按对应字段重新计算"}
          </button>
        </form>
      )}
      {record.status === "input_rejected" && record.authorized && (
        <p>已确认字段对应，等待原任务继续。</p>
      )}
      {record.window && (
        <small>
          执行截至 {new Date(record.window.executionDue).toLocaleString()} ·
          原回执核对截至 {new Date(record.window.receiptDue).toLocaleString()}
        </small>
      )}
      {record.lateReceipts.map((l) => (
        <p key={l.attempt}>
          第 {l.attempt} 次的迟到回执 · {new Date(l.time).toLocaleString()} ·{" "}
          {l.outcome === "completed"
            ? "已返回结果"
            : l.outcome === "input_rejected"
              ? "工具已返回输入拒绝回执"
              : l.hasRemoteTask
                ? "已收到远端任务编号"
                : "结果仍未知"}
          。已单独留存核对记录。
          {canReview && (
            <a
              href={
                "/api/runs/" +
                run +
                "/calls/" +
                record.step +
                "/attempts/" +
                l.attempt +
                "/late-receipt"
              }
              download={"receipt-" + l.attempt + ".json"}
            >
              下载第 {l.attempt} 次调用的核对回执
            </a>
          )}
          {l.fingerprint && <small>结果指纹 SHA-256 · {l.fingerprint}</small>}
        </p>
      ))}
      {record.history.map((h, i) => (
        <p key={i}>
          第 {h.attempt} 次调用 · {h.actor} ·{" "}
          {h.corrected
            ? "已确认原表字段并授权一次计算"
            : h.decision === "redo"
              ? "已授权重新执行一次"
              : "继续等待"}{" "}
          · {new Date(h.time).toLocaleString()}
          {h.superseded && " · 已由后续确认接替"}
          <br />
          {h.evidence ?? "核对依据受限"}
          {(h.corrections ?? (h.correction ? [h.correction] : [])).map((mapping, index) => (
            <small key={index}>
              《{mapping.name}》v{mapping.version} ·{" "}
              {Object.entries(mapping.columns)
                .map(([field, original]) => field + " 取自 " + original)
                .join("，")}
            </small>
          ))}
          {h.timedOut &&
            h.receiptDue &&
            "（原回执核对截至 " +
              new Date(h.receiptDue).toLocaleString() +
              "）"}
        </p>
      ))}
      {pending && (
        <p>
          第 {record.attempt}{" "}
          次调用缺少可查询的远端任务编号。请核对供应商记录。原调用可能已经执行和计费。
        </p>
      )}
      {pending && record.status === "submitted" && (
        <small>
          {record.window?.expired
            ? "核对时限已过。可以继续等待，或在核对依据后单独授权重新执行。原请求仍可能返回。"
            : "原请求仍可能返回。可继续等待，或在运行控制中取消后续执行。"}
        </small>
      )}
      {pending && record.authorized && (
        <small>已授权一次新调用，等待其他待核对步骤和执行条件就绪。</small>
      )}
      {pending && canReview && !record.authorized && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const form = new FormData(e.currentTarget);
            void perform(() =>
              api(
                "runs/" + run + "/calls/" + record.step + "/review",
                "POST",
                {
                  attempt: record.attempt,
                  expected: revision,
                  decision,
                  evidence: form.get("evidence"),
                  authorizeOneCall: form.get("authorize") === "on",
                },
                { "Idempotency-Key": crypto.randomUUID() },
              ),
            );
          }}
        >
          <label>
            核对决定
            <select
              value={decision}
              onChange={(e) => setDecision(e.target.value)}
            >
              <option value="hold">记录依据，继续等待</option>
              {(record.status === "unknown" || record.window?.expired) && (
                <option value="redo">承担重复风险，重新执行一次</option>
              )}
            </select>
          </label>
          <label>
            核对依据（项目成员可见）
            <textarea required name="evidence" maxLength={1600} />
          </label>
          {decision === "redo" && (
            <label>
              <input type="checkbox" name="authorize" required />
              我已核对重复执行和重复计费风险，授权团队承担本次新增调用。
            </label>
          )}
          <button disabled={busy}>记录核对决定</button>
        </form>
      )}
    </details>
  );
}

import { FilePreview } from "./file-preview";
import { useState, useEffect } from "react";
import { api } from "./client";
type Trial = {
  canReuseSample?: boolean;
  id: string;
  withdrawn: string | null;
  canWithdraw: boolean;
  checkName: string | null;
  file: string | null;
  picture?: string | null;
  verdict: boolean | null;
  reason: string | null;
  reviewedBy: string | null;
  reviewedAt: string | null;
  revision: number;
  status: string;
  reviewDue: string | null;
  remoteState: string | null;
  output: string | null;
  canCheck: boolean;
  cancelRequested: boolean;
  remoteOperations?: { operation: string; outcome: string; state?: string; time: string }[];
};
const operationLabels: Record<string, string> = {
  send: "提交", query: "查询原任务", cancel: "取消请求",
};
const operationOutcomes: Record<string, string> = {
  text: "已取得文本", state: "已核对任务状态", unconfirmed: "回执待核对",
};
const operationStates: Record<string, string> = {
  submitted: "已受理", working: "执行中", "input-required": "等待输入",
  "auth-required": "等待授权", completed: "已完成", canceled: "已取消", failed: "失败", rejected: "已拒绝",
};
export function TrialEvidence({
  agent,
  refresh,
  version,
  onReuse,
  revision,
}: {
  agent: string;
  refresh: () => Promise<void>;
  version: number;
  revision?: number;
  onReuse?: (sample: { input: string; expected: string }) => void;
}) {
  const [open, setOpen] = useState(false),
    [items, setItems] = useState<Trial[]>([]),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [epoch, bump] = useState(0);
  const base = "agents/" + agent + "/evidence";
  useEffect(() => {
    if (!open) return;
    let active = true;
    api<Trial[]>(base)
      .then((v) => {
        if (active) setItems(v);
      })
      .catch((e) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [base, open, epoch, version]);
  async function follow(id: string, action: string) {
    setBusy(true);
    try {
      await api(base + "/" + id + "/receipt", "POST", { action });
      bump((x) => x + 1);
      await refresh();
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>试运行记录与回执</summary>
      {error && <p role="alert">{error}</p>}
      {items.filter((t) => revision === undefined || t.revision === revision).map((t) => (
        <section key={t.id}>
          <strong>
            v{t.revision} ·{" "}
            {t.remoteState === "canceled" && t.remoteOperations?.some(item => item.operation === "cancel" && item.state === "canceled")
              ? "远端已确认取消"
              : (
              {
                submitted: "已受理，等待结果",
                passed: "验证通过",
                remote: "等待远端回执",
                failed: "验证未通过",
                unknown: "结果待核对",
              } as Record<string, string>
            )[t.status] || t.status}
          </strong>
          {t.withdrawn && <strong>证据已失效 · {t.withdrawn}</strong>}
          {t.canWithdraw && (
            <details>
              <summary>标记证据失效</summary>
              <form
                onSubmit={async (e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  setBusy(true);
                  try {
                    await api(base + "/" + t.id + "/withdraw", "POST", {
                      reason: f.get("reason"),
                    });
                    bump((x) => x + 1);
                    await refresh();
                    setError("");
                  } catch (err) {
                    setError((err as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                <label>
                  失效原因
                  <input required name="reason" />
                </label>
                <button disabled={busy}>确认标记失效</button>
              </form>
            </details>
          )}
          {t.reviewDue && (
            <small>
              {Date.parse(t.reviewDue) <= Date.now()
                ? "已到复查日期"
                : "建议复查日期"}{" "}
              · {new Date(t.reviewDue).toLocaleDateString("zh-CN")}
            </small>
          )}
          {t.checkName && (
            <small>
              {t.checkName}
              {t.verdict !== null &&
                " · " +
                  t.reviewedBy +
                  " · " +
                  (t.verdict ? "验收通过" : "验收未通过") +
                  " · " +
                  t.reason}
            </small>
          )}
          {t.file && (
            <>
              <FilePreview
                name={t.file}
                url={"/api/" + base + "/" + t.id + "/file"}
              />
              <a href={"/api/" + base + "/" + t.id + "/file"}>下载试运行原件</a>
            </>
          )}
          {t.picture && <FilePreview name={t.picture} url={"/api/" + base + "/" + t.id + "/picture"} />}
          {t.output && <p className="preserve">{t.output}</p>}
          {!!t.remoteOperations?.length && (
            <ul aria-label="远端操作回执">
              {t.remoteOperations.map((item, index) => (
                <li key={index}>
                  {operationLabels[item.operation] || item.operation} · {operationOutcomes[item.outcome] || item.outcome}
                  {item.state && operationStates[item.state] && " · " + operationStates[item.state]}
                  <small>{new Date(item.time).toLocaleString("zh-CN")}</small>
                </li>
              ))}
            </ul>
          )}
          {onReuse && t.canReuseSample && (
            <button
              type="button"
              disabled={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  const sample = await api<{ input: string; expected: string }>(
                    base + "/" + t.id + "/sample",
                  );
                  onReuse(sample);
                  setError("");
                } catch (err) {
                  setError((err as Error).message);
                } finally {
                  setBusy(false);
                }
              }}
            >
              沿用这条样例
            </button>
          )}
          {t.canCheck && (
            <div className="actions">
              <button
                disabled={busy}
                onClick={() => void follow(t.id, "check")}
              >
                查询原试运行
              </button>
              <button
                disabled={busy || t.cancelRequested}
                onClick={() => void follow(t.id, "cancel")}
              >
                {t.cancelRequested ? "已申请取消" : "取消远端试运行"}
              </button>
            </div>
          )}
        </section>
      ))}
    </details>
  );
}

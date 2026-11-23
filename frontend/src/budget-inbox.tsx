import { useEffect, useState } from "react";
import { api } from "./client";
import { BudgetReceipt } from "./budget-requests";
import { quantity, targetName, type Receipt } from "./budget-request-types";

type Entry = Pick<
  Receipt,
  | "id"
  | "project"
  | "projectTitle"
  | "team"
  | "requester"
  | "created"
  | "reason"
  | "scope"
  | "amount"
  | "resourceName"
>;

export function BudgetInbox({
  query,
  setQuery,
  openProject,
}: {
  query: string;
  setQuery: (q: string) => void;
  openProject: (id: string) => void;
}) {
  const [id, select] = useState("");
  const [rows, setRows] = useState<Entry[]>([]);
  const [receipt, setReceipt] = useState<Receipt | null>(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [epoch, reload] = useState(0);
  function navigate(next: string) {
    select(next);
    setReceipt(null);
    setRows([]);
    setMessage("");
    setError("");
    setLoading(true);
  }
  useEffect(() => {
    let sequence = 0;
    const load = async () => {
      if (document.hidden) return;
      const n = ++sequence;
      try {
        if (id) {
          const item = await api<Receipt>("budget-inbox/" + id);
          if (n === sequence) setReceipt(item);
        } else {
          const items = await api<Entry[]>("budget-inbox");
          if (n === sequence) setRows(items);
        }
        if (n === sequence) setError("");
      } catch (e) {
        if (n === sequence) {
          setRows([]);
          setReceipt(null);
          setError((e as Error).message);
        }
      } finally {
        if (n === sequence) setLoading(false);
      }
    };
    void load();
    window.addEventListener("focus", load);
    document.addEventListener("visibilitychange", load);
    return () => {
      ++sequence;
      window.removeEventListener("focus", load);
      document.removeEventListener("visibilitychange", load);
    };
  }, [id, epoch]);
  async function act(fn: () => Promise<void>, success: string) {
    if (busy) return;
    setBusy(true);
    try {
      await fn();
      setMessage(success);
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setLoading(true);
      setReceipt(null);
      reload((v) => v + 1);
      setBusy(false);
    }
  }
  const needle = query.trim().toLocaleLowerCase();
  const matches = rows.filter((r) =>
    [
      r.projectTitle,
      r.project,
      r.team,
      r.requester,
      r.reason,
      r.resourceName,
      targetName(r.scope, r.resourceName || r.team),
      quantity(r.scope, r.amount),
    ]
      .join(" ")
      .toLocaleLowerCase()
      .includes(needle),
  );
  return (
    <>
      {id ? (
        <button type="button" disabled={busy} onClick={() => navigate("")}>
          返回预算待办
        </button>
      ) : (
        <input
          type="search"
          aria-label="搜索预算待办"
          placeholder="搜索项目、团队或申请人"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      )}
      <button
        type="button"
        disabled={busy || loading}
        onClick={() => reload((v) => v + 1)}
      >
        刷新预算待办
      </button>
      {error ? (
        <p role="alert">{error}</p>
      ) : loading ? (
        <p role="status">正在读取预算申请…</p>
      ) : id ? (
        receipt && (
          <>
            <BudgetReceipt receipt={receipt} busy={busy} act={act} />
            {receipt.projectTitle && (
              <button
                type="button"
                onClick={() => openProject(receipt.project)}
              >
                打开所属项目
              </button>
            )}
          </>
        )
      ) : matches.length ? (
        matches.map((r) => (
          <button
            type="button"
            className="list"
            key={r.id}
            onClick={() => navigate(r.id)}
          >
            <strong>追加 {quantity(r.scope, r.amount)}</strong> ·{" "}
            {targetName(r.scope, r.resourceName || r.team)}
            <small>{r.reason}</small>
            <small>
              {r.projectTitle || "项目 " + r.project} · {r.team} · {r.requester}{" "}
              · {new Date(r.created).toLocaleString()}
            </small>
          </button>
        ))
      ) : (
        <p role="status">
          {needle ? "没有匹配的预算申请" : "当前没有需要你审批的预算申请"}
        </p>
      )}
      {message && <p role="status">{message}</p>}
    </>
  );
}

import { useCallback, useEffect, useRef, useState } from "react";
import { api, type Agent } from "./client";

const statuses = {
  open: "待处理",
  accepted: "已受理",
  declined: "已关闭",
  withdrawn: "已撤回",
  resolved: "已关联发布版本",
};
type State = keyof typeof statuses;
type Improvement = {
  id: string;
  agent: string;
  name: string;
  base: number;
  description: string;
  acceptance: string;
  requestedBy: string;
  maintainer: string;
  maintainerAvailable: boolean;
  mine: boolean;
  assigned: boolean;
  state: State;
  version: number;
  createdAt: string;
  result: number | null;
  availablePublication: number | null;
  decisions: { state: State; reason: string; actor: string; at: string }[];
};
const updated = "alliance-agent-improvements-updated";
export function RequestImprovement({ agent }: { agent: Agent }) {
  const [base, setBase] = useState(agent.published || agent.revision),
    [description, setDescription] = useState(""),
    [acceptance, setAcceptance] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [receipt, setReceipt] = useState("");
  const submission = useRef<{ signature: string; key: string } | null>(null);
  async function submit() {
    if (busy) return;
    setBusy(true);
    setError("");
    const signature = JSON.stringify([agent.id, base, description, acceptance]);
    if (submission.current?.signature !== signature)
      submission.current = { signature, key: crypto.randomUUID() };
    try {
      const reply = await api<{ id: string }>(
        "agents/" + agent.id + "/improvements",
        "POST",
        { base, description, acceptance },
        { "Idempotency-Key": submission.current.key },
      );
      setReceipt(reply.id);
      window.dispatchEvent(new Event(updated));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <details>
      <summary>向维护者提出增强需求</summary>
      {receipt ? (
        <p role="status">
          需求已提交，可在团队能力库的「能力增强需求」查看进度。编号 {receipt}
        </p>
      ) : (
        <fieldset className="plan-fields" disabled={busy}>
          <label>
            需求针对的版本
            <select
              aria-label="需求针对的版本"
              value={base}
              onChange={(e) => setBase(Number(e.target.value))}
            >
              {agent.published > 0 && (
                <option value={agent.published}>
                  已发布 v{agent.published}
                </option>
              )}
              {agent.revision !== agent.published && (
                <option value={agent.revision}>草稿 v{agent.revision}</option>
              )}
            </select>
          </label>
          <label>
            需要增强什么
            <textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
          <label>
            如何验收增强结果
            <textarea
              value={acceptance}
              onChange={(e) => setAcceptance(e.target.value)}
            />
          </label>
          <button
            type="button"
            disabled={!description.trim() || !acceptance.trim()}
            onClick={() => void submit()}
          >
            提交增强需求
          </button>
          {error && <p role="alert">{error}</p>}
        </fieldset>
      )}
    </details>
  );
}
function ImprovementRecord({
  item,
  refresh,
}: {
  item: Improvement;
  refresh: () => Promise<void>;
}) {
  const [reason, setReason] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function decide(action: string) {
    setBusy(true);
    setError("");
    try {
      await api("agent-improvements/" + item.id + "/decisions", "POST", {
        expected: item.version,
        action,
        reason,
        publication: action === "resolve" ? item.availablePublication : 0,
      });
      setReason("");
      await refresh();
    } catch (err) {
      setError((err as Error).message);
      await refresh();
    } finally {
      setBusy(false);
    }
  }
  const live = item.state === "open" || item.state === "accepted";
  return (
    <article className="card">
      <strong>
        {item.name} · v{item.base}
      </strong>
      <p>{item.description}</p>
      <small>验收要求 · {item.acceptance}</small>
      <p>
        <strong>{statuses[item.state]}</strong>
        {item.result && " · v" + item.result}
      </p>
      <small>
        提出人 · {item.requestedBy}　维护者 · {item.maintainer}
        {!item.maintainerAvailable && "（当前不可处理，需交接维护职责）"}
      </small>
      {item.decisions.map((d, i) => (
        <p key={i}>
          <small>
            {new Date(d.at).toLocaleString()} · {d.actor} · {statuses[d.state]}
          </small>
          {d.reason}
        </p>
      ))}
      {live && (
        <>
          <label>
            处理依据
            <textarea
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          </label>
          <div className="actions">
            {item.assigned && item.state === "open" && (
              <button
                disabled={!reason.trim() || busy}
                onClick={() => void decide("accept")}
              >
                受理需求
              </button>
            )}
            {item.assigned && (
              <button
                disabled={!reason.trim() || busy}
                onClick={() => void decide("decline")}
              >
                关闭并说明原因
              </button>
            )}
            {item.mine && (
              <button
                disabled={!reason.trim() || busy}
                onClick={() => void decide("withdraw")}
              >
                撤回需求
              </button>
            )}
            {item.assigned &&
              item.state === "accepted" &&
              (item.availablePublication ? (
                <button
                  disabled={!reason.trim() || busy}
                  onClick={() => void decide("resolve")}
                >
                  关联已发布 v{item.availablePublication} 并完成
                </button>
              ) : (
                <small>新修订通过验证并发布后可关联。</small>
              ))}
          </div>
        </>
      )}
      {error && <p role="alert">{error}</p>}
    </article>
  );
}
export function ImprovementInbox() {
  const [open, setOpen] = useState(false),
    [items, setItems] = useState<Improvement[]>([]),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(true);
  const latest = useRef(0);
  const refresh = useCallback(async () => {
    const id = ++latest.current;
    try {
      const rows = await api<Improvement[]>("agent-improvements");
      if (id === latest.current) {
        setItems(rows);
        setError("");
      }
    } catch (err) {
      if (id === latest.current) {
        setItems([]);
        setError((err as Error).message);
      }
    } finally {
      if (id === latest.current) setLoading(false);
    }
  }, []);
  useEffect(() => {
    void refresh();
    const sync = () => {
      void refresh();
    };
    window.addEventListener(updated, sync);
    window.addEventListener("focus", sync);
    return () => {
      ++latest.current;
      window.removeEventListener(updated, sync);
      window.removeEventListener("focus", sync);
    };
  }, [refresh]);
  const pending = items.filter((i) => i.assigned && i.state === "open").length;
  return (
    <details
      onToggle={(e) => {
        const next = e.currentTarget.open;
        setOpen(next);
        if (next) void refresh();
      }}
    >
      <summary>
        能力增强需求{pending > 0 && <strong> · 待受理 {pending}</strong>}
      </summary>
      {open && (
        <>
          <button type="button" onClick={() => void refresh()}>
            刷新处理记录
          </button>
          {error ? (
            <p role="alert">{error}</p>
          ) : loading ? (
            <p>正在读取…</p>
          ) : items.length ? (
            items.map((i) => (
              <ImprovementRecord key={i.id} item={i} refresh={refresh} />
            ))
          ) : (
            <p>暂无与你有关的需求。</p>
          )}
        </>
      )}
    </details>
  );
}

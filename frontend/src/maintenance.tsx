import { useCallback, useEffect, useState } from "react";
import { api } from "./client";
type Maintenance = {
  owner: string;
  available: boolean;
  version: number;
  canNominate: boolean;
  pending: boolean;
  target: string | null;
  canAccept: boolean;
  canCancel: boolean;
  reason: string | null;
};
export function MaintenancePanel({
  path,
  subject,
  version,
  invited,
  refresh,
}: {
  path: string;
  subject: "agent" | "rule";
  version?: number;
  invited: boolean;
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [data, setData] = useState<Maintenance | null>(null),
    [error, setError] = useState(""),
    [email, setEmail] = useState(""),
    [reason, setReason] = useState(""),
    [busy, setBusy] = useState(false);
  const load = useCallback(async () => {
    setData(await api<Maintenance>(path));
  }, [path]);
  useEffect(() => {
    if (!open) return;
    let live = true;
    setData(null);
    api<Maintenance>(path)
      .then((row) => {
        if (live) {
          setData(row);
          setError("");
        }
      })
      .catch((e) => {
        if (live) {
          setData(null);
          setError(e.message);
        }
      });
    return () => {
      live = false;
    };
  }, [path, open, version]);
  async function mutate(action: string) {
    if (!data) return;
    setBusy(true);
    setError("");
    try {
      await api(path, "POST", {
        expected: data.version,
        action,
        reason,
        email,
      });
      setEmail("");
      setReason("");
      await load();
      await refresh();
      if (subject === "agent")
        window.dispatchEvent(new Event("alliance-agent-improvements-updated"));
    } catch (e) {
      setError((e as Error).message);
      await load().catch(() => setData(null));
    } finally {
      setBusy(false);
    }
  }
  return (
    <details
      onToggle={(e) => {
        setOpen(e.currentTarget.open);
        if (!e.currentTarget.open) {
          setData(null);
          setError("");
          setReason("");
          setEmail("");
        }
      }}
    >
      <summary className={invited ? "action-needed" : undefined}>
        {invited ? "待接受维护职责" : "维护者与交接"}
      </summary>
      {data && open && (
        <>
          <p>
            当前维护者 · <strong>{data.owner}</strong> ·{" "}
            {data.available ? "在岗" : "职责待交接"}
          </p>
          {data.pending && (
            <p>
              等待 {data.target} 接受。{data.reason}
            </p>
          )}
          <small>
            {subject === "agent"
              ? "交接范围为此 Agent 的增强需求与验证证据维护。"
              : "接任者负责这条规则的日常维护和复查。已发布版本、到期日和项目引用保留。"}
            接任者接受后生效。版本发布仍需发布权限。
          </small>
          {((!data.pending && data.canNominate) ||
            (data.pending && (data.canAccept || data.canCancel))) && (
            <fieldset disabled={busy} className="plan-fields">
              {!data.pending && (
                <label>
                  接任人团队邮箱
                  <input
                    type="email"
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                  />
                </label>
              )}
              <label>
                交接依据
                <textarea
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                />
              </label>
              <div className="actions">
                {!data.pending && (
                  <button
                    type="button"
                    disabled={!email.trim() || !reason.trim()}
                    onClick={() => void mutate("nominate")}
                  >
                    提名接任人
                  </button>
                )}
                {data.pending && data.canAccept && (
                  <button
                    type="button"
                    disabled={!reason.trim()}
                    onClick={() => void mutate("accept")}
                  >
                    接受维护职责
                  </button>
                )}
                {data.pending && data.canCancel && (
                  <button
                    type="button"
                    disabled={!reason.trim()}
                    onClick={() => void mutate("cancel")}
                  >
                    取消本次交接
                  </button>
                )}
              </div>
            </fieldset>
          )}
        </>
      )}
      {error && <p role="alert">{error}</p>}
    </details>
  );
}

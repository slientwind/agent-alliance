import { useEffect, useState } from "react";
import { api, type Member } from "./client";
type Policy = {
  id: string;
  name: string;
  days: number;
  revision: number;
  manager: string | null;
};
export function TracePolicies({ members }: { members?: Member[] }) {
  const [rows, setRows] = useState<Policy[]>([]),
    [error, setError] = useState("");
  async function load() {
    setRows(await api<Policy[]>("trace-policies"));
  }
  useEffect(() => {
    void load().catch((e) => setError(e.message));
  }, []);
  if (!error && rows.length === 0) return null;
  return (
    <details className="card">
      <summary>团队诊断保留策略</summary>
      <p>运行结束时固定日志到期日。调整期限适用于此后结束的运行。</p>
      {error && <p role="alert">{error}</p>}
      {rows.map((row) => (
        <form
          key={row.id + ":" + row.revision}
          onSubmit={async (e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            try {
              const input = {
                days: Number(f.get("days")),
                expected: row.revision,
                reason: f.get("reason"),
                ...(members && f.get("manager") !== (row.manager || "")
                  ? { manager: f.get("manager") }
                  : {}),
              };
              await api("teams/" + row.id + "/trace-policy", "PATCH", input);
              await load();
              setError("");
            } catch (e) {
              setError((e as Error).message);
              void load().catch(() => {});
            }
          }}
        >
          <h3>{row.name}</h3>
          <label>
            日志保留天数
            <input
              name="days"
              type="number"
              min={1}
              max={365}
              defaultValue={row.days}
              required
            />
          </label>
          {members && (
            <label>
              日志策略负责人
              <select name="manager" defaultValue={row.manager || ""}>
                <option value="">由管理员维护</option>
                {members
                  .filter((m) => m.enabled)
                  .map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
              </select>
            </label>
          )}
          <label>
            策略变更原因
            <input name="reason" required />
          </label>
          <button>更新日志策略</button>
        </form>
      ))}
    </details>
  );
}
type Health = {
  waiting: number;
  retrying: number;
  delivered: number;
  rejected: number;
};
export function TelemetryHealth() {
  const [open, setOpen] = useState(false),
    [data, setData] = useState<Health | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let active = true;
    async function load() {
      try {
        const v = await api<Health>("admin/telemetry");
        if (active) {
          setData(v);
          setError("");
        }
      } catch (e) {
        if (active) setError((e as Error).message);
      }
    }
    void load();
    const timer = setInterval(load, 10000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [open]);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>Trace 采集状态</summary>
      {data && (
        <p>
          待发送 {data.waiting} · 等待重试 {data.retrying} · 已接收{" "}
          {data.delivered} · 接收端拒绝 {data.rejected}
        </p>
      )}
      {data?.waiting ? (
        <small>Worker 配置接收端后会发送保留期内的脱敏记录。</small>
      ) : null}
      {error && <p role="alert">{error}</p>}
    </details>
  );
}

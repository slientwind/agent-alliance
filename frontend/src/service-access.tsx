import { useEffect, useState } from "react";
import { api } from "./client";
type Grant = {
  id: string;
  name: string;
  expires: string;
  state: "active" | "expired" | "revoked" | "unavailable";
};
type Access = { applications: Grant[]; delegations: Grant[] };
const labels = {
  active: "有效",
  expired: "已到期",
  unavailable: "授权待恢复",
  revoked: "已撤销",
};
export function ServiceAccess({ id, admin }: { id: string; admin: boolean }) {
  const [data, setData] = useState<Access>(),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false),
    [credential, setCredential] = useState(""),
    [open, setOpen] = useState(false),
    [epoch, refresh] = useState(0);
  const path = "services/" + id;
  useEffect(() => {
    if (!open) return;
    let live = true;
    api<Access>(path + "/access")
      .then((x) => {
        if (live) setData(x);
      })
      .catch((e) => {
        if (live) {
          setError(e.message);
          setData(undefined);
          setCredential("");
        }
      });
    return () => {
      live = false;
    };
  }, [open, path, epoch]);
  async function act(fn: () => Promise<string>) {
    setError("");
    setBusy(true);
    setCredential("");
    try {
      setCredential(await fn());
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
      refresh((x) => x + 1);
    }
  }
  function grants(items: Grant[], isApp: boolean) {
    return items.map((g) => (
      <p key={g.id}>
        <strong>{g.name}</strong> · {labels[g.state]}
        <small>有效至 {new Date(g.expires).toLocaleString()}</small>
        {(admin || !isApp) &&
          (g.state === "active" || g.state === "unavailable") && (
            <button
              disabled={busy}
              onClick={() =>
                void act(async () => {
                  await api(
                    (isApp ? "applications/" : "delegations/") + g.id,
                    "DELETE",
                  );
                  return "";
                })
              }
            >
              {isApp ? "撤销此应用" : "撤销我的委托"}
            </button>
          )}
      </p>
    ));
  }
  return (
    <details
      onToggle={(e) => {
        setOpen(e.currentTarget.open);
        if (!e.currentTarget.open) setCredential("");
      }}
    >
      <summary>Open API 与身份委托</summary>
      {open && (
        <>
          <p>
            <a href="/api/v1/openapi.json" target="_blank" rel="noreferrer">
              查看 OpenAPI 接口契约
            </a>
          </p>
          {error && <p role="alert">{error}</p>}
          {data && (
            <>
              {admin && (
                <>
                  <h3>服务应用</h3>
                  {grants(data.applications, true)}
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      const f = new FormData(e.currentTarget);
                      void act(async () => {
                        const a = await api<{ id: string; token: string }>(
                          path + "/applications",
                          "POST",
                          { name: f.get("name"), days: Number(f.get("days")) },
                        );
                        return "应用 ID " + a.id + "\nBearer " + a.token;
                      });
                    }}
                  >
                    <label>
                      应用名称
                      <input name="name" required />
                    </label>
                    <label>
                      有效天数
                      <input
                        name="days"
                        type="number"
                        min="1"
                        max="365"
                        defaultValue="7"
                        required
                      />
                    </label>
                    <button disabled={busy}>签发限定服务凭证</button>
                  </form>
                </>
              )}
              <h3>我的成员委托</h3>
              {grants(data.delegations, false)}
              {data.applications.some((a) => a.state === "active") ? (
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    const f = new FormData(e.currentTarget);
                    void act(async () => {
                      const g = await api<{ id: string; token: string }>(
                        path + "/delegate",
                        "POST",
                        { app: f.get("app"), hours: 1 },
                      );
                      return (
                        "委托 ID " + g.id + "\nX-Alliance-Delegation " + g.token
                      );
                    });
                  }}
                >
                  <label>
                    受托应用
                    <select name="app" required defaultValue="">
                      <option value="">选择获授权的应用</option>
                      {data.applications
                        .filter((a) => a.state === "active")
                        .map((a) => (
                          <option key={a.id} value={a.id}>
                            {a.name}
                          </option>
                        ))}
                    </select>
                  </label>
                  <button disabled={busy}>授予 1 小时服务调用权</button>
                </form>
              ) : (
                <p>当前没有可受托的应用。</p>
              )}
              {credential && (
                <label>
                  仅本次显示
                  <textarea value={credential} readOnly />
                </label>
              )}
            </>
          )}
        </>
      )}
    </details>
  );
}

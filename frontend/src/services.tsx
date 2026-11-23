import { ServiceAccess } from "./service-access";
import { ServiceVersions } from "./service-versions";
import { useEffect, useState } from "react";
import { api, RequestError, type Agent, type Summary } from "./client";
import { serviceURL, type PublishedService as Service } from "./service-entry";
export function Services({
  admin,
  agents,
  projects,
}: {
  admin: boolean;
  agents: Agent[];
  projects: Summary[];
}) {
  const [items, setItems] = useState<Service[]>([]),
    [msg, setMsg] = useState(""),
    [selected, setSelected] = useState<Service | null>(null);
  const [target, setTarget] = useState<Service | null>(null);
  const latestTarget = target && items.find((s) => s.id === target.id);
  const staleTarget = target && (target.version !== latestTarget?.version ||
    target.routingRevision !== latestTarget?.routingRevision);
  async function refresh() {
    const rows = await api<Service[]>("services");
    setItems(rows);
    setSelected((previous) => rows.find((s) => s.id === previous?.id) || null);
  }
  useEffect(() => {
    void refresh().catch((e) => setMsg(e.message));
  }, []);
  return (
    <section>
      {items.map((s) => (
        <button
          className="list"
          key={s.id}
          onClick={() => {
            setSelected(s);
          }}
        >
          {s.name} · v{s.version}
          <small>{s.brief}</small>
        </button>
      ))}
      {!items.length && <p>团队还没有已发布的服务。</p>}
      {selected && (
        <section className="card" key={selected.id}>
          <h3>
            {selected.name} · v{selected.version}
          </h3>
          <p>
            <a href={serviceURL(selected.id)}>打开独立服务对话</a>
          </p>
          {admin && <ServiceVersions id={selected.id} routingRevision={selected.routingRevision} refreshService={refresh} />}
          <ServiceAccess key={selected.id} id={selected.id} admin={admin} />
        </section>
      )}
      {admin && (
        <details>
          <summary>发布可复用服务</summary>
          <label>
            发布目标
            <select
              value={target?.id || ""}
              onChange={(e) =>
                setTarget(items.find((s) => s.id === e.target.value) || null)
              }
            >
              <option value="">新建服务</option>
              {items.map((s) => (
                <option key={s.id} value={s.id}>
                  更新 {s.name} · v{s.version}
                </option>
              ))}
            </select>
          </label>
          {staleTarget && (
            <p role="status">服务默认版本已变化。{latestTarget &&
              <button type="button" onClick={() => setTarget(latestTarget)}>核对 v{latestTarget.version} 后继续编辑</button>}
            </p>
          )}
          <form
            key={target?.id || "new"}
            onSubmit={async (e) => {
              e.preventDefault();
              if (staleTarget) return;
              const form = e.currentTarget,
                f = new FormData(form),
                source = String(f.get("source")),
                [kind, id] = source.split("/");
              try {
                await api("services", "POST", {
                  id: target?.id,
                  expected: target?.version,
                  expectedRoutingRevision: target?.routingRevision,
                  name: f.get("name"),
                  instructions: f.get("brief"),
                  example: f.get("example"),
                  [kind]: id,
                });
                await refresh();
                form.reset();
                setTarget(null);
                setMsg("服务已发布，来源修订已固定。");
              } catch (e) {
                setMsg((e as Error).message);
                if (e instanceof RequestError && e.status === 409) void refresh().catch(() => {});
              }
            }}
          >
            <label>
              服务名称
              <input name="name" defaultValue={target?.name} required />
            </label>
            <label>
              能力来源
              <select name="source" required>
                <option value="">请选择</option>
                {agents
                  .filter((a) => a.publishedReady)
                  .map((a) => (
                    <option key={a.id} value={"agent/" + a.id}>
                      {a.publishedName || a.name} v{a.published}
                    </option>
                  ))}
                {projects
                  .filter(
                    (p) =>
                      p.planVersion > 0 && !["brief", "plan"].includes(p.stage),
                  )
                  .map((p) => (
                    <option key={p.id} value={"project/" + p.id}>
                      协作方案 · {p.title}
                    </option>
                  ))}
              </select>
            </label>
            <label>
              对外服务要求
              <textarea name="brief" defaultValue={target?.brief} required />
            </label>
            <label>
              使用示例（可选）
              <textarea
                maxLength={1000}
                name="example"
                defaultValue={target?.example}
              />
            </label>
            <p>本次发布人承担新服务运行的审核与最终确认职责。</p>
            <button disabled={!!staleTarget}>{target ? "确认发布新版本" : "发布固定版本"}</button>
          </form>
        </details>
      )}
      {msg && <p role="status">{msg}</p>}
    </section>
  );
}

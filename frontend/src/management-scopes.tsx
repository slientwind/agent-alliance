import { RunStateSummary, type RunStates } from "./run-states";
import { useCallback, useEffect, useState } from "react";
import { api, RequestError, type Member } from "./client";

import { DetailLevel, ManagementDetails } from "./management-details";
import { ManagementExport } from "./management-export";

type Scope = {
  id: string;
  kind: "department" | "team" | "enterprise";
  name: string;
};
type Grant = Scope & {
  costs: boolean;
  export?: boolean;
  financialExport?: boolean;
  details?: boolean;
  detailCosts?: boolean;
  expiresAt: string;
  member?: string;
};
type GrantRecord = Grant & {
  exportRevision: number;
  detailRevision: number;
  issuer: string;
  reason: string;
  grantedAt: string;
  endedAt: string | null;
  endReason: string | null;
};
type Catalog = {
  scopes: Scope[];
  grants: GrantRecord[];
  ended?: GrantRecord[];
  assignments: Record<string, string>;
};
type Totals = {
  runs: number;
  completed: number;
  runStates?: RunStates;
  waiting: number;
  reviewed: number;
  adopted: number;
  editions: number;
  checked: number;
  from: string;
  checkedAt: string;
  to: string;
  expiresAt: string;
  costs: null | {
    calls: number;
    tokens: number;
    recordedCny: string;
    remainingEstimateCny: string;
    pending: number;
    unknown: number;
  };
};
const label = (scope: Scope) =>
  ({ team: "团队 · ", department: "部门 · ", enterprise: "企业全域 · " })[
    scope.kind
  ] + scope.name;
const date = (s: string) =>
  new Date(s).toLocaleString("zh-CN", { hour12: false });

export function ManageScopes({ members }: { members: Member[] }) {
  const [open, setOpen] = useState(false);
  return (
    <details className="card" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>管理汇总授权</summary>
      {open && <GrantEditor members={members.filter((m) => m.enabled)} />}
    </details>
  );
}
function GrantEditor({ members }: { members: Member[] }) {
  const [catalog, setCatalog] = useState<Catalog>();
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [grantCosts, setGrantCosts] = useState(false);
  const [selectedScope, setSelectedScope] = useState("");
  const [assignee, setAssignee] = useState(members[0]?.id ?? "");
  const reload = useCallback(
    async () => setCatalog(await api<Catalog>("admin/management-scopes")),
    [],
  );
  useEffect(() => {
    void reload().catch((e) => setError(e.message));
  }, [reload]);
  async function change(action: () => Promise<unknown>, success: string) {
    setError("");
    setMessage("");
    setBusy(true);
    try {
      await action();
      await reload();
      setMessage(success);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      {error && <p role="alert">{error}</p>}
      {message && <p role="status">{message}</p>}
      <button
        type="button"
        disabled={busy}
        onClick={() =>
          void reload()
            .then(() => {
              setError("");
              setMessage("授权清单已刷新");
            })
            .catch((e) => setError(e.message))
        }
      >
        刷新授权清单
      </button>
      {catalog && (
        <>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const form = e.currentTarget;
              const f = new FormData(form);
              const scope = catalog.scopes.find((s) => s.id === f.get("scope"));
              if (!scope) return;
              void change(async () => {
                await api("admin/management-grants", "POST", {
                  kind: scope.kind,
                  scope: scope.id,
                  member: f.get("member"),
                  costs: f.has("costs"),
                  export: f.has("export"),
                  financialExport: f.has("financialExport"),
                  details: f.get("detailLevel") !== "summary",
                  detailCosts: f.get("detailLevel") === "costs",
                  days: Number(f.get("days")),
                  reason: f.get("reason"),
                });
                form.reset();
                setGrantCosts(false);
                setSelectedScope("");
              }, "已授予汇总查看权");
            }}
          >
            <label>
              授权成员
              <select name="member" aria-label="授权成员">
                {members.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              统计范围
              <select
                name="scope"
                aria-label="统计范围"
                value={selectedScope}
                required
                onChange={(e) => setSelectedScope(e.target.value)}
              >
                <option value="" disabled>
                  请选择统计范围
                </option>
                {catalog.scopes.map((s) => (
                  <option key={s.id} value={s.id}>
                    {label(s)}
                  </option>
                ))}
              </select>
            </label>
            {selectedScope === "enterprise" && (
              <p>
                此次授权覆盖本企业全部团队、部门、应用及没有部门归属的历史活动。
              </p>
            )}
            <label>
              <input
                type="checkbox"
                name="costs"
                checked={grantCosts}
                onChange={(e) => setGrantCosts(e.target.checked)}
              />{" "}
              同时开放该范围的用量与费用汇总
            </label>
            <label>
              <input type="checkbox" name="export" /> 允许导出已授权的汇总字段
            </label>
            {grantCosts && (
              <label>
                <input type="checkbox" name="financialExport" />{" "}
                允许导出逐笔调用费用与入账记录
              </label>
            )}
            <DetailLevel />
            <label>
              有效天数
              <input
                name="days"
                type="number"
                min="1"
                max="365"
                defaultValue="30"
                required
              />
            </label>
            <label>
              授权原因
              <input name="reason" required maxLength={500} />
            </label>
            <button
              disabled={busy || !members.length || !catalog.scopes.length}
            >
              授予汇总查看权
            </button>
          </form>
          <h3>有效授权</h3>
          {catalog.grants.length === 0 && <p>暂无有效授权</p>}
          {catalog.grants.map((g) => (
            <details key={g.id}>
              <summary>
                {g.member} · {label(g)}
              </summary>
              <p>
                {g.costs ? "业务、用量与费用汇总" : "业务汇总"} ·{" "}
                {g.export ? "汇总可导出" : "汇总仅查看"}
                {g.financialExport && " · 逐笔费用可导出"} · 有效至{" "}
                {date(g.expiresAt)}
              </p>
              <small>
                {g.issuer} 于 {date(g.grantedAt)} 授权 · {g.reason}
              </small>
              <form
                key={g.detailRevision}
                onSubmit={(e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  const level = f.get("detailLevel");
                  void change(
                    () =>
                      api(
                        "admin/management-grants/" + g.id + "/details",
                        "PATCH",
                        {
                          details: level !== "summary",
                          detailCosts: level === "costs",
                          expected: g.detailRevision,
                          reason: f.get("reason"),
                        },
                      ),
                    "明细授权已更新",
                  );
                }}
              >
                <DetailLevel
                  value={
                    g.detailCosts ? "costs" : g.details ? "tasks" : "summary"
                  }
                />
                <label>
                  明细权限调整原因
                  <input name="reason" maxLength={400} required />
                </label>
                <button disabled={busy}>保存明细权限</button>
              </form>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget);
                  void change(
                    () =>
                      api(
                        "admin/management-grants/" + g.id + "/export",
                        "PATCH",
                        {
                          enabled: !g.export,
                          expected: g.exportRevision,
                          reason: f.get("reason"),
                        },
                      ),
                    g.export ? "已关闭导出，已有下载已失效" : "已开放导出",
                  );
                }}
              >
                <label>
                  导出权限调整原因
                  <input name="reason" maxLength={400} required />
                </label>
                <button disabled={busy}>
                  {g.export ? "关闭导出并使已有下载失效" : "开放汇总导出"}
                </button>
              </form>
              {g.costs && (
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    const reason = new FormData(e.currentTarget).get("reason");
                    void change(
                      () =>
                        api(
                          "admin/management-grants/" + g.id + "/export",
                          "PATCH",
                          {
                            financial: true,
                            enabled: !g.financialExport,
                            expected: g.exportRevision,
                            reason,
                          },
                        ),
                      g.financialExport
                        ? "已关闭逐笔费用导出"
                        : "已开放逐笔费用导出",
                    );
                  }}
                >
                  <label>
                    逐笔费用授权调整原因
                    <input name="reason" required maxLength={400} />
                  </label>
                  <button disabled={busy}>
                    {g.financialExport
                      ? "关闭逐笔费用导出"
                      : "开放逐笔费用导出"}
                  </button>
                </form>
              )}
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const reason = new FormData(e.currentTarget).get("reason");
                  void change(
                    () =>
                      api("admin/management-grants/" + g.id, "DELETE", {
                        reason,
                      }),
                    "已撤销汇总查看权",
                  );
                }}
              >
                <label>
                  撤销原因
                  <input name="reason" required maxLength={500} />
                </label>
                <button disabled={busy}>撤销此授权</button>
              </form>
            </details>
          ))}
          {(catalog.ended?.length ?? 0) > 0 && (
            <details>
              <summary>已结束的授权（最近 50 条）</summary>
              {catalog.ended?.map((g) => (
                <p key={g.id}>
                  {g.member} · {label(g)} · {g.endReason}
                  <small>
                    {g.endedAt && date(g.endedAt)} · 原授权人 {g.issuer} ·{" "}
                    {g.reason}
                  </small>
                </p>
              ))}
            </details>
          )}
          <details>
            <summary>成员部门归属</summary>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const f = new FormData(e.currentTarget);
                void change(
                  () =>
                    api("admin/members/" + assignee + "/department", "PUT", {
                      department: f.get("department"),
                      previous: catalog.assignments[assignee] ?? "",
                      reason: f.get("reason"),
                    }),
                  "部门归属已更新",
                );
              }}
            >
              <label>
                调整归属的成员
                <select
                  value={assignee}
                  onChange={(e) => setAssignee(e.target.value)}
                >
                  {members.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                归属部门
                <select
                  name="department"
                  key={assignee + (catalog.assignments[assignee] ?? "")}
                  defaultValue={catalog.assignments[assignee] ?? ""}
                >
                  <option value="">未分配</option>
                  {catalog.scopes
                    .filter((s) => s.kind === "department")
                    .map((s) => (
                      <option key={s.id} value={s.id}>
                        {s.name}
                      </option>
                    ))}
                </select>
              </label>
              <small>
                调离后结束本人对原部门的汇总授权。历史项目和调用保留原归属。
              </small>
              <label>
                调整原因
                <input name="reason" required />
              </label>
              <button disabled={busy || !assignee}>保存部门归属</button>
            </form>
          </details>
        </>
      )}
    </>
  );
}

export function MyManagementScopes() {
  const [scopes, setScopes] = useState<Grant[]>([]);
  const [error, setError] = useState("");
  const [scope, setScope] = useState("");
  const [days, setDays] = useState("30");
  const [open, setOpen] = useState(false);
  useEffect(() => {
    let current = true,
      request = 0;
    async function load() {
      const id = ++request;
      try {
        const data = await api<Grant[]>("management-scopes");
        if (!current || id !== request) return;
        setError("");
        setScopes(data);
        setScope((old) => old || data[0]?.id || "");
      } catch (e) {
        if (current && id === request) {
          setScopes([]);
          setError(
            e instanceof RequestError && e.status === 404
              ? ""
              : (e as Error).message,
          );
        }
      }
    }
    void load();
    window.addEventListener("focus", load);
    const timer = window.setInterval(load, 30000);
    return () => {
      current = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", load);
    };
  }, []);
  const grant = scopes.find((s) => s.id === scope);
  if (error) return <p role="alert">管理范围读取失败 · {error}</p>;
  if (!scopes.length && !(open && scope)) return null;
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>获授权的管理汇总</summary>
      {open && (
        <>
          <div className="usage-filters">
            <label>
              已授权范围
              <select
                aria-label="已授权范围"
                value={grant ? scope : ""}
                onChange={(e) => setScope(e.target.value)}
              >
                {!grant && (
                  <option value="" disabled>
                    请选择有效范围
                  </option>
                )}
                {scopes.map((s) => (
                  <option key={s.id} value={s.id}>
                    {label(s)}
                  </option>
                ))}
              </select>
            </label>
            <label>
              汇总周期
              <select value={days} onChange={(e) => setDays(e.target.value)}>
                {[7, 30, 90].map((d) => (
                  <option key={d} value={d}>
                    近 {d} 天
                  </option>
                ))}
              </select>
            </label>
          </div>
          {!grant && (
            <p role="status">
              先前所选范围的访问已结束。
              {scopes.length
                ? "请选择其他已授权范围。"
                : "当前没有其他已授权范围。"}
            </p>
          )}
          {grant && (
            <ScopeReport
              key={grant.id + "/" + days}
              grant={grant}
              days={days}
            />
          )}
        </>
      )}
    </details>
  );
}
function ScopeReport({ grant, days }: { grant: Grant; days: string }) {
  const [report, setReport] = useState<Totals>();
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    let active = true,
      generation = 0;
    let expiry: number | undefined;
    function expire() {
      generation++;
      setReport(undefined);
      setError("授权已到期");
    }
    async function read() {
      const id = ++generation;
      const started = performance.now();
      if (document.hidden) {
        setReport(undefined);
        setError("");
        return;
      }
      try {
        const data = await api<Totals>(
          "management-scopes/" + grant.id + "/summary?days=" + days,
        );
        if (active && id === generation) {
          const remaining =
            Date.parse(data.expiresAt) -
            Date.parse(data.checkedAt ?? data.to) -
            (performance.now() - started);
          window.clearTimeout(expiry);
          if (remaining <= 0) {
            expire();
            return;
          }
          if (remaining <= 2147483647)
            expiry = window.setTimeout(expire, remaining);
          setReport(data);
          setError("");
        }
      } catch (e) {
        if (active && id === generation) {
          setReport(undefined);
          setError((e as Error).message);
        }
      }
    }
    void read();
    const timer = window.setInterval(read, 30000);
    document.addEventListener("visibilitychange", read);
    window.addEventListener("focus", read);
    return () => {
      active = false;
      window.clearInterval(timer);
      window.clearTimeout(expiry);
      document.removeEventListener("visibilitychange", read);
      window.removeEventListener("focus", read);
    };
  }, [grant.id, grant.expiresAt, days, refresh]);
  return (
    <section aria-label="范围汇总结果">
      <button
        onClick={() => {
          setReport(undefined);
          setError("");
          setRefresh((n) => n + 1);
        }}
      >
        刷新范围汇总
      </button>
      {error ? (
        <p role="alert">{error}</p>
      ) : !report ? (
        <p role="status">正在读取汇总…</p>
      ) : (
        <>
          <p>
            {date(report.from)} 至 {date(report.to)}
          </p>
          <dl className="usage-outcomes">
            {Object.entries({
              发起运行: report.runs,
              执行完成: report.completed,
              "阻塞、待核对或暂停": report.waiting,
              能力样例检查通过: report.checked,
              成果版本审核通过: report.reviewed,
              简单回答已采纳: report.adopted,
              正式交付批次: report.editions,
            }).map(([k, v]) => (
              <div key={k}>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </div>
            ))}
          </dl>
          {report.runStates && <RunStateSummary groups={[report.runStates]} />}
          {report.costs && (
            <p>
              调用 {report.costs.calls} 次 · 已知 {report.costs.tokens} token ·
              已记账 ¥{report.costs.recordedCny} · 剩余预估 ¥
              {report.costs.remainingEstimateCny} · 待结算{" "}
              {report.costs.pending} 笔 · 未知费用 {report.costs.unknown} 笔
            </p>
          )}
          {grant.details ? (
            <ManagementDetails
              key={String(grant.detailCosts)}
              id={grant.id}
              period={report}
              costs={!!grant.detailCosts}
            />
          ) : (
            <small>当前授权可查看汇总。按发起人查看任务需要明细权限。</small>
          )}
          {grant.financialExport && report.costs && (
            <ManagementExport
              id={grant.id}
              scope={label(grant)}
              period={report}
              costs
              financial
            />
          )}
          {grant.export && (
            <ManagementExport
              id={grant.id}
              scope={label(grant)}
              period={report}
              costs={!!report.costs}
            />
          )}
          <small>
            {grant.kind === "enterprise"
              ? "按本企业实际发生的事件统计，同一运行在企业内只计一次，包含未记录部门的活动。"
              : grant.kind === "team"
                ? "运行与交付按项目归属团队，检查按能力所属团队，费用按实际承担团队。"
                : "运行与交付按建项时发起人的部门，检查和费用按调用发起时的部门。仅包含已记录部门的活动。"}
          </small>
        </>
      )}
    </section>
  );
}

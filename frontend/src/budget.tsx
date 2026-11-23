import { BudgetRequests } from "./budget-requests";
import { useEffect, useState } from "react";
import { api, type Member, type BudgetSettings } from "./client";
type Budget = {
  limit: string | null;
  warning: boolean;
  exhausted: boolean;
  revision: number;
  warningPercent: number;
  settledAmount: string;
  reserved: string;
  unknown: number;
  resources: (BudgetSettings & {
    id: string;
    kind: "model" | "tool" | "external";
    name: string;
    callLimit: number | null;
    used: string;
    recorded: string;
    reserved: string;
    unknown: number;
    calls: number;
    warning: boolean;
    exhausted: boolean;
  })[];
  projects: (BudgetSettings & {
    id: string;
    used: string;
    title: string | null;
    warning: boolean;
    exhausted: boolean;
  })[];
  items: {
    id: string;
    kind: string;
    resource: string | null;
    sourceRevision: number | null;
    sourceName: string | null;
    estimate: string | null;
    amount: string;
    settled: boolean;
  }[];
};
type Team = {
  id: string;
  name: string;
  default: boolean;
  budgetPending: number;
  budgetOwner: string | null;
};
export function BudgetPanel({
  user,
  members,
}: {
  user: Member;
  members?: Member[];
}) {
  const [teams, setTeams] = useState<Team[]>([]),
    [selected, select] = useState(""),
    [message, setMessage] = useState("");
  async function refreshTeams() {
    const latest = await api<Team[]>("teams");
    setTeams(latest);
    const eligible = latest.filter((t) => members || t.budgetOwner === user.id);
    select(
      (previous) =>
        eligible.find((t) => t.id === previous)?.id ||
        eligible.find((t) => t.budgetPending > 0)?.id ||
        eligible.find((t) => t.default && t.budgetOwner === user.id)?.id ||
        eligible.find((t) => t.budgetOwner === user.id)?.id ||
        eligible.find((t) => t.default)?.id ||
        eligible[0]?.id ||
        "",
    );
  }
  useEffect(() => {
    void refreshTeams().catch((e) => setMessage(e.message));
  }, []);
  const visible = teams.filter((t) => members || t.budgetOwner === user.id);
  const team =
    visible.find((t) => t.id === selected) ||
    visible.find((t) => t.budgetPending > 0) ||
    visible[0];
  const pending = visible.reduce((sum, t) => sum + t.budgetPending, 0);
  if (!team) return message ? <p role="status">{message}</p> : null;
  return (
    <details className="card">
      <summary>预算管理{pending > 0 && " · " + pending + " 项待审批"}</summary>
      <label>
        管理团队
        <select
          value={team.id}
          onChange={(e) => {
            select(e.target.value);
            setMessage("");
          }}
        >
          {visible.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
              {t.budgetPending > 0 && " · " + t.budgetPending + " 项待审批"}
            </option>
          ))}
        </select>
      </label>
      {members && (
        <details>
          <summary>预算负责人</summary>
          <form
            key={team.id + (team.budgetOwner || "")}
            onSubmit={async (e) => {
              e.preventDefault();
              const f = new FormData(e.currentTarget);
              try {
                await api("teams/" + team.id + "/budget-owner", "PUT", {
                  previous: team.budgetOwner || "",
                  member: f.get("member"),
                  reason: f.get("reason"),
                });
                await refreshTeams();
                setMessage("已更新预算负责人。");
              } catch (e) {
                setMessage((e as Error).message);
              }
            }}
          >
            <label>
              负责成员
              <select name="member" defaultValue={team.budgetOwner || ""}>
                <option value="">未指定</option>
                {members
                  .filter((m) => m.enabled)
                  .map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
              </select>
            </label>
            <label>
              变更原因
              <input required name="reason" />
            </label>
            <button>保存预算负责人</button>
          </form>
        </details>
      )}
      {team.budgetOwner === user.id && (
        <TeamBudget key={team.id} team={team.id} refreshTeams={refreshTeams} />
      )}
      {message && <p role="status">{message}</p>}
    </details>
  );
}

export function BudgetEditor({
  path,
  settings,
  refresh,
}: {
  path: string;
  settings: BudgetSettings & { callLimit?: number | null };
  refresh: () => Promise<void>;
}) {
  const [message, setMessage] = useState("");
  return (
    <details>
      <summary>调整预算</summary>
      <form
        key={settings.revision}
        onSubmit={async (e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          try {
            await api(path, "PUT", {
              limit: f.get("limit") || null,
              ...(settings.callLimit !== undefined
                ? { callLimit: f.get("calls") ? Number(f.get("calls")) : null }
                : {}),
              expected: settings.revision,
              warningPercent: Number(f.get("threshold")),
              reason: f.get("reason"),
            });
            setMessage("已更新预算。");
          } catch (e) {
            setMessage((e as Error).message);
          }
          try {
            await refresh();
          } catch (e) {
            setMessage((e as Error).message);
          }
        }}
      >
        <label>
          金额（CNY，留空不设金额阈值）
          <input
            name="limit"
            defaultValue={settings.limit || ""}
            inputMode="decimal"
          />
        </label>
        {settings.callLimit !== undefined && (
          <label>
            累计调用次数上限（留空不设上限）
            <input
              type="number"
              min="0"
              max="2147483647"
              step="1"
              name="calls"
              defaultValue={settings.callLimit ?? ""}
            />
          </label>
        )}
        <label>
          提醒百分比
          <input
            type="number"
            required
            name="threshold"
            min="1"
            max="100"
            defaultValue={settings.warningPercent}
          />
        </label>
        <label>
          调整原因
          <input name="reason" required />
        </label>
        <button>确认预算调整</button>
      </form>
      {message && <p role="status">{message}</p>}
    </details>
  );
}

function TeamBudget({
  team,
  refreshTeams,
}: {
  team: string;
  refreshTeams: () => Promise<void>;
}) {
  const [data, setData] = useState<Budget | null>(null),
    [message, setMessage] = useState("");
  const path = "teams/" + team + "/budget";
  async function refresh() {
    try {
      setData(await api<Budget>(path));
    } catch (e) {
      setData(null);
      throw e;
    }
  }
  useEffect(() => {
    void refresh().catch((e) => setMessage(e.message));
  }, [path]);
  const resourceNames = new Map(data?.resources.map((r) => [r.id, r.name]));
  return (
    <section>
      <h2>团队预算</h2>
      {data && (
        <>
          <p>
            预算 {data.limit ?? "未设金额阈值"} · 已记账 ¥{data.settledAmount} ·
            预占 ¥{data.reserved} · {data.unknown} 笔未知费用待核对
          </p>
          {data.warning && (
            <p role="status">
              <strong>
                {data.exhausted
                  ? "本团队已达到预算停止阈值。"
                  : "本团队已达到预算提醒阈值。"}
              </strong>
            </p>
          )}
          <BudgetRequests
            path={"teams/" + team}
            refresh={async () => {
              await Promise.all([refresh(), refreshTeams()]);
            }}
          />
          <BudgetEditor path={path} settings={data} refresh={refresh} />
          <details>
            <summary>资源额度 · {data.resources.length}</summary>
            {data.resources.map((r) => (
              <section key={r.id}>
                <strong>{r.name}</strong> ·{" "}
                {
                  { model: "模型", tool: "工具", external: "外部 Agent" }[
                    r.kind
                  ]
                }
                <p>
                  金额上限 {r.limit === null ? "未设置" : "¥" + r.limit} ·
                  已记账 ¥{r.recorded} · 预占 ¥{r.reserved} · {r.unknown}{" "}
                  笔费用待核对
                </p>
                <p>
                  累计调用 {r.calls} 次 · 次数上限 {r.callLimit ?? "未设置"}
                  {r.exhausted
                    ? " · 已达停止阈值"
                    : r.warning
                      ? " · 已达提醒线"
                      : ""}
                </p>
                <BudgetEditor
                  path={"teams/" + team + "/resource-budgets/" + r.id}
                  settings={r}
                  refresh={refresh}
                />
              </section>
            ))}
          </details>
          <details>
            <summary>项目额度 · {data.projects.length}</summary>
            {data.projects.map((p) => (
              <section key={p.id}>
                <strong>{p.title || "项目 " + p.id}</strong>
                <p>
                  额度 {p.limit ?? "未设金额阈值"} · 已记账与预占 ¥{p.used}
                  {p.exhausted
                    ? " · 已达停止阈值"
                    : p.warning
                      ? " · 已达提醒阈值"
                      : ""}
                </p>
                <BudgetEditor
                  path={"projects/" + p.id + "/budget"}
                  settings={p}
                  refresh={refresh}
                />
              </section>
            ))}
          </details>
          {data.items.length > 0 && (
            <details>
              <summary>最近调用与账单核对</summary>
              {data.items.map((x) => (
                <div key={x.id}>
                  <strong>
                    {resourceNames.get(x.resource || "") || "历史资源未关联"}
                  </strong>{" "}
                  ·{" "}
                  {x.sourceRevision !== null && (
                    <small>
                      {x.sourceName} v{x.sourceRevision}
                    </small>
                  )}
                  {x.kind} · 预计 {x.estimate ?? "未知"} · 已记账 {x.amount} ·{" "}
                  {x.settled ? "已结清" : "待核对"}
                  <small>{x.id}</small>
                </div>
              ))}
              <form
                onSubmit={async (e) => {
                  e.preventDefault();
                  const f = new FormData(e.currentTarget),
                    form = e.currentTarget;
                  try {
                    await api("admin/bills", "POST", {
                      line: f.get("line"),
                      spend: f.get("spend"),
                      amount: f.get("amount"),
                      final: f.get("final") === "on",
                    });
                    form.reset();
                    await refresh();
                    setMessage("已登记供应商账单行。");
                  } catch (e) {
                    setMessage((e as Error).message);
                  }
                }}
              >
                <label>
                  对应调用
                  <select name="spend">
                    {data.items
                      .filter((x) => !x.settled)
                      .map((x) => (
                        <option key={x.id}>{x.id}</option>
                      ))}
                  </select>
                </label>
                <label>
                  供应商账单行标识
                  <input name="line" required />
                </label>
                <label>
                  本行新增金额
                  <input name="amount" inputMode="decimal" required />
                </label>
                <label>
                  <input name="final" type="checkbox" />
                  最终结算，释放剩余预占
                </label>
                <button>记账</button>
              </form>
            </details>
          )}
        </>
      )}
      {message && <p role="status">{message}</p>}
    </section>
  );
}

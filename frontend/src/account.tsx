import { startSSO, type SSOMode } from "./enterprise-login";
import { RunStateSummary, type RunStates } from "./run-states";
import { useEffect, useState, type Dispatch, type SetStateAction } from "react";
import { MemberHistory } from "./member-history";
import { ManagementExport } from "./management-export";
import { api, type Member } from "./client";
export function Account({
  initialized,
  sso,
  ready,
}: {
  initialized: boolean;
  sso?: SSOMode;
  ready: () => Promise<void>;
}) {
  const [register, setRegister] = useState(false),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const [emergency, setEmergency] = useState(false);
  const required = sso === "required";
  const localForm = !required || register || emergency || !initialized;
  const signup = !initialized || register;
  return (
    <main className="gate">
      <form
        className="card"
        onSubmit={async (e) => {
          e.preventDefault();
          const data = Object.fromEntries(new FormData(e.currentTarget));
          setBusy(true);
          setError("");
          try {
            const submitter = (e.nativeEvent as SubmitEvent)
              .submitter as HTMLButtonElement | null;
            if (
              initialized &&
              sso &&
              (submitter?.value === "oidc" || (required && !emergency))
            ) {
              await startSSO(
                register ? "oidc/enroll" : "oidc/login",
                register ? data : {},
              );
              return;
            }
            await api(
              initialized ? (register ? "register" : "login") : "initialize",
              "POST",
              data,
            );
            await ready();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
          }
        }}
      >
        <h1>
          {!initialized
            ? "初始化企业工作空间"
            : register
              ? "凭邀请加入企业"
              : "登录工作空间"}
        </h1>
        {!initialized && (
          <label>
            企业名称
            <input name="Enterprise" required autoComplete="organization" />
          </label>
        )}
        {signup && (
          <>
            <label>
              {initialized ? "邀请凭证" : "安装凭证"}
              <input name="Token" type="password" required autoComplete="off" />
            </label>
            <label>
              姓名
              <input name="Name" required autoComplete="name" />
            </label>
          </>
        )}
        {localForm && (
          <>
            <label>
              邮箱
              <input
                name="Email"
                required
                type="email"
                autoComplete="username"
              />
            </label>
            <label>
              密码
              <input
                name="Password"
                type="password"
                required
                minLength={signup ? 12 : 1}
                autoComplete={signup ? "new-password" : "current-password"}
              />
            </label>
            {required && emergency && !register && (
              <label>
                应急登录原因
                <input name="Reason" required maxLength={200} />
              </label>
            )}
          </>
        )}
        {error && <p role="alert">{error}</p>}
        <button className="primary" disabled={busy}>
          {!initialized
            ? "创建首位管理员"
            : register
              ? required
                ? "验证企业身份并加入"
                : "注册并进入"
              : required && !emergency
                ? "企业单点登录"
                : emergency
                  ? "应急登录"
                  : "登录"}
        </button>
        {initialized && sso === "optional" && (
          <button
            type="submit"
            value="oidc"
            formNoValidate={!register}
            disabled={busy}
          >
            {register ? "验证企业身份并加入" : "企业单点登录"}
          </button>
        )}
        {initialized && required && !register && (
          <button
            type="button"
            onClick={() => {
              setEmergency(!emergency);
              setError("");
            }}
          >
            {emergency ? "返回企业登录" : "指定管理员应急登录"}
          </button>
        )}
        {initialized && (
          <button
            type="button"
            onClick={() => {
              setRegister(!register);
              setEmergency(false);
              setError("");
            }}
          >
            {register ? "返回登录" : "我有企业邀请"}
          </button>
        )}
      </form>
    </main>
  );
}
type TeamOption = { id: string; name: string };
type MemberPanelProps = { members: Member[]; reload: () => Promise<void> };
type TeamFilter = {
  team: string;
  teams: TeamOption[];
  setTeam: (id: string) => void;
};

export function AccountManagement({ members, reload }: MemberPanelProps) {
  const [teams, setTeams] = useState<TeamOption[]>([]);
  const [team, setTeam] = useState("");
  const [actor, setActor] = useState("");
  const [profile, setProfile] = useState<Member | null>(null);
  useEffect(() => {
    let active = true;
    api<TeamOption[]>("teams")
      .then((data) => {
        if (active) setTeams(data);
      })
      .catch(() => {
        if (active) setTeams([]);
      });
    return () => {
      active = false;
    };
  }, [members]);
  function focus(id: string) {
    requestAnimationFrame(() => document.getElementById(id)?.focus());
  }
  const filter = { teams, team, setTeam };
  const activeMember = members.find((m) => m.id === actor);
  return (
    <>
      <Monitor
        {...filter}
        member={actor}
        setMember={setActor}
        showProfile={
          activeMember
            ? () => {
                setProfile(activeMember);
                focus("member-profile");
              }
            : undefined
        }
      />
      <Members
        {...filter}
        members={members}
        reload={reload}
        selected={profile}
        setSelected={setProfile}
        showUsage={(id) => {
          setActor(id);
          focus("usage-monitor");
        }}
      />
    </>
  );
}

function TeamSelect({
  team,
  teams,
  setTeam,
  label,
}: { label: string } & TeamFilter) {
  return (
    <label>
      {label}
      <select value={team} onChange={(e) => setTeam(e.target.value)}>
        <option value="">全部团队</option>
        {teams.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </select>
    </label>
  );
}

function Members({
  members,
  reload,
  selected,
  setSelected,
  showUsage,
  ...filter
}: MemberPanelProps &
  TeamFilter & {
    selected: Member | null;
    setSelected: Dispatch<SetStateAction<Member | null>>;
    showUsage: (id: string) => void;
  }) {
  const [query, setQuery] = useState(""),
    [role, setRole] = useState(""),
    [status, setStatus] = useState("");
  const memberStatus = (m: Member) =>
    m.departed ? "departed" : m.enabled ? "enabled" : "disabled";
  const visible = members.filter(
    (m) =>
      (!role || m.role === role) &&
      (!status || memberStatus(m) === status) &&
      (!filter.team || m.teams?.some((t) => t.id === filter.team)) &&
      (m.name + " " + m.email)
        .toLocaleLowerCase()
        .includes(query.trim().toLocaleLowerCase()),
  );
  const current = members.find((m) => m.id === selected?.id);
  const [invite, setInvite] = useState(""),
    [err, setErr] = useState("");
  return (
    <section aria-label="用户管理">
      <h2>用户管理</h2>
      <button
        onClick={async () => {
          try {
            const d = await api<{ token: string }>("admin/invitations", "POST");
            setInvite(d.token);
          } catch (e) {
            setErr((e as Error).message);
          }
        }}
      >
        创建 48 小时邀请
      </button>
      {invite && (
        <label>
          仅向受邀人提供
          <input value={invite} readOnly onFocus={(e) => e.target.select()} />
        </label>
      )}
      <div className="usage-filters">
        <label>
          查找成员
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="姓名或邮箱"
          />
        </label>
        {filter.teams.length > 1 && (
          <TeamSelect {...filter} label="成员所属团队" />
        )}
        <label>
          账号角色
          <select value={role} onChange={(e) => setRole(e.target.value)}>
            <option value="">全部角色</option>
            <option value="admin">管理员</option>
            <option value="member">普通用户</option>
          </select>
        </label>
        <label>
          账号状态
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">全部状态</option>
            <option value="enabled">启用</option>
            <option value="disabled">暂时停用</option>
            <option value="departed">已离职</option>
          </select>
        </label>
      </div>
      {!visible.length && <p>没有符合条件的成员</p>}
      {visible.map((m) => (
        <button
          className="list"
          aria-current={selected?.id === m.id ? "page" : undefined}
          key={m.id}
          onClick={() => {
            setSelected(m);
            requestAnimationFrame(() =>
              document.getElementById("member-profile")?.focus(),
            );
            setErr("");
          }}
        >
          {m.name} · {m.email}
          <small>
            {m.role === "admin" ? "管理员" : "普通用户"} ·{" "}
            {m.departed ? "已离职" : m.enabled ? "启用" : "暂时停用"}
          </small>
        </button>
      ))}
      {selected && current && (
        <section
          key={selected.id}
          aria-label="成员资料"
          id="member-profile"
          tabIndex={-1}
          className="card"
        >
          <h3>{current.name}</h3>
          <p>
            {current.email} · {current.role === "admin" ? "管理员" : "普通用户"}{" "}
            ·{" "}
            {current.departed
              ? "已离职"
              : current.enabled
                ? "启用"
                : "暂时停用"}
          </p>
          <p>
            {current.registration === "initial"
              ? "企业初始化"
              : current.registration === "invitation"
                ? "邀请注册"
                : "注册方式未记录"}
            {current.createdAt &&
              " · " + new Date(current.createdAt).toLocaleDateString("zh-CN")}
          </p>
          <p>
            部门 · {current.department ?? "未分配"}
            <br />
            团队 · {current.teams?.map((t) => t.name).join("、") || "未分配"}
          </p>
          <button onClick={() => showUsage(selected.id)}>查看此成员用量</button>
          <MemberHistory id={selected.id} />
          {selected.accountRevision !== current.accountRevision ? (
            <div role="status">
              <p>账号状态已变化，请核对上方最新资料后再修改。</p>
              <button onClick={() => setSelected(current)}>
                按最新资料修改
              </button>
            </div>
          ) : (
            <MemberChange
              key={selected.id + "/" + selected.accountRevision}
              selected={selected}
              reload={reload}
              close={() =>
                setSelected((open) => (open?.id === selected.id ? null : open))
              }
            />
          )}
        </section>
      )}
      {err && <p role="alert">{err}</p>}
    </section>
  );
}

function MemberChange({
  selected,
  reload,
  close,
}: {
  selected: Member;
  reload: () => Promise<void>;
  close: () => void;
}) {
  const [employment, setEmployment] = useState(
    selected.departed ? "return" : "",
  );
  const [role, setRole] = useState(selected.role);
  const [enabled, setEnabled] = useState(selected.enabled);
  const [revision, setRevision] = useState(0);
  const [duties, setDuties] = useState<{
    finalProjects: number;
    reviewVersions: number;
    delegations: number;
  }>();
  const [dutiesError, setDutiesError] = useState("");
  useEffect(() => {
    let active = true;
    setDuties(undefined);
    setDutiesError("");
    api<NonNullable<typeof duties>>(
      "admin/members/" + selected.id + "/responsibilities",
    )
      .then((data) => {
        if (active) setDuties(data);
      })
      .catch((e) => {
        if (active) setDutiesError(e.message);
      });
    return () => {
      active = false;
    };
  }, [selected.id, revision]);
  const nextRole = employment ? "member" : role;
  const nextEnabled = employment ? employment === "return" : enabled;
  const roleLabel = (value: string) =>
    value === "admin" ? "管理员" : "普通用户";
  const statusLabel = (value: boolean, departed: boolean) =>
    departed ? "已离职" : value ? "启用" : "暂时停用";
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  return (
    <form
      className="card"
      onSubmit={async (e) => {
        e.preventDefault();
        const f = new FormData(e.currentTarget);
        setBusy(true);
        setErr("");
        try {
          await api("admin/members/" + selected.id, "PATCH", {
            expected: selected.accountRevision,
            role: nextRole,
            enabled: nextEnabled,
            employment,
            reason: f.get("reason"),
            password: f.get("password"),
          });
          await reload();
          close();
        } catch (e) {
          setErr((e as Error).message);
          await reload().catch((e) => setErr(e.message));
        } finally {
          setBusy(false);
        }
      }}
    >
      <h3>修改 {selected.name}</h3>
      <label>
        变更类型
        <select
          value={employment}
          onChange={(e) => setEmployment(e.target.value)}
        >
          {selected.departed ? (
            <option value="return">办理重新入职</option>
          ) : (
            <>
              <option value="">角色与登录状态</option>
              <option value="depart">办理正式离职</option>
            </>
          )}
        </select>
      </label>
      {employment ? (
        <p role="status">
          {employment === "depart"
            ? "将注销会话，撤销团队和项目成员资格、委托与飞书绑定。个人偏好进入隔离回收。请另行交接业务职责。"
            : "以普通用户身份恢复登录。团队和项目须重新授权，有效偏好可由本人验证后逐项恢复。"}
        </p>
      ) : (
        <>
          <select
            aria-label="成员角色"
            name="role"
            value={role}
            onChange={(e) => setRole(e.target.value as Member["role"])}
          >
            <option value="member">普通用户</option>
            <option value="admin">管理员</option>
          </select>
          <label>
            <input
              type="checkbox"
              name="enabled"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            允许登录
          </label>
        </>
      )}
      <section aria-label="变更影响">
        <p>
          {roleLabel(selected.role)}（
          {statusLabel(selected.enabled, !!selected.departed)}） →{" "}
          <strong>
            {roleLabel(nextRole)}（
            {statusLabel(nextEnabled, employment === "depart")}）
          </strong>
        </p>
        {role !== selected.role && !employment && (
          <p>
            {role === "admin"
              ? "可以配置企业模型、查看管理大盘和管理成员。业务资料和审核仍按项目授权。"
              : "将收回企业管理入口。单独取得的项目职责继续有效。"}
          </p>
        )}
        {!nextEnabled && (
          <p>现有登录和应用委托将撤销。重新启用后须重新登录并签发新委托。</p>
        )}
        {duties && (
          <p>
            待最终确认项目 {duties.finalProjects} 个 · 待审核成果版本{" "}
            {duties.reviewVersions} 个 · 未到期且未撤销的成员委托{" "}
            {duties.delegations} 项。项目确认权须由接任者明确接受。
          </p>
        )}
        {!duties && !dutiesError && <p role="status">正在读取待交接事项…</p>}
        {dutiesError && (
          <>
            <p role="alert">{dutiesError}</p>
            <button type="button" onClick={() => setRevision((n) => n + 1)}>
              重读交接事项
            </button>
          </>
        )}
      </section>
      <label>
        变更原因
        <input name="reason" required />
      </label>
      <label>
        管理员当前密码
        <input type="password" name="password" required />
      </label>
      <button disabled={busy || !duties}>确认变更</button>
      {err && <p role="alert">{err}</p>}
    </form>
  );
}

type Usage = {
  runStates?: RunStates;
  id: string;
  name: string;
  kind: string;
  runs: number;
  completed: number;
  waiting: number;
  attempts: number;
  tokens: number;
  unknown: number;
  recordedCny: string;
  passedAgentChecks: number;
  reviewedVersions: number;
  adoptedAnswers: number;
  deliveredEditions: number;
  unsettledCalls: number;
  remainingEstimateCny: string;
};
const outcomeMetrics = [
  ["completed", "执行完成"],
  ["passedAgentChecks", "能力样例检查通过"],
  ["reviewedVersions", "成果版本审核通过"],
  ["adoptedAnswers", "简单回答已采纳"],
  ["deliveredEditions", "正式交付批次"],
] as const;

// Ledger amounts have six decimal places. Keep that precision across members.
function totalCny(
  rows: Usage[],
  field: "recordedCny" | "remainingEstimateCny",
) {
  let micros = 0n;
  for (const row of rows) {
    const [whole, fraction = ""] = row[field].split(".");
    micros += BigInt(whole + fraction.padEnd(6, "0"));
  }
  const amount = micros.toString().padStart(7, "0");
  const fraction = amount.slice(-6).replace(/0+$/, "").padEnd(2, "0");
  const whole = BigInt(amount.slice(0, -6)).toLocaleString("zh-CN");
  return whole + "." + fraction;
}

type DashboardReport = {
  items: Usage[];
  from: string;
  to: string;
  team?: string | null;
  enterpriseExport: {
    id: string;
    name: string;
    costs: boolean;
  } | null;
  enterpriseFinancialExport?: { id: string; name: string } | null;
};

function Monitor({
  member,
  setMember,
  showProfile,
  ...filter
}: TeamFilter & {
  member: string;
  setMember: (id: string) => void;
  showProfile?: () => void;
}) {
  const [report, setReport] = useState<Usage[] | DashboardReport>([]);
  const rows = Array.isArray(report) ? report : report.items;
  const [error, setError] = useState("");

  const [days, setDays] = useState("30");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let current = true,
      generation = 0;
    async function load() {
      const mine = ++generation;
      try {
        const result = await api<Usage[] | DashboardReport>(
          "admin/usage?days=" +
            days +
            "&view=dashboard" +
            (filter.team ? "&team=" + encodeURIComponent(filter.team) : ""),
        );
        if (
          filter.team &&
          (Array.isArray(result) || result.team !== filter.team)
        )
          throw new Error("服务未返回所选团队的统计，请重新选择范围");
        if (current && mine === generation) {
          setReport(result);
          setError("");
        }
      } catch (e) {
        if (current && mine === generation) {
          setReport([]);
          setError((e as Error).message);
        }
      } finally {
        if (current && mine === generation) setLoading(false);
      }
    }
    setLoading(true);
    setError("");
    void load();
    const timer = window.setInterval(load, 30000);
    window.addEventListener("focus", load);
    return () => {
      current = false;
      window.clearInterval(timer);
      window.removeEventListener("focus", load);
    };
  }, [days, filter.team]);
  const legacy = rows.some((row) => row.deliveredEditions === undefined);
  const filtered = rows.filter((x) => !member || x.id === member);
  const count = (
    field: Exclude<
      keyof Usage,
      | "id"
      | "name"
      | "kind"
      | "recordedCny"
      | "remainingEstimateCny"
      | "runStates"
    >,
  ) => filtered.reduce((total, row) => total + row[field], 0);
  const waiting = count("waiting");
  const unknown = count("unknown");
  return (
    <section aria-label="运行监控" id="usage-monitor" tabIndex={-1}>
      <h2>运行监控</h2>
      <div className="usage-filters">
        <label>
          统计周期
          <select
            value={legacy ? "30" : days}
            disabled={legacy}
            onChange={(e) => setDays(e.target.value)}
          >
            {[7, 30, 90].map((n) => (
              <option key={n} value={n}>
                近 {n} 天
              </option>
            ))}
          </select>
        </label>
        {filter.teams.length > 1 && (
          <TeamSelect {...filter} label="用量统计团队" />
        )}
        <label>
          发起成员或应用
          <select value={member} onChange={(e) => setMember(e.target.value)}>
            <option value="">全部</option>
            {rows.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}（{x.kind === "application" ? "应用" : "成员"}）
              </option>
            ))}
          </select>
        </label>
      </div>
      {loading ? (
        <p role="status">正在读取统计…</p>
      ) : error ? (
        <p role="alert">{error}</p>
      ) : (
        <>
          {showProfile && <button onClick={showProfile}>查看成员资料</button>}
          {filter.team && <p>运行按项目团队统计，费用按实际承担团队统计。</p>}
          {legacy && (
            <p>
              当前管理服务提供近 30
              天的运行与已记账费用。更新服务后可查看审核、采纳和交付。
            </p>
          )}
          <dl className="usage-outcomes" aria-label="工作成果统计">
            {outcomeMetrics
              .filter(([key]) => !legacy || key === "completed")
              .map(([key, name]) => (
                <div key={key}>
                  <dt>{name}</dt>
                  <dd>{count(key)}</dd>
                </div>
              ))}
          </dl>
          {filtered.length > 0 && filtered.every((row) => row.runStates) && (
            <RunStateSummary groups={filtered.map((row) => row.runStates!)} />
          )}
          {!legacy && (
            <details>
              <summary>统计口径</summary>
              <p>
                执行完成按周期内发起的运行及其当前状态统计。能力样例按自动检查通过时间和试运行发起人统计。审核、采纳和交付按发生时间计入项目发起人。每个已审核版本和每批已确认交付各计一次。
              </p>
              <p>
                费用按周期内发起的调用归集，反映当前账单。待结算包含已预估和金额未知的调用，剩余预估扣除已记账金额。
              </p>
            </details>
          )}
          {!member &&
            !filter.team &&
            !Array.isArray(report) &&
            report.enterpriseExport && (
              <ManagementExport
                key={report.enterpriseExport.id}
                id={report.enterpriseExport.id}
                scope={"企业全域 · " + report.enterpriseExport.name}
                period={report}
                costs={report.enterpriseExport.costs}
              />
            )}
          {!member &&
            !filter.team &&
            !Array.isArray(report) &&
            report.enterpriseFinancialExport && (
              <ManagementExport
                key={report.enterpriseFinancialExport.id + "-charges"}
                id={report.enterpriseFinancialExport.id}
                scope={"企业全域 · " + report.enterpriseFinancialExport.name}
                period={report}
                costs
                financial
              />
            )}
          <div className="card">
            <p>
              发起运行 {count("runs")} ·{" "}
              {waiting > 0 ? (
                <strong>阻塞、待核对或暂停 {waiting}</strong>
              ) : (
                "阻塞、待核对或暂停 0"
              )}
            </p>
            <p>
              调用 {count("attempts")} 次 · 已知 {count("tokens")} token ·
              已记账 ¥{totalCny(filtered, "recordedCny")}
            </p>
            <p>
              {!legacy && (
                <>
                  待结算 {count("unsettledCalls")} 笔 · 剩余预估 ¥
                  {totalCny(filtered, "remainingEstimateCny")} ·{" "}
                </>
              )}

              {unknown > 0 ? (
                <strong>{unknown} 笔未知费用待核对</strong>
              ) : (
                "0 笔未知费用待核对"
              )}
            </p>
          </div>
        </>
      )}
    </section>
  );
}

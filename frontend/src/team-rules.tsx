import { useCallback, useEffect, useRef, useState } from "react";
import { MaintenancePanel } from "./maintenance";
import { RuleRecords, ruleTaskNames, type RuleTask } from "./rule-maintenance";
import type { Agent } from "./client";
import { api } from "./client";

type Team = { id: string; name: string; joined: boolean; default: boolean };
type Rule = {
  id: string;
  body: string;
  publishedBody: string | null;
  revision: number;
  published: number;
  lifecycle: number;
  disabled: boolean;
  expired: boolean;
  due: boolean;
  mine: boolean;
  expiresAt: string | null;
  publishedExpiresAt: string | null;
  reviewAt: string | null;
  owner: string;
  creator: string;
  invited: boolean;
  ownerAvailable: boolean;
  maintenanceVersion: number;
  agentScope?: string[];
  publishedAgentScope?: string[] | null;
};
type RuleAgent = Pick<Agent, "id" | "name" | "publishedName" | "team">;

function scopeNames(scope: string[] | null | undefined, agents: RuleAgent[]) {
  if (!scope?.length) return "团队全部步骤";
  return scope.map(id => {
    const agent = agents.find(item => item.id === id);
    return agent?.publishedName || agent?.name || "已绑定的 Agent";
  }).join("、");
}
function localDate(value: string | null) {
  if (!value) return "";
  const time = new Date(value);
  return new Date(time.getTime() - time.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}
function RuleDraft({
  rule,
  agents,
  save,
  cancel,
}: {
  rule?: Rule;
  agents: RuleAgent[];
  save: (body: string, expiresAt: string | null, agentScope: string[]) => Promise<void>;
  cancel?: () => void;
}) {
  const [body, setBody] = useState(rule?.body || ""),
    [date, setDate] = useState(localDate(rule?.expiresAt || null)),
    [agentScope, setAgentScope] = useState(rule?.agentScope ?? []),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <form
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError("");
        try {
          await save(body, date ? new Date(date).toISOString() : null, agentScope);
          setBody("");
          setDate("");
          setAgentScope([]);
        } catch (e) {
          setError((e as Error).message);
        } finally {
          setBusy(false);
        }
      }}
    >
      <label>
        规则正文
        <textarea
          required
          value={body}
          onChange={(e) => setBody(e.target.value)}
        />
      </label>
      <label>
        截止时间（选填，本地时间）
        <input
          type="datetime-local"
          value={date}
          onChange={(e) => setDate(e.target.value)}
        />
      </label>
      <details open={agentScope.length > 0 || undefined}>
        <summary>适用范围 · {scopeNames(agentScope, agents)}</summary>
        <p>默认适用团队全部步骤，勾选后限定在所选 Agent。</p>
        {agents.map(agent => (
          <label key={agent.id}>
            <input type="checkbox" checked={agentScope.includes(agent.id)} onChange={event => {
              setAgentScope(current => event.target.checked ? [...current, agent.id] : current.filter(id => id !== agent.id));
            }} />
            {agent.publishedName || agent.name}
          </label>
        ))}
        {agentScope.length > 0 && <button type="button" onClick={() => setAgentScope([])}>设为团队全部步骤</button>}
      </details>
      <div className="actions">
        <button disabled={busy || !body.trim()}>保存草稿</button>
        {cancel && (
          <button type="button" disabled={busy} onClick={cancel}>
            取消编辑
          </button>
        )}
      </div>
      {error && <p role="alert">{error}</p>}
    </form>
  );
}
function RuleCard({
  rule: r,
  team,
  admin,
  agents,
  reload,
}: {
  rule: Rule;
  team: string;
  admin: boolean;
  agents: RuleAgent[];
  reload: () => Promise<void>;
}) {
  const [editing, setEditing] = useState<Rule | null>(null),
    [reason, setReason] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const base = "teams/" + team + "/rules";
  async function action(review: boolean, revision: number, disabled = false) {
    setBusy(true);
    setError("");
    try {
      await api(
        base + "/" + r.id + (review ? "/review" : "/publish"),
        "POST",
        review
          ? { expected: r.lifecycle, revision, reason }
          : { expected: r.lifecycle, revision, disabled },
      );
      setReason("");
      await reload();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <article className="card" id={"team-rule-" + r.id} tabIndex={-1}>
      <strong>{r.published ? "已发布 v" + r.published : "待发布草稿"}</strong>
      {r.published > 0 && (
        <>
          <p className="preserve">{r.publishedBody}</p>
          <small>适用范围 · {scopeNames(r.publishedAgentScope, agents)}</small>
          <small>
            {r.disabled
              ? "已停用"
              : r.expired
                ? "已到期"
                : r.due
                  ? "待复查 · 仍然有效"
                  : "有效"}{" "}
            ·{" "}
            {r.publishedExpiresAt
              ? "截止 " + new Date(r.publishedExpiresAt).toLocaleString()
              : "长期有效"}
          </small>
        </>
      )}
      <small>
        维护人 · {r.owner}
        {!r.ownerAvailable && " · 职责待交接"}
        {r.reviewAt &&
          " · 复查日期 " + new Date(r.reviewAt).toLocaleDateString()}
      </small>
      <small>创建人 · {r.creator}</small>
      <MaintenancePanel
        subject="rule"
        version={r.maintenanceVersion}
        path={base + "/" + r.id + "/maintenance"}
        invited={r.invited}
        refresh={reload}
      />
      <RuleRecords path={base + "/" + r.id} version={r.maintenanceVersion} />
      {r.revision !== r.published && (
        <>
          <strong>草稿 v{r.revision}</strong>
          <p className="preserve">{r.body}</p>
          <small>草稿适用范围 · {scopeNames(r.agentScope, agents)}</small>
          <small>
            {r.expiresAt
              ? "草稿截止 " + new Date(r.expiresAt).toLocaleString()
              : "草稿长期有效"}
          </small>
        </>
      )}
      {editing ? (
        <RuleDraft
          rule={editing}
          agents={agents}
          cancel={() => setEditing(null)}
          save={async (body, expiresAt, agentScope) => {
            await api(base, "POST", {
              id: r.id,
              expected: editing.revision,
              body,
              expiresAt,
              agentScope,
            });
            setEditing(null);
            await reload();
          }}
        />
      ) : (
        <div className="actions">
          <button onClick={() => setEditing(r)}>编辑草稿</button>
          {admin && (r.published !== r.revision || r.disabled) && (
            <button
              disabled={
                busy || !!(r.expiresAt && Date.parse(r.expiresAt) <= Date.now())
              }
              onClick={() => void action(false, r.revision)}
            >
              发布 v{r.revision}
            </button>
          )}
          {admin && r.published > 0 && !r.disabled && (
            <button
              disabled={busy}
              onClick={() => void action(false, r.published, true)}
            >
              停用已发布规则
            </button>
          )}
        </div>
      )}
      {r.mine && r.published > 0 && !r.disabled && !r.expired && (
        <details>
          <summary>复查已发布 v{r.published}</summary>
          <label>
            复查依据
            <input value={reason} onChange={(e) => setReason(e.target.value)} />
          </label>
          <button
            disabled={busy || !reason.trim()}
            onClick={() => void action(true, r.published)}
          >
            确认复查
          </button>
        </details>
      )}
      {error && <p role="alert">{error}</p>}
    </article>
  );
}
function RuleList({
  team,
  admin,
  target,
  onRead,
}: {
  team: string;
  admin: boolean;
  target: string;
  onRead: () => void;
}) {
  const [items, setItems] = useState<Rule[]>([]),
    [agents, setAgents] = useState<RuleAgent[]>([]),
    [error, setError] = useState(""),
    [ready, setReady] = useState(false);
  const currentRequest = useRef(0),
    focused = useRef("");
  const reload = useCallback(async () => {
    const seq = ++currentRequest.current;
    try {
      const [data, available] = await Promise.all([api<Rule[]>("teams/" + team + "/rules"), api<RuleAgent[]>("agents")]);
      if (currentRequest.current !== seq) return;
      setItems(data);
      setAgents(available.filter(agent => agent.team === team));
      setError("");
      setReady(true);
      onRead();
    } catch (err) {
      if (currentRequest.current !== seq) return;
      setReady(false);
      setError((err as Error).message);
    }
  }, [team, onRead]);
  useEffect(() => {
    const sync = () => {
      if (!document.hidden) void reload();
    };
    sync();
    window.addEventListener("focus", sync);
    const timer = setInterval(sync, 30000);
    return () => {
      ++currentRequest.current;
      clearInterval(timer);
      window.removeEventListener("focus", sync);
    };
  }, [reload]);
  useEffect(() => {
    if (!target) focused.current = "";
    if (
      target &&
      focused.current !== target &&
      items.some((r) => r.id === target)
    ) {
      document.getElementById("team-rule-" + target)?.focus();
      focused.current = target;
    }
  }, [target, items]);
  return (
    <>
      <div className="actions">
        <button onClick={reload}>刷新团队规则</button>
      </div>
      {error && <p role="alert">{error}</p>}
      {ready && (
        <>
          <details>
            <summary>起草团队规则</summary>
            <RuleDraft
              agents={agents}
              save={async (body, expiresAt, agentScope) => {
                await api("teams/" + team + "/rules", "POST", {
                  body,
                  expiresAt,
                  agentScope,
                });
                await reload();
              }}
            />
          </details>
          {items.length === 0 && <p>团队尚无规则。</p>}
          {items.map((r) => (
            <RuleCard
              key={r.id}
              rule={r}
              team={team}
              admin={admin}
              agents={agents}
              reload={reload}
            />
          ))}
        </>
      )}
    </>
  );
}
export function TeamRules({ admin }: { admin: boolean }) {
  const [tasks, setTasks] = useState<RuleTask[]>([]),
    [target, setTarget] = useState("");
  const [teams, setTeams] = useState<Team[]>([]),
    [team, setTeam] = useState(""),
    [error, setError] = useState("");
  const request = useRef(0);
  const reloadDirectory = useCallback(async () => {
    const seq = ++request.current;
    try {
      const [directory, inbox] = await Promise.all([
        api<Team[]>("teams"),
        api<RuleTask[]>("rules/maintenance-inbox"),
      ]);
      if (seq !== request.current) return;
      const joined = directory.filter((t) => t.joined);
      setTeams(joined);
      setTasks(inbox);
      setError("");
      setTeam((current) =>
        joined.some((t) => t.id === current)
          ? current
          : (joined.find((t) => t.default) || joined[0])?.id || "",
      );
    } catch (e) {
      if (seq === request.current) {
        setTasks([]);
        setTeams([]);
        setTeam("");
        setError((e as Error).message);
      }
    }
  }, []);
  useEffect(() => {
    void reloadDirectory();
    window.addEventListener("focus", reloadDirectory);
    return () => {
      ++request.current;
      window.removeEventListener("focus", reloadDirectory);
    };
  }, [reloadDirectory]);
  return (
    <>
      {error && <p role="alert">{error}</p>}
      {error && <button onClick={reloadDirectory}>重新读取团队与待办</button>}
      {tasks.length > 0 && (
        <details>
          <summary>我的规则维护待办 · {tasks.length}</summary>
          {tasks.map((t) => (
            <button
              className="list"
              key={t.id}
              onClick={() => {
                setTeam(t.team);
                setTarget(t.id);
                document.getElementById("team-rule-" + t.id)?.focus();
              }}
            >
              <strong>
                {t.name} · {ruleTaskNames[t.action]}
              </strong>
              <small>{t.body}</small>
            </button>
          ))}
        </details>
      )}
      <label>
        适用团队
        <select
          value={team}
          onChange={(e) => {
            setTarget("");
            setTeam(e.target.value);
          }}
        >
          {teams.map((t) => (
            <option key={t.id} value={t.id}>
              {t.name}
            </option>
          ))}
        </select>
      </label>
      <p>
        已发布规则随方案固定版本。复查每 90
        天提醒一次，临时规则在截止时间停止使用。
      </p>
      {team && (
        <RuleList
          key={team}
          team={team}
          admin={admin}
          target={target}
          onRead={reloadDirectory}
        />
      )}
    </>
  );
}

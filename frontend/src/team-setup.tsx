import { ReviewPolicy } from "./review-reminders";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, statusLabel, type Agent, type Model } from "./client";
import type { AgentTeam } from "./agent-team";
import { AgentChecks } from "./agent-checks";
import { RequestRelease } from "./agent-release";
import { Models } from "./library";

type Template = {
  slot: string;
  name: string;
  instructions: string;
  sample: string;
  expected: string;
};
type Slot = {
  agent: string;
  revision: number;
  published: number;
  verified: boolean;
  ready: boolean;
  connected: boolean;
  name: string;
  resource: string | null;
  resourceRevision: number | null;
  disabled: boolean;
};
type Connection = {
  resource: string;
  revision: number;
  status: string;
  detail: string;
  at: string;
};
type Setup = {
  templates: Template[];
  state: {
    connections: Connection[];
    slots: Record<string, Slot>;
    passed: boolean;
    planned: boolean;
    planner: {
      resource: string;
      revision: number;
      version: number;
      name: string;
      enabled: boolean;
    } | null;
    projects: {
      id: string;
      stage: string;
      matches: boolean;
      createdAt: string;
    }[];
  };
};
type Props = {
  admin: boolean;
  agents: Agent[];
  models: Model[];
  refresh: () => Promise<void>;
  openProject: (id: string) => Promise<void>;
  useAgent?: (agent: Agent) => void;
};
export function TeamSetup({
  teams,
  teamError,
  preferredTeam,
  back,
  ...props
}: Props & {
  teams: AgentTeam[] | null;
  teamError: string;
  preferredTeam: string;
  back?: () => void;
}) {
  const [chosen, setTeam] = useState("");
  const fallback =
    teams?.find((option) => option.default) ||
    (teams?.length === 1 ? teams[0] : undefined);
  const target = chosen || preferredTeam || fallback?.id;
  const team = teams?.find((option) => option.id === target);
  return (
    <>
      {back && (
        <button type="button" onClick={back}>返回 Agent 列表</button>
      )}
      {teamError && <p role="alert">{teamError}</p>}
      {teams?.length === 0 && <p>请联系管理员开通可使用的团队资源。</p>}
      {teams?.length === 1 && team ? (
        <small>
          {team.name}
          {props.admin ? "承担本次配置的资源费用。" : "承担试运行费用。"}
        </small>
      ) : teams && teams.length > 0 ? (
        <label>
          {props.admin ? "配置团队" : "模板团队"}
          <select value={team?.id || ""} onChange={(e) => setTeam(e.target.value)}>
            {!team && <option value="">请选择归属</option>}
            {teams.map((t) => (
              <option key={t.id} value={t.id}>{t.name}</option>
            ))}
          </select>
        </label>
      ) : null}
      {team && (
        <SetupSteps
          key={team.id}
          {...props}
          team={team.id}
          models={props.models.filter((m) => m.team === team.id)}
        />
      )}
      {team && props.admin && (
        <ReviewPolicy key={"reminders-" + team.id} scope={"teams/" + team.id} />
      )}
    </>
  );
}
const probeState: Record<string, string> = {
  submitted: "正在核对",
  passed: "连通通过",
  rejected: "供应商拒绝",
  invalid: "响应不符合模型接口",
  unknown: "结果待核对",
};
function ConnectionCheck({
  model,
  receipts,
  admin,
  busy,
  run,
}: {
  model: Model;
  receipts: Connection[];
  admin: boolean;
  busy: boolean;
  run: () => Promise<void>;
}) {
  const latest = receipts[0];
  return (
    <details>
      <summary>
        {model.name} v{model.revision} ·{" "}
        {latest ? probeState[latest.status] : "未做独立连通验证"}
      </summary>
      {receipts.map((r, i) => (
        <p key={r.at + i}>
          <strong>{probeState[r.status]}</strong> ·{" "}
          {new Date(r.at).toLocaleString()} {r.detail}
        </p>
      ))}
      {admin && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const form = e.currentTarget;
            void run().then(() => form.reset());
          }}
        >
          <label>
            <input type="checkbox" required />
            授权发送一次合成连通请求，费用由团队承担。
            {model.operation === "png" && "将生成一张 PNG 并校验图像。"}
            {model.estimate == null
              ? "单次费用未知，按实际账单核对。"
              : "预计 ¥" + model.estimate + "。"}
            {latest?.status === "unknown" &&
              "请先核对前次账单，再授权新的调用。"}
          </label>
          <button disabled={busy || latest?.status === "submitted"}>
            验证模型连通
          </button>
        </form>
      )}
    </details>
  );
}
function SetupSteps({
  admin,
  agents,
  team,
  models,
  refresh,
  openProject,
  useAgent,
}: Props & { team: string }) {
  const base = "teams/" + team + "/setup";
  const [data, setData] = useState<Setup | null>(null),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const latest = useRef(0);
  const reload = useCallback(async () => {
    const generation = ++latest.current;
    const d = await api<Setup>(base);
    if (generation === latest.current) setData(d);
  }, [base]);
  useEffect(() => {
    void reload().catch((e) => setError(e.message));
    return () => {
      ++latest.current;
    };
  }, [reload]);
  async function action(fn: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      await refresh();
      await reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const ready =
    data?.templates.filter((t) => data.state.slots[t.slot]?.ready).length || 0;
  const textModels = models.filter((model) => model.operation === "text");
  const usableModels = textModels.filter((model) => !model.credentialBlocked);
  const soleModel = !admin && usableModels.length === 1 ? usableModels[0] : undefined;
  async function sample() {
    if (!data) return;
    const slots = data.templates.map((t) => data.state.slots[t.slot]);
    const agents = slots.map((s) => s.agent),
      revisions = slots.map((s) => s.published);
    const slot =
      "alliance.setup." +
      team +
      "." +
      agents.join(".") +
      "." +
      revisions.join(".");
    const key = sessionStorage.getItem(slot) || crypto.randomUUID();
    sessionStorage.setItem(slot, key);
    const project = await api<{ id: string }>(
      base + "/samples",
      "POST",
      { agents, revisions },
      { "Idempotency-Key": key },
    );
    await openProject(project.id);
    sessionStorage.removeItem(slot);
  }
  return (
    <>
      <div className="actions">
        <button disabled={busy} onClick={() => void action(async () => {})}>
          刷新配置状态
        </button>
      </div>
      {error && <p role="alert">{error}</p>}
      {admin && (
        <details>
          <summary>接入团队模型 · {models.length} 项</summary>
          <Models
            models={models}
            team={team}
            refresh={async () => {
              await refresh();
              await reload();
            }}
          />
          {data &&
            models.map((m) => (
              <ConnectionCheck
                key={m.id + ":" + m.revision}
                model={m}
                admin={admin}
                busy={busy}
                receipts={data.state.connections.filter(
                  (r) => r.resource === m.id && r.revision === m.revision,
                )}
                run={() =>
                  action(async () => {
                    const slot = "alliance.connection." + m.id + "." + m.revision;
                    const key =
                      sessionStorage.getItem(slot) || crypto.randomUUID();
                    sessionStorage.setItem(slot, key);
                    const response = await api<{ status: string }>(
                      base + "/connections",
                      "POST",
                      {
                        resource: m.id,
                        revision: m.revision,
                        authorizeOneCall: true,
                      },
                      { "Idempotency-Key": key },
                    );
                    if (response.status !== "submitted")
                      sessionStorage.removeItem(slot);
                  })
                }
              />
            ))}
        </details>
      )}
      {data && (
        <>
          {admin && (
            <details>
              <summary>
                规划助手默认模型 ·{" "}
                {data.state.planner
                  ? data.state.planner.name + " v" + data.state.planner.revision
                  : "待配置"}
              </summary>
              <p>
                新项目会固定此模型修订，规划时可另选已授权模型。已有项目沿用创建时的配置。每次模型调用仍需确认费用。
              </p>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const m = models.find(
                    (m) =>
                      m.id === new FormData(e.currentTarget).get("planner"),
                  );
                  if (m)
                    void action(() =>
                      api(base + "/planner", "PUT", {
                        resource: m.id,
                        revision: m.revision,
                        expected: data.state.planner?.version || 0,
                      }),
                    );
                }}
              >
                <label>
                  新项目的规划模型
                  <select name="planner" required defaultValue="">
                    <option value="">选择模型修订</option>
                    {textModels.map((m) => (
                      <option
                        key={m.id}
                        value={m.id}
                        disabled={m.credentialBlocked}
                      >
                        {m.name} · v{m.revision}
                        {m.credentialBlocked && " · 团队凭据待修复"}
                      </option>
                    ))}
                  </select>
                </label>
                <button disabled={busy || usableModels.length === 0}>
                  设为新项目默认模型
                </button>
              </form>
            </details>
          )}
          <p>
            <strong>
              {ready} / {data.templates.length} 个助手可用
            </strong>{" "}
            ·{" "}
            {data.state.passed
              ? "当前发布组合已完成审核交付"
              : "当前发布组合待协作验证"}
          </p>
          {data.templates.map((t) => {
            const s = data.state.slots[t.slot];
            const agent = s && agents.find((item) => item.id === s.agent);
            const needsModel = t.slot !== "document";
            return (
              <article key={t.slot} className="card">
                <h3>{t.name}</h3>
                <p>{t.instructions}</p>
                {!s ? (
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      const id = new FormData(e.currentTarget).get("model");
                      const m = models.find((m) => m.id === id);
                      void action(() =>
                        api(
                          base + "/" + t.slot,
                          "PUT",
                          m ? { resource: m.id, revision: m.revision } : {},
                        ),
                      );
                    }}
                  >
                    {needsModel && (
                      soleModel ? (
                        <>
                          <input type="hidden" name="model" value={soleModel.id} />
                          <small>使用 {soleModel.name} v{soleModel.revision}</small>
                        </>
                      ) : !admin && usableModels.length === 0 ? (
                        <small>请联系管理员接入可用的文本模型。</small>
                      ) : (
                        <label>
                          绑定文本模型
                          <select name="model" required defaultValue="">
                            <option value="">请选择团队模型</option>
                            {textModels.map((m) => (
                              <option
                                key={m.id}
                                value={m.id}
                                disabled={m.credentialBlocked}
                              >
                                {m.name} · {m.model} · v{m.revision}
                                {m.credentialBlocked && " · 团队凭据待修复"}
                              </option>
                            ))}
                          </select>
                        </label>
                      )
                    )}
                    <button
                      disabled={busy || (needsModel && usableModels.length === 0)}
                    >
                      按模板创建草稿
                    </button>
                  </form>
                ) : (
                  <>
                    <small>
                      {s.resource ? "模型" : "Word 连接器"} ·{" "}
                      {s.connected
                        ? "此修订已有连通回执"
                        : "此修订尚无连通回执"}
                      {s.resource &&
                        " · " +
                          (models.find((m) => m.id === s.resource)?.name ||
                            "绑定资源") +
                          " v" +
                          s.resourceRevision}
                    </small>
                    <p>
                      草稿 v{s.revision} · {s.verified ? "验证通过" : "待验证"}
                      　
                      {s.ready
                        ? "可使用已发布 v" + s.published
                        : s.published
                          ? "已发布版本当前不可用"
                          : "尚未发布"}
                    </p>
                    {s.disabled ? (
                      <small>助手已停用，请先核对团队能力库。</small>
                    ) : (
                      <AgentChecks
                        agent={s.agent}
                        revision={s.revision}
                        refresh={async () => {
                          await refresh();
                          await reload();
                        }}
                      />
                    )}
                    {admin &&
                      !s.disabled &&
                      s.verified &&
                      s.revision !== s.published && (
                        <button
                          disabled={busy}
                          onClick={() =>
                            void action(() =>
                              api("agents/" + s.agent + "/publish", "POST", {
                                revision: s.revision,
                              }),
                            )
                          }
                        >
                          发布 v{s.revision} 供团队使用
                        </button>
                      )}
                    {!admin && agent && !s.disabled &&
                      agent.published !== agent.revision &&
                      agent.revision === s.revision &&
                      (agent.validated || agent.release) && (
                        <RequestRelease
                          agent={agent}
                          refresh={async () => {
                            await refresh();
                            await reload();
                          }}
                        />
                      )}
                    {useAgent && s.ready && agent?.publishedReady &&
                      agent.published === s.published && (
                        <button type="button" disabled={busy} onClick={() => useAgent(agent)}>
                          用于当前对话
                        </button>
                      )}
                  </>
                )}
              </article>
            );
          })}
          <section>
            <h3>{admin ? "协作验证" : "试用内置组合"}</h3>
            {admin && (
              <p>
                {data.state.planned
                  ? "默认规划助手的建议已被采纳并完成交付"
                  : "规划验证待完成。可在样例项目中请默认模型规划，核对并采用建议后完成交付。"}
              </p>
            )}
            <p>
              以合成的培训安排为资料，整理简报、核对事实并生成
              Word。打开样例后确认方案和费用，审核三个成果并确认交付。
            </p>
            <button
              className="primary"
              disabled={busy || ready !== data.templates.length}
              onClick={() => void action(sample)}
            >
              创建协作验证项目
            </button>
            {data.state.projects.map((p) => (
              <button
                className="list"
                key={p.id}
                onClick={() => void openProject(p.id)}
              >
                打开验证项目 · {statusLabel[p.stage] || p.stage}
                <small>
                  {new Date(p.createdAt).toLocaleString()} ·{" "}
                  {p.matches ? "匹配当前组合" : "配置或方案版本已变化"}
                </small>
              </button>
            ))}
          </section>
        </>
      )}
    </>
  );
}

import { useRef, useState } from "react";
import { AgentChecks } from "./agent-checks";
import { api, RequestError } from "./client";
import type { Impact, Resource, Revision } from "./model-maintenance";

const states = {
  pending: "待复验或资源不可用",
  ready: "复验通过，待发布",
  published: "已发布",
  superseded: "已有其他修订或发布决策",
  disabled: "已停用",
  unavailable: "已发布，当前不可用",
  restored: "当前已回到原修订",
};
export type BatchReceipt = {
  id: string;
  target: string;
  revision: number;
  created: string;
  reason: string;
  agents: {
    id: string;
    original: number;
    name: string;
    revision: number;
    rollbackReady: boolean;
    rollbackReason: string | null;
    status: keyof typeof states;
  }[];
};

export function ReplacementBatch({
  resources,
  resource,
  revision,
  impact,
  reload,
}: {
  resources: Resource[];
  resource: Resource;
  revision: Revision;
  impact: Impact;
  reload: () => Promise<void>;
}) {
  const [target, setTarget] = useState("");
  const [selected, setSelected] = useState<string[]>([]);
  const [reason, setReason] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [receipt, setReceipt] = useState("");
  const request = useRef({ body: "", key: "" });
  const targets = resources
    .filter((r) => r.team === resource.team && r.enabled && !r.emergency)
    .flatMap((r) =>
      r.revisions
        .filter(
          (v) =>
            !v.blocked &&
            v.operation === revision.operation &&
            (r.id !== resource.id || v.revision !== revision.revision),
        )
        .map((v) => ({
          id: r.id + "/" + v.revision + "/" + v.generation,
          resource: r,
          revision: v,
        })),
    );
  const dest = targets.find((t) => t.id === target);
  const candidates = impact.agents.filter((a) => a.published);
  const matches = candidates.filter(
    (a) =>
      a.eligible &&
      selected.includes(a.id + "/" + a.revision + "/" + a.generation),
  );
  const canPrepare =
    dest &&
    matches.length > 0 &&
    matches.length === selected.length &&
    reason.trim() &&
    confirmed;
  const clearConsent = () => {
    setConfirmed(false);
    setReceipt("");
    setError("");
  };
  async function showFailure(e: unknown) {
    setError((e as Error).message);
    if (e instanceof RequestError && [403, 404, 409].includes(e.status)) {
      setConfirmed(false);
      await reload().catch((err) => setError(err.message));
    }
  }
  async function prepare() {
    if (!canPrepare || !dest || busy) return;
    const body = {
      target: dest.resource.id,
      targetRevision: dest.revision.revision,
      targetGeneration: dest.revision.generation,
      reason: reason.trim(),
      useTeamCredential: true,
      agents: matches.map((a) => ({
        id: a.id,
        published: a.revision,
        generation: a.generation,
      })),
    };
    const signature = JSON.stringify(body);
    if (request.current.body !== signature)
      request.current = { body: signature, key: crypto.randomUUID() };
    setBusy(true);
    setError("");
    try {
      const result = await api<{ id: string }>(
        `admin/model-resources/${resource.id}/revisions/${revision.revision}/replacements`,
        "POST",
        body,
        { "Idempotency-Key": request.current.key },
      );
      setReceipt(result.id);
      setSelected([]);
      setConfirmed(false);
      await reload();
    } catch (e) {
      await showFailure(e);
    } finally {
      setBusy(false);
    }
  }
  async function changePublication(
    path: string,
    body: Record<string, unknown>,
  ) {
    setBusy(true);
    setError("");
    try {
      await api(path, "POST", body);
      await reload();
    } catch (e) {
      await showFailure(e);
    } finally {
      setBusy(false);
    }
  }
  return (
    <details>
      <summary>按批次替换助手的模型</summary>
      <p>
        当前修订关联 {impact.pinnedServices} 个本团队已启用服务，以及{" "}
        {impact.pinnedProjects}{" "}
        个你可访问的在途项目。已保存方案、服务版本和团队规划设置继续引用原修订。
      </p>
      {impact.defaultPlanner && (
        <p>此修订也是团队默认规划模型，可在团队首次配置中单独更换。</p>
      )}
      <fieldset className="plan-fields" disabled={busy}>
        <label>
          目标模型修订
          <select
            value={dest?.id || ""}
            onChange={(e) => {
              setTarget(e.target.value);
              clearConsent();
            }}
          >
            <option value="">请选择同团队、同操作的模型修订</option>
            {targets.map((t) => (
              <option value={t.id} key={t.id}>
                {t.resource.name} v{t.revision.revision} · {t.revision.model}
              </option>
            ))}
          </select>
        </label>
        {dest && (
          <p className="management-task">
            操作 · {revision.operation === "png" ? "生成单张 PNG" : "文本对话"}
            <br />
            原配置 · {revision.model} · {revision.endpoint} · 单次预计{" "}
            {revision.estimate === null ? "金额未知" : "¥" + revision.estimate}
            <br />
            目标配置 · {dest.revision.model} · {dest.revision.endpoint} ·
            单次预计{" "}
            {dest.revision.estimate === null
              ? "金额未知"
              : "¥" + dest.revision.estimate}
          </p>
        )}
        {candidates.length === 0 && <p>此修订没有当前已发布的助手。</p>}
        {candidates.map((a) => {
          const id = a.id + "/" + a.revision + "/" + a.generation;
          return (
            <label key={id}>
              <input
                type="checkbox"
                disabled={!a.eligible}
                checked={selected.includes(id)}
                onChange={(e) => {
                  setSelected(
                    e.target.checked
                      ? [...selected, id]
                      : selected.filter((x) => x !== id),
                  );
                  clearConsent();
                }}
              />{" "}
              {a.name} v{a.revision} ·{" "}
              {a.independent ? "原用独立凭据" : "原用团队凭据"}
              {!a.eligible && " · 已有更新草稿或已停用"}
            </label>
          );
        })}
        {matches.length !== selected.length && (
          <div>
            <p role="status">部分选择已发生变化，请重新核对助手范围。</p>
            <button
              type="button"
              onClick={() => {
                setSelected(
                  matches.map(
                    (a) => a.id + "/" + a.revision + "/" + a.generation,
                  ),
                );
                clearConsent();
              }}
            >
              移除已失效的选择
            </button>
          </div>
        )}
        <label>
          替换依据与已核对的差异
          <textarea
            value={reason}
            maxLength={1000}
            onChange={(e) => {
              setReason(e.target.value);
              clearConsent();
            }}
            placeholder="说明输入输出限制、数据授权和费用差异"
          />
        </label>
        <label>
          <input
            type="checkbox"
            checked={confirmed}
            onChange={(e) => setConfirmed(e.target.checked)}
          />{" "}
          所选助手的新草稿统一使用目标团队凭据，团队承担调用费用。
        </label>
        <button disabled={!canPrepare} onClick={() => void prepare()}>
          为所选助手创建替换草稿
        </button>
      </fieldset>
      {error && <p role="alert">{error}</p>}
      {receipt && (
        <p role="status">
          替换草稿已保存。展开助手可核对两版的样例及运行结果，并复验新修订。
        </p>
      )}
      {(impact.replacements || []).map((b) => (
        <section key={b.id}>
          <h3>
            {b.target} v{b.revision} · 替换批次
          </h3>
          <small>
            {new Date(b.created).toLocaleString()} · {b.reason}
          </small>
          <ul>
            {b.agents.map((a) => (
              <li key={a.id}>
                <details>
                  <summary>
                    {a.name} v{a.revision} · {states[a.status]}
                  </summary>
                  {a.status !== "disabled" && (
                    <>
                      <AgentChecks
                        agent={a.id}
                        revision={a.original}
                        refresh={reload}
                        pinned
                      />
                      <AgentChecks
                        agent={a.id}
                        revision={a.revision}
                        refresh={reload}
                        pinned
                      />
                    </>
                  )}
                  {a.status === "ready" && (
                    <button
                      className="primary"
                      disabled={busy}
                      onClick={() =>
                        void changePublication("agents/" + a.id + "/publish", {
                          revision: a.revision,
                        })
                      }
                    >
                      发布已复验的 v{a.revision}
                    </button>
                  )}
                  {a.rollbackReason && (
                    <p>已记录的回退依据 · {a.rollbackReason}</p>
                  )}
                  {(a.status === "published" || a.status === "unavailable") &&
                    !a.rollbackReason && (
                      <details>
                        <summary>回退此助手的发布修订</summary>
                        {a.rollbackReady ? (
                          <form
                            onSubmit={(event) => {
                              event.preventDefault();
                              void changePublication(
                                "admin/model-replacements/" +
                                  b.id +
                                  "/agents/" +
                                  a.id +
                                  "/rollback",
                                {
                                  reason: new FormData(event.currentTarget).get(
                                    "reason",
                                  ),
                                },
                              );
                            }}
                          >
                            <p>
                              新方案将使用原助手 v{a.original}{" "}
                              的模型和凭据。已受理任务、已有服务和成果继续使用各自固定的版本。
                            </p>
                            <label>
                              回退依据
                              <input name="reason" required maxLength={1000} />
                            </label>
                            <button disabled={busy}>
                              确认恢复发布 v{a.original}
                            </button>
                          </form>
                        ) : (
                          <p>
                            原资源、验证或当前发布记录已变化，请核对后刷新。
                          </p>
                        )}
                      </details>
                    )}
                </details>
              </li>
            ))}
          </ul>
        </section>
      ))}
      <button
        disabled={busy}
        onClick={() => void reload().catch((e) => setError(e.message))}
      >
        刷新替换进度
      </button>
    </details>
  );
}

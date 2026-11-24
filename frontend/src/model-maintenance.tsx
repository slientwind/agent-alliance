import { ReplacementBatch, type BatchReceipt } from "./model-replacement";
import { useEffect, useState } from "react";
import { api, type Model } from "./client";

export type Revision = {
  operation: Model["operation"];
  streamText?: boolean;
  revision: number;
  model: string;
  endpoint: string;
  estimate: string | null;
  blocked: boolean;
  generation: number;
};
export type Resource = {
  id: string;
  team: string;
  teamName: string;
  name: string;
  enabled: boolean;
  emergency: boolean;
  revisions: Revision[];
};
export type Impact = {
  defaultPlanner: boolean;
  generation: number;
  lifecycle: number;
  enabled: boolean;
  blocked: boolean;
  replacements: BatchReceipt[];
  pinnedServices: number;
  pinnedProjects: number;
  agents: {
    id: string;
    eligible: boolean;
    generation: number;
    name: string;
    revision: number;
    independent: boolean;
    published: boolean;
  }[];
  projects: { id: string; title: string; stage: string }[];
  services: string[];
  candidate: {
    id: string;
    reason: string;
    status: string | null;
    detail: string | null;
    tested: boolean;
  } | null;
};
export function ModelMaintenance({
  team,
  refresh,
}: {
  team?: string;
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [version, reload] = useState(0);
  const [all, setAll] = useState<Resource[]>([]),
    [selected, select] = useState(""),
    [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let live = true;
    setError("");
    api<Resource[]>(
      "admin/model-resources" +
        (team ? "?team=" + encodeURIComponent(team) : ""),
    )
      .then((items) => {
        if (live) setAll(items || []);
      })
      .catch((e) => {
        if (live) {
          setAll([]);
          setError(e.message);
        }
      });
    return () => {
      live = false;
    };
  }, [team, open, version]);
  const choices = all.flatMap((resource) =>
    resource.revisions.map((revision) => ({
      id: resource.id + "/" + revision.revision,
      resource,
      revision,
    })),
  );
  const item = choices.find((x) => x.id === selected) || choices[0];
  return (
    <details className="model-maintenance" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>维护模型与凭据</summary>
      {open && (
        <>
          {error && <p role="alert">{error}</p>}
          {item ? (
            <>
              <label>
                要维护的模型修订
                <select
                  value={item.id}
                  onChange={(e) => select(e.target.value)}
                >
                  {choices.map((x) => (
                    <option value={x.id} key={x.id}>
                      {x.resource.teamName} · {x.resource.name} v
                      {x.revision.revision}
                    </option>
                  ))}
                </select>
              </label>
              <RevisionMaintenance
                key={item.id}
                resources={all}
                resource={item.resource}
                revision={item.revision}
                refresh={async () => {
                  reload((v) => v + 1);
                  await refresh();
                }}
              />
            </>
          ) : (
            <p>暂无可维护的模型。</p>
          )}
        </>
      )}
    </details>
  );
}

function RevisionMaintenance({
  resources,
  resource,
  revision,
  refresh,
}: {
  resources: Resource[];
  resource: Resource;
  revision: Revision;
  refresh: () => Promise<void>;
}) {
  const base =
    "admin/model-resources/" + resource.id + "/revisions/" + revision.revision;
  const [impact, setImpact] = useState<Impact | null>(null),
    [reason, setReason] = useState(""),
    [error, setError] = useState(""),
    [message, setMessage] = useState(""),
    [busy, setBusy] = useState(false);
  const [epoch, reload] = useState(0);
  useEffect(() => {
    let live = true;
    api<Impact>(base)
      .then((x) => {
        if (live) setImpact(x);
      })
      .catch((e) => {
        if (live) {
          setError(e.message);
          setImpact(null);
        }
      });
    return () => {
      live = false;
    };
  }, [base, epoch]);
  async function act(fn: () => Promise<string>) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      setMessage(await fn());
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      reload((v) => v + 1);
      setBusy(false);
    }
  }
  const candidate = impact?.candidate;
  const mutate = (action: string, fields: Record<string, unknown> = {}) =>
    api(base + "/credentials", "POST", {
      action,
      reason,
      generation: impact!.generation,
      candidate: candidate?.id,
      ...fields,
    });
  return (
    <section aria-label="模型修订维护">
      <p>
        {revision.model} · {revision.endpoint}
      </p>
      {error && <p role="alert">{error}</p>}
      {message && <p role="status">{message}</p>}
      {impact && (
        <>
          <p>
            <strong>
              {resource.emergency
                ? "已紧急停止"
                : !impact.enabled
                  ? "已停用"
                  : impact.blocked
                    ? "团队凭据被拒绝"
                    : "已启用"}
            </strong>{" "}
            · 凭据第 {impact.generation} 代
          </p>
          <details>
            <summary>查看依赖此修订的助手、服务与项目</summary>
            {impact.defaultPlanner && <p>团队默认规划助手使用此修订。</p>}
            <ul>
              {impact.agents.map((a, i) => (
                <li key={i}>
                  {a.name} v{a.revision} · {a.published ? "已发布" : "草稿"} ·{" "}
                  {a.independent ? "助手独立凭据" : "继承团队凭据"}
                </li>
              ))}
            </ul>
            <p>使用团队凭据的服务 · {impact.services.join("、") || "无"}</p>
            <p>
              我可访问的在途项目 ·{" "}
              {impact.projects.map((p) => p.title).join("、") || "无"}
            </p>
          </details>
          <ReplacementBatch
            resources={resources}
            resource={resource}
            revision={revision}
            impact={impact}
            reload={async () => {
              reload((n) => n + 1);
              await refresh();
            }}
          />
          <label>
            维护原因
            <input
              maxLength={1000}
              value={reason}
              onChange={(e) => setReason(e.target.value)}
            />
          </label>
          <fieldset className="plan-fields" disabled={busy}>
            {candidate ? (
              <>
                <p>待启用凭据 · {candidate.reason}</p>
                <p>
                  <strong>
                    {candidate.tested
                      ? "候选凭据验证通过"
                      : candidate.status === "passed"
                        ? "资源状态已变化，请重新验证"
                        : candidate.detail || "候选凭据待验证"}
                  </strong>
                </p>
                <button
                  disabled={!impact.enabled}
                  onClick={() =>
                    void act(async () => {
                      const slot =
                        "model-key-probe." +
                        candidate.id +
                        "." +
                        impact.lifecycle;
                      const requestKey =
                        sessionStorage.getItem(slot) || crypto.randomUUID();
                      sessionStorage.setItem(slot, requestKey);
                      const reply = await api<{
                        status: string;
                        detail: string;
                      }>(
                        "teams/" + resource.team + "/setup/connections",
                        "POST",
                        {
                          resource: resource.id,
                          revision: revision.revision,
                          candidate: candidate.id,
                          authorizeOneCall: true,
                        },
                        { "Idempotency-Key": requestKey },
                      );
                      return reply.detail;
                    })
                  }
                >
                  授权一次候选凭据验证 · 团队承担费用
                </button>
                <button
                  className="primary"
                  disabled={
                    !candidate.tested || !impact.enabled || !reason.trim()
                  }
                  onClick={() =>
                    void act(async () => {
                      await mutate("apply");
                      return "已启用验证通过的凭据。";
                    })
                  }
                >
                  启用已验证凭据
                </button>
                <button
                  disabled={!reason.trim()}
                  onClick={() =>
                    void act(async () => {
                      await mutate("discard");
                      return "候选凭据已清除。";
                    })
                  }
                >
                  放弃候选凭据
                </button>
              </>
            ) : (
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const form = e.currentTarget;
                  const data = new FormData(form);
                  void act(async () => {
                    await mutate("prepare", {
                      key: data.get("key"),
                      sameScope: data.has("scope"),
                    });
                    form.reset();
                    return "候选凭据已加密保存，验证后可启用。";
                  });
                }}
              >
                <label>
                  新的团队凭据
                  <input
                    type="password"
                    name="key"
                    required
                    autoComplete="new-password"
                  />
                </label>
                <label>
                  <input type="checkbox" required name="scope" />{" "}
                  已核对计费账号、地域和授权范围相同
                </label>
                <button disabled={!reason.trim()}>准备同范围凭据轮换</button>
              </form>
            )}
            <details>
              <summary>调整整个模型资源的运行状态</summary>
              <p>
                普通停用阻止新任务，已受理任务可继续。紧急停止还会阻止在途任务后续调用。
              </p>
              <div className="actions">
                {[
                  ["enable", "启用资源"],
                  ["disable", "普通停用"],
                  ["emergency-stop", "紧急停止"],
                ].map(([action, label]) => (
                  <button
                    key={action}
                    disabled={
                      !reason.trim() ||
                      (action === "enable"
                        ? impact.enabled
                        : action === "disable"
                          ? !impact.enabled
                          : resource.emergency)
                    }
                    onClick={() =>
                      void act(async () => {
                        await api(
                          "admin/catalog/models/" + resource.id,
                          "POST",
                          { action, reason, expected: impact.lifecycle },
                        );
                        return "资源状态已更新。";
                      })
                    }
                  >
                    {label}
                  </button>
                ))}
              </div>
            </details>
            <details>
              <summary>更换模型或供应商</summary>
              <p>
                保存新修订后，在助手或团队规划设置中明确选择并复验。既有项目继续引用原修订。
              </p>
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const form = e.currentTarget;
                  const data = new FormData(form);
                  void act(async () => {
                    await api("resources", "POST", {
                      id: resource.id,
                      name: resource.name,
                      expected: resource.revisions[0].revision,
                      endpoint: data.get("endpoint"),
                      model: data.get("model"),
                      operation: data.get("operation"),
                      streamText: data.has("streamText"),
                      key: data.get("key"),
                      estimate: data.get("estimate") || null,
                    });
                    form.reset();
                    return "已建立新的模型修订。";
                  });
                }}
              >
                <label>
                  新修订服务地址
                  <input type="url" required name="endpoint" />
                </label>
                <label>
                  <input name="streamText" type="checkbox" defaultChecked={revision.streamText} /> 桌面逐段预览（仅文本模型）
                </label>
                <label>
                  新修订模型操作
                  <select name="operation" defaultValue={revision.operation}>
                    <option value="text">文本对话</option>
                    <option value="png">文生图 · 单张 PNG</option>
                    <option value="vision">视觉理解 · PNG 或 JPEG 转文本</option>
                    <option value="image-edit">参考图编辑 · 单张 PNG</option>
                  </select>
                </label>
                <label>
                  新修订模型标识
                  <input required name="model" />
                </label>
                <label>
                  新修订密钥
                  <input
                    type="password"
                    required
                    name="key"
                    autoComplete="new-password"
                  />
                </label>
                <label>
                  新修订单次预计金额（CNY）
                  <input name="estimate" inputMode="decimal" />
                </label>
                <button>保存为新修订</button>
              </form>
            </details>
          </fieldset>
        </>
      )}
    </section>
  );
}

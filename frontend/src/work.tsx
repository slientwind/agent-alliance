import { AggregationScope } from "./aggregation-scope";
import { fileFormats } from "./output-operations";
import { PlanningInputs, planningRequestKey, selectedPins, type PlanningPin } from "./planning-inputs";
import { LookupBody } from "./lookup-body";
import type { BudgetDraft } from "./budget-request-form";
import type { DraftField } from "./project-drafts";
import { ReviewPolicy } from "./review-reminders";
import { outputOperations } from "./output-operations";
import { DistributionReview, held } from "./distribution";
import { DeliveryConfirmation, DeliveryHistory } from "./delivery";
import { CallRecovery } from "./call-recovery";
import { SourceReceipt } from "./source-receipt";
import { SourceReview, hasSourceReview } from "./source-impact";
import { ProjectFeedback, type ProjectFeedbackDraft } from "./project-feedback";
import { ServiceRecommendations } from "./service-entry";
import { AgentChecks } from "./agent-checks";
import { useState, useEffect, useCallback, useRef } from "react";
import {
  api,
  RequestError,
  type Model,
  type Agent,
  type Member,
  type Project,
  type WorkflowTemplateReference,
  statusLabel,
} from "./client";
import { FilePreview, canPreview } from "./file-preview";
import { ArtifactEditor, type RevisionDraft } from "./artifact-editor";
import { ArtifactHistory } from "./artifact-history";
import { RemoteReceipt } from "./remote-receipt";
import { Diagnostics } from "./diagnostics";
import { ProjectCharges } from "./project-charges";
import { Participants } from "./participants";
import { Feedback, blocksApproval, type FeedbackDraft } from "./feedback";
import { ProjectAgentGrants } from "./project-agent-grants";
import { SuggestedPlan } from "./suggested-plan";
import { currentPlan, PlanEditor, planFromTemplateUpdate, type PlanDraft } from "./plan-editor";
import { SaveWorkflowTemplate } from "./workflow-templates";
import { WorkflowTemplateVersionReview } from "./workflow-template-update";
import { Sources, type Source, type Preference } from "./knowledge";

type Props = {
  p: Project;
  threadRef: (thread: HTMLDivElement | null) => void;
  uploadedPins?: PlanningPin[];
  planningSubmitted: (uploads?: PlanningPin[]) => void;
  revisions?: Record<string, RevisionDraft>;
  changeRevision: (
    id: string,
    next?: RevisionDraft,
    expected?: RevisionDraft,
  ) => void;
  budgetDraft: DraftField<BudgetDraft>;
  planDraft?: PlanDraft;
  changePlanDraft: (next?: PlanDraft, expected?: PlanDraft) => void;
  projectFeedbackDrafts: Map<string, ProjectFeedbackDraft>;
  feedbackDrafts: Map<string, FeedbackDraft>;
  user: Member;
  agents: Agent[];
  models: Model[];
  refresh: () => Promise<void>;
  openLibrary: () => void;
  openWorkflowTemplates: (template?: WorkflowTemplateReference) => void;
  onRecoveryDelivered: (id: string) => void;
  reloadCatalog: () => Promise<void>;
};
export function Work({
  p, threadRef, uploadedPins, planningSubmitted,
  revisions,
  changeRevision,
  budgetDraft,
  planDraft,
  changePlanDraft,
  projectFeedbackDrafts,
  feedbackDrafts,
  user,
  agents,
  models,
  refresh,
  openLibrary,
  openWorkflowTemplates,
  onRecoveryDelivered,
  reloadCatalog,
}: Props) {
  const [err, setErr] = useState(""),
    [busy, setBusy] = useState(false);
  const [sources, setSources] = useState<Source[]>([]),
    [prefs, setPrefs] = useState<Preference[]>([]);
  const options = useRef<HTMLElement>(null);
  const planSection = useRef<HTMLDetailsElement>(null);
  const updateRequest = useRef<{ body: string; key: string } | null>(null);
  function showOptions(section: "cost" | "people") {
    const target = options.current?.querySelector<HTMLDetailsElement>(
      section === "cost"
        ? ":scope > details:first-of-type"
        : ":scope > details:last-of-type",
    );
    if (target) {
      target.open = true;
      target.querySelector("summary")?.focus();
      target.scrollIntoView({ block: "center" });
    }
  }
  const sourceRequest = useRef(0);
  const reloadSources = useCallback(async () => {
    const ticket = ++sourceRequest.current;
    const latest = await api<Source[]>("projects/" + p.id + "/sources");
    if (ticket === sourceRequest.current) setSources(latest);
  }, [p.id]);
  useEffect(() => {
    const sync = () => {
      if (!document.hidden)
        void reloadSources().catch((e) => setErr(e.message));
    };
    sync();
    const timer = window.setInterval(sync, 15000);
    window.addEventListener("focus", sync);
    document.addEventListener("visibilitychange", sync);
    return () => {
      ++sourceRequest.current;
      clearInterval(timer);
      window.removeEventListener("focus", sync);
      document.removeEventListener("visibilitychange", sync);
    };
  }, [reloadSources, p.planVersion, uploadedPins]);
  useEffect(() => {
    void api<Preference[]>("preferences")
      .then(setPrefs)
      .catch((e) => setErr(e.message));
  }, [p.id]);
  const base = "projects/" + p.id,
    available = [...agents, ...(p.sharedAgents || [])].filter(
      (a) => a.publishedReady,
    );
  const reviewScope = p.artifacts
    .filter((a) => !a.reviewNotesReadable)
    .map((a) => a.id)
    .sort()
    .join(",");
  const calculationInputs = p.plan?.nodes.some(
    (node) => node.operation === "aggregate",
  )
    ? p.plan.sources.map((pin) => {
        const file = sources.find(
          (source) =>
            source.id === pin.id &&
            source.version === pin.version &&
            !source.revoked,
        );
        return { ...pin, file };
      })
    : [];
  const invalidCalculationInput = calculationInputs.some(
    ({ file }) => file && !/\.csv$/i.test(file.name),
  );
  const pictureSteps = p.plan?.nodes.some(node => node.vision || node.editsImage);
  const missingPicture = pictureSteps && !p.plan?.sources.some(pin =>
    pin.picture && !sources.some(source => source.id === pin.id && source.revoked),
  );
  const pendingPlanning =
    p.suggestion?.baseRevision === p.revision &&
    ["unknown", "submitted"].includes(p.suggestion.status);
  async function perform(fn: () => Promise<unknown>) {
    setBusy(true);
    setErr("");
    try {
      await fn();
      await refresh();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const pin = p.planningModel;
  const choices = models
    .filter((m) => m.operation === "text")
    .map((m) => ({ ...m, value: m.id }));
  const current = choices.find(
    (m) => m.id === pin?.id && m.revision === pin?.revision,
  );
  const defaultChoice =
    current?.value || (pin?.enabled ? pin.id + "@" + pin.revision : "");
  if (pin?.enabled && !current)
    choices.unshift({
      ...pin,
      operation: "text",
      value: defaultChoice,
      budgetBlocked: false,
      team: "",
      model: "",
    });
  const previousFormats = p.suggestion?.requiredOutputs ?? p.previousPlanningOutputs ??
    [p.suggestion?.requiredOutput ?? p.previousPlanningOutput].filter((f): f is string => !!f);
  return (
    <div className="thread" ref={threadRef}>
      <h1>{p.title}</h1>
      {p.distributionCases.length > 0 && (
        <DistributionReview project={p} refresh={refresh} />
      )}
      {p.recoveryUntil && (
        <p role="status">
          <strong>临时接管</strong> · 项目访问截至{" "}
          {new Date(p.recoveryUntil).toLocaleString()}
          ，交付或职责转交后提前结束。
        </p>
      )}
      {p.handoffs.some(
        (h) => h.state === "pending" && h.successor === user.id,
      ) && (
        <button onClick={() => showOptions("people")}>
          处理待接受的职责转交
        </button>
      )}
      {(p.budget.warning || p.budget.pending > 0) && (
        <button onClick={() => showOptions("cost")}>
          {p.budget.pending ? "查看待处理的预算申请" : "查看预算提醒"}
        </button>
      )}
      {p.messages.map((m) => (
        <article
          key={m.seq}
          className={m.kind === "user" ? "bubble user" : "bubble"}
        >
          {m.body}
        </article>
      ))}
      {p.stage === "adopted" && (
        <p role="status">
          <strong>已采纳回答</strong>
          {p.adoption && (
            <small>
              {p.adoption.actor} ·{" "}
              <time dateTime={p.adoption.at}>{new Date(p.adoption.at).toLocaleString("zh-CN")}</time>
            </small>
          )}
        </p>
      )}
      {p.workflowTemplate && (
        <>
          <p>采用协作模板 <strong>{p.workflowTemplate.name} · v{p.workflowTemplate.version}</strong>{!p.plan && "。请附本次资料并确认执行 Agent。"}</p>
          <WorkflowTemplateVersionReview
            key={p.workflowTemplate.version}
            project={p}
            owner={p.owner === user.id}
            hasDraft={!!planDraft}
            busy={busy}
            refresh={refresh}
            prepare={(update) => {
              changePlanDraft(planFromTemplateUpdate(p, update));
              requestAnimationFrame(() => planSection.current?.scrollIntoView({ block: "start" }));
            }}
            openTemplate={openWorkflowTemplates}
          />
        </>
      )}
      {p.stage === "brief" && p.owner === user.id && !p.workflowTemplate && (
        <ServiceRecommendations
          project={p.id}
          goal={p.messages.find((m) => m.kind === "user")?.body || p.title}
        />
      )}
      {["brief", "plan"].includes(p.stage) &&
        p.owner === user.id &&
        (choices.length > 0 || !!pin) && (
          <details className="card" open={!p.workflowTemplate && uploadedPins?.length && !p.plan && !p.suggestion ? true : undefined}>
            <summary>根据对话推荐方案或直接回答</summary>
            <form
              key={p.suggestion?.id || p.revision}
              onSubmit={(e) => {
                e.preventDefault();
                const form = new FormData(e.currentTarget);
                const pins = selectedPins(form);
                const formats = form.getAll("requiredFormat").map(String).sort();
                const scope = formats.join(",");
                const m = choices.find((v) => v.value === form.get("resource"));
                if (!m) return;
                const previous = p.suggestion;
                const sameModel =
                  previous?.baseRevision === p.revision &&
                  previous.resource === m.id &&
                  previous.resourceRevision === m.revision;
                const sameInputs = JSON.stringify(previous?.inputSources) === JSON.stringify(pins) && [...previousFormats].sort().join(",") === scope;
                const baseKey = [
                  "planning",
                  p.id,
                  p.revision,
                  m.id,
                  m.revision,
                ].join("-");
                void perform(async () => {
                  const scopedKey = await planningRequestKey(scope ? baseKey + "-" + scope : baseKey, pins);
                  const key =
                    sameModel &&
                    previous &&
                    (previous.status === "stale" || previous.status === "invalid")
                      ? scopedKey + "-" + previous.id
                      : sameModel && sameInputs && previous?.requestKey
                        ? previous.requestKey
                        : scopedKey;
                  await api(
                    base + "/suggestions",
                    "POST",
                    {
                      resource: m.id,
                      revision: m.revision,
                      expected: p.revision,
                      authorizeOneCall: true,
                      sources: pins,
                      ...(formats.length > 1 ? {requiredOutputs: formats} : {requiredOutput: formats[0] || ""}),
                    },
                    {
                      "Idempotency-Key": key,
                    },
                  );
                  planningSubmitted(uploadedPins);
                });
              }}
            >
              <label>
                规划助手模型
                <select
                  name="resource"
                  required
                  defaultValue={pin ? defaultChoice : choices[0]?.value}
                >
                  {pin && !pin.enabled && (
                    <option value="">
                      原默认模型不可用，请选择其他已授权模型
                    </option>
                  )}
                  {choices.map((m) => (
                    <option
                      value={m.value}
                      key={m.value}
                      disabled={m.credentialBlocked}
                    >
                      {m.name} v{m.revision}
                      {m.credentialBlocked && " · 团队凭据待修复"}
                      {m.budgetBlocked && " · 资源额度不足"}
                    </option>
                  ))}
                </select>
              </label>
              <PlanningInputs files={sources} selected={[...(p.suggestion?.inputSources ?? p.plan?.sources ?? p.previousPlanningSources ?? []), ...(uploadedPins ?? [])]} />
              <details>
                <summary>指定交付文件格式</summary>
                <fieldset>
                  <legend>需要交付的文件</legend>
                  {Object.entries(fileFormats).map(([value, label]) => (
                    <label key={value}>
                      <input type="checkbox" name="requiredFormat" value={value}
                        defaultChecked={previousFormats.includes(value)} /> {label}
                    </label>
                  ))}
                </fieldset>
                <small>可同时选择多种。留空时由助手根据需求规划。</small>
              </details>
              <label>
                <input type="checkbox" required />
                授权一次规划调用，实际费用由团队承担。
              </label>
              <button disabled={busy || pendingPlanning}>请助手规划</button>
            </form>
          </details>
        )}
      {p.suggestion?.status === "ready" &&
        p.suggestion.result &&
        ["brief", "plan"].includes(p.stage) && (
          <section className="card planning-suggestion">
            {p.suggestion.result.kind === "answer" &&
              p.reviewer === user.id && (
                <button
                  onClick={() =>
                    perform(() =>
                      api(base + "/adoption", "POST", {
                        request: p.suggestion!.id,
                        expected: p.revision,
                      }),
                    )
                  }
                >
                  明确采纳这条回答
                </button>
              )}
            {p.suggestion.result.kind === "plan" && (
              <SuggestedPlan
                project={p}
                sources={sources}
                agents={agents}
                models={models}
                user={user}
                busy={busy}
                perform={perform}
                reloadCatalog={reloadCatalog}
                openLibrary={openLibrary}
              />
            )}
          </section>
        )}
      {p.suggestion && p.suggestion.status !== "ready" && (
        <p role="status">
          {p.suggestion.status === "stale"
            ? "规划期间项目、授权或候选条件已变化。核对当前条件后，可重新授权规划。"
            : p.suggestion.status === "invalid"
              ? "本次答复未满足规划要求。可调整描述，或重新授权规划。"
              : "规划调用结果尚待核对，可刷新原调用状态。"}
          {pendingPlanning && (
            <button
              type="button"
              onClick={() => void refresh().catch((e) => setErr(e.message))}
            >
              刷新原调用状态
            </button>
          )}
        </p>
      )}
      <Sources
        reviewScope={reviewScope}
        project={p.id}
        items={sources}
        pins={
          planDraft?.documents ??
          Object.fromEntries(
            (p.plan?.sources ?? []).map((s) => [s.id, s.version]),
          )
        }
        pin={
          !busy && p.owner === user.id && ["brief", "plan"].includes(p.stage)
            ? (id, version) => {
                const base = planDraft ?? currentPlan(p);
                changePlanDraft({
                  ...base,
                  documents: { ...base.documents, [id]: version },
                });
              }
            : undefined
        }
        reload={async () => {
          await Promise.all([reloadSources(), refresh()]);
        }}
      />
      {(["brief", "plan"].includes(p.stage) || planDraft) &&
        p.owner === user.id && (
          <details className="card" ref={planSection} open={p.stage === "brief" || !!planDraft}>
            <summary>拟定协作方案</summary>
            <PlanEditor
              draft={planDraft}
              change={changePlanDraft}
              project={p}
              agents={available}
              sources={sources}
              preferences={prefs}
              busy={busy}
              openLibrary={openLibrary}
              save={(body) =>
                perform(async () => {
                  try {
                    if (planDraft?.templateUpdate) {
                      const { fromVersion, toVersion } = planDraft.templateUpdate;
                      const request = { fromVersion, toVersion, plan: body };
                      const serialized = JSON.stringify(request);
                      if (updateRequest.current?.body !== serialized) {
                        updateRequest.current = { body: serialized, key: crypto.randomUUID() };
                      }
                      await api(base + "/workflow-template-update", "POST", request, { "Idempotency-Key": updateRequest.current.key });
                    } else {
                      await api(base + "/plan", "POST", body);
                    }
                  } catch (e) {
                    if (e instanceof RequestError && e.status === 409)
                      await refresh();
                    throw e;
                  }
                  if (planDraft) changePlanDraft(undefined, planDraft);
                })
              }
            />
          </details>
        )}
      {p.plan && (
        <section className="card">
          <h2>协作方案 v{p.planVersion}</h2>
          <p>{p.plan.brief}</p>
          {calculationInputs.length > 0 && (
            <details>
              <summary>汇总输入 · {calculationInputs.length} 份资料</summary>
              <ul>
                {calculationInputs.map(({ id, version, file }) => (
                  <li key={id}>
                    <a
                      href={
                        "/api/projects/" +
                        p.id +
                        "/sources/" +
                        id +
                        "/content?version=" +
                        version
                      }
                    >
                      {file?.name || "核对所选原件"} · v{version}
                    </a>
                  </li>
                ))}
              </ul>
            </details>
          )}
          {p.stage === "plan" && invalidCalculationInput && (
            <p role="alert">汇总步骤需要 CSV。请在方案中更换资料并保存。</p>
          )}
          {p.stage === "plan" && missingPicture && (
            <p role="status">请在方案中引用至少一张可用的 PNG 或 JPEG 图片。</p>
          )}
          {(p.plan.rules?.length ?? 0) > 0 && (
            <details>
              <summary>方案采用的团队规则 · {p.plan.rules.length}</summary>
              <ul>
                {p.plan.rules.map((rule, i) => (
                  <li key={i}>
                    <strong>v{rule.revision}</strong> ·{" "}
                    {rule.available
                      ? rule.body
                      : "规则已到期、停用或当前无权查看"}
                  </li>
                ))}
              </ul>
            </details>
          )}
          <p>
            最终确认人 ·{" "}
            <strong>
              {p.participants.find((m) => m.id === p.reviewer)?.name ||
                "需核对项目成员"}
            </strong>
          </p>
          <ol>
            {p.plan.nodes.map((n, i) => {
              const chosen = n.toolChoice?.selected === 2 ? n.toolChoice : n;
              return (
              <li key={i}>
                {n.toolChoice ? <strong>汇总报表</strong> : (
                  <>{n.name} · v{n.revision}{n.lookupPhase === "search" ? " · 检索依据" : n.lookupPhase === "answer_calc" ? " · 引文金额核算" : n.lookupPhase === "answer" ? " · 据原文回答" : ""}</>
                )}
                <small>
                  {n.dependsOn.length
                    ? "引用步骤 " + n.dependsOn.join("、")
                    : "可独立开始"}
                </small>
                <small>
                  {n.lookupPhase === "answer_calc"
                    ? "从引文选择 2 至 6 项同币种金额求和，或用两项算差额。金额最多两位小数，交付包含算式与原文。"
                    : n.vision ? "读取本次引用的图片，提交可审核的文本。"
                      : n.editsImage ? "使用方案中的原件作为参考图，提交 PNG。"
                        : outputOperations[n.operation] || "成果操作范围待核实"}
                </small>
                {n.toolChoice && (
                  <small>
                    自动选择 · {n.name} v{n.revision} 或 {n.toolChoice.name} v{n.toolChoice.revision}
                  </small>
                )}
                {n.toolChoice?.selected === 0 && <strong>待人工选择</strong>}
                {!!n.toolChoice?.selected && (
                  <strong>{n.toolChoice.manual ? "人工选定 " : "本次使用 "}{chosen.name} v{chosen.revision}</strong>
                )}
                {n.toolChoice && (
                  <details>
                    <summary>查看两项汇总口径</summary>
                    <p>{n.name} v{n.revision} · <AggregationScope config={n.toolChoice.primaryConfig} /></p>
                    <p>{n.toolChoice.name} v{n.toolChoice.revision} · <AggregationScope config={n.toolChoice.config} /></p>
                  </details>
                )}
                {p.run?.status === "blocked" && p.run.canResolveToolChoice && n.toolChoice?.selected === 0 && (
                  <div className="actions">
                    {[n.name, n.toolChoice.name].map((name, index) => (
                      <button
                        type="button"
                        key={index}
                        disabled={busy}
                        onClick={() => {
                          const run = p.run!;
                          const choice = index + 1;
                          const step = i + 1;
                          void perform(() => api("runs/" + run.id + "/tool-choice", "POST", {
                            step, choice, expected: run.controlRevision,
                          }, { "Idempotency-Key": ["tool-choice", run.id, run.controlRevision, step, choice].join("/") }));
                        }}
                      >
                        选择{name}并继续
                      </button>
                    ))}
                  </div>
                )}
                {p.stage === "blocked" &&
                  agents.some((a) => a.id === chosen.agent) && (
                    <AgentChecks
                      agent={chosen.agent}
                      revision={chosen.revision}
                      refresh={async () => {
                        await reloadCatalog();
                        await refresh();
                      }}
                      project={p.id}
                      pictureInput={n.vision || n.editsImage}
                      pinned
                    />
                  )}
              </li>
            );
            })}
          </ol>
          {p.stage === "plan" && p.owner === user.id && planDraft?.templateUpdate && <p role="status">请保存或丢弃正在编辑的新版方案，再确认执行。</p>}
          {p.stage === "plan" && p.owner === user.id && !planDraft?.templateUpdate && (
            <form
              key={p.revision + ":" + JSON.stringify(p.plan.quote)}
              onSubmit={(e) => {
                e.preventDefault();
                if (missingPicture) return;
                const fields = new FormData(e.currentTarget);
                void perform(() =>
                  api(
                    base + "/runs",
                    "POST",
                    {
                      expected: p.revision,
                      authorizeCalls: p.plan!.nodes.length,
                      ...(fields.has("awaitReceipt") ? { awaitCalculationReceipt: true } : {}),
                      ...(fields.has("awaitLookup") ? { awaitLookupReceipt: true } : {}),
                    },
                    { "Idempotency-Key": "plan-" + p.id + "-" + p.planVersion },
                  ),
                );
              }}
            >
              {p.plan.nodes.some(n=>n.lookupPhase==="answer_calc") && <label><input type="checkbox" name="awaitReceipt" defaultChecked/>缺少金额时等待我补件</label>}
              {p.plan.nodes.some((n) => n.lookupPhase === "answer") && (
                <label><input type="checkbox" name="awaitLookup" />找不到原文时等待我补件</label>
              )}

              <p>
                预计费用 ·{" "}
                {p.plan.quote.priced > 0 &&
                  `已估价 ${p.plan.quote.priced} 次，共 ¥${p.plan.quote.known}`}
                {p.plan.quote.unknown > 0 &&
                  `${p.plan.quote.priced ? "，另有" : ""}${p.plan.quote.unknown} 次金额未知`}
              </p>
              {(p.plan.quote.projectShort ||
                p.plan.quote.teamShort ||
                p.plan.quote.resourceShort) && (
                <p role="status">
                  <strong>
                    {[
                      p.plan.quote.projectShort && "项目",
                      p.plan.quote.teamShort && "承担团队",
                      p.plan.quote.resourceShort && "资源",
                    ]
                      .filter(Boolean)
                      .join("及")}
                    预算不足。
                  </strong>
                  可以先调整方案或追加额度。继续后，额度不足的调用会暂停。
                  <button type="button" onClick={() => showOptions("cost")}>
                    查看项目费用
                  </button>
                </p>
              )}
              {p.plan.quote.grantShort && (
                <p role="status">
                  <strong>跨团队能力授权不足。</strong>{" "}
                  请核对所选修订的有效期和剩余次数。运行时会在缺少授权的步骤暂停。
                  <button type="button" onClick={() => showOptions("people")}>
                    查看项目授权
                  </button>
                </p>
              )}
              {planDraft && (
                <p role="status">
                  本次执行采用已保存的方案 v{p.planVersion}。本页草稿尚未提交。
                </p>
              )}
              <label>
                <input type="checkbox" required />
                授权本次 {p.plan.nodes.length}{" "}
                次能力调用{p.plan.nodes.some(n=>n.lookupPhase) && "（上限，跳过的模型调用不计费）"}，费用由团队承担，按实际账单核对。
                {!!p.plan.quote.unknown &&
                  "本次授权包含上述未知费用，实际金额可能超过预估。"}
                {pictureSteps && "引用的图片原件将发给所选的视觉模型。"}
                {p.plan.quote.remote &&
                  "外部 Agent 返回任务编号时，平台会查询该任务，并在取消时向远端申请停止。"}
              </label>
              <button
                className="primary"
                disabled={busy || invalidCalculationInput || missingPicture}
              >
                确认并执行
              </button>
            </form>
          )}
        </section>
      )}
      {p.run && (
        <Diagnostics
          project={p}
          owner={p.owner === user.id}
          refresh={refresh}
        />
      )}
      {p.run && (
        <details
          className="card"
          open={
            p.run.attempts.some((a) => a.callReview?.inputIssues?.length || a.callReview?.inputIssue) || undefined
          }
        >
          <summary>
            {statusLabel[p.run.status] || p.run.status} · 调用记录{" "}
            {p.run.attempts.reduce(
              (total, step) => total + (step.callReview?.attempt ?? 1),
              0,
            )}{" "}
            项
          </summary>
          {p.run.attempts.map((a) => (
            <p key={a.step}>
              步骤 {a.step}
              {a.callReview &&
                a.callReview.attempt > 1 &&
                " · 第 " + a.callReview.attempt + " 次"}{" "}
              · {statusLabel[a.status] || a.status} ·{" "}
              {p.canViewCosts &&
                (a.tokens === null ? "用量待核对" : a.tokens + " token")}{" "}
              {a.diagnostic}
            </p>
          ))}
          {p.run.attempts.map((a) => (
            <CallRecovery
              project={p.id}
              key={
                a.step +
                ":" +
                a.callReview?.attempt +
                ":" +
                p.run!.controlRevision
              }
              record={a.callReview}
              run={p.run!.id}
              state={p.run!.status}
              revision={p.run!.controlRevision}
              canReview={p.owner === user.id}
              model={p.planningModel}
              busy={busy}
              perform={perform}
            />
          ))}
          {p.run.attempts.map(
            (a) =>
              a.remote && (
                <RemoteReceipt
                  key={a.step}
                  remote={a.remote}
                  run={p.run!.id}
                  revision={p.run!.controlRevision}
                  canCheck={p.owner === user.id}
                  perform={perform}
                />
              ),
          )}
        </details>
      )}
      {p.run?.lookupPending && (
        <p className="card" role="status">正在对照原文补查依据。</p>
      )}
      {p.run?.indexWait && (
        <section className="card" aria-label="资料索引进度">
          <p role="status">{p.run.indexWait.automatic
            ? "正在等待资料索引。选词已保存，索引就绪后将在本任务继续。"
            : "自动等待已结束。选词已保存，可以在本任务重新核对原资料。"}</p>
          {p.owner === user.id && (
            <button disabled={busy} onClick={() => void perform(() => api(
              "runs/" + p.run!.id + "/control", "POST",
              { action: "resume", reason: "重新核对原资料索引", expected: p.run!.controlRevision },
              { "Idempotency-Key": crypto.randomUUID() },
            ))}>重新核对原资料</button>
          )}
        </section>
      )}
      {p.run?.receiptRequest && p.owner===user.id && <SourceReceipt key={p.run.id+":"+p.run.controlRevision} project={p} sources={sources} busy={busy} perform={perform} reloadSources={reloadSources}/>}
      {p.run &&
        p.owner === user.id &&
        ["queued", "running", "paused", "blocked", "reconcile"].includes(
          p.run.status,
        ) && (
          <details className="card">
            <summary>控制运行</summary>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const f = new FormData(e.currentTarget);
                void perform(() =>
                  api(
                    "runs/" + p.run!.id + "/control",
                    "POST",
                    {
                      action: f.get("action"),
                      reason: f.get("reason"),
                      expected: p.run!.controlRevision,
                    },
                    { "Idempotency-Key": crypto.randomUUID() },
                  ),
                );
              }}
            >
              <label>
                动作
                <select name="action">
                  {["queued", "running"].includes(p.run.status) && (
                    <option value="pause">暂停后续执行</option>
                  )}
                  {!p.run.indexWait && !p.run.receiptRequest && !p.plan?.nodes.some(n=>n.toolChoice?.selected===0) && ["paused", "blocked"].includes(p.run.status) && (
                    <option value="resume">复查条件并继续</option>
                  )}
                  <option value="cancel">取消后续执行</option>
                  {p.run.canConcludeFailure && (
                    <option value="confirm_failure">
                      按已核对回执确认执行失败
                    </option>
                  )}
                </select>
              </label>
              <label>
                原因
                <input name="reason" required />
              </label>
              <small>已发送的外部调用仍需记录结果和费用。</small>
              <button disabled={busy}>确认控制动作</button>
            </form>
          </details>
        )}
      {p.stage === "review" && p.artifacts.length > 0 && (
        <ProjectFeedback
          p={p}
          drafts={projectFeedbackDrafts}
          busy={busy}
          perform={perform}
        />
      )}
      {p.artifacts.map((a) => (
        <article
          className="card"
          key={a.id}
          tabIndex={-1}
          id={
            p.stage === "review" &&
            p.canReview &&
            (!a.approved ||
              hasSourceReview(a) ||
              (p.takeoverAssignment && a.needsTakeoverReview)) &&
            !a.feedback.some(blocksApproval)
              ? "review-" + a.id + "-" + a.version
              : undefined
          }
        >
          <h2>
            {a.name} · v{a.version}
          </h2>
          {p.plan?.nodes[a.ordinal - 1]?.lookupPhase
            ? <LookupBody artifact={a} project={p} sources={sources} />
            : <div className="preserve">{a.body}</div>}
          {a.file && canPreview(a.file) && (
            <FilePreview
              key={a.version}
              url={
                "/api/" +
                base +
                "/artifacts/" +
                a.id +
                "/file?version=" +
                a.version
              }
              name={a.file}
            />
          )}
          {a.file && (
            <a
              download
              href={
                "/api/" +
                base +
                "/artifacts/" +
                a.id +
                "/file?version=" +
                a.version
              }
            >
              {a.file} · 下载此版本
            </a>
          )}
          <small
            className={
              a.approved && !hasSourceReview(a)
                ? "review-state"
                : "review-state pending"
            }
          >
            {a.approved ? "已审核" : "待审核"}
            {p.stage === "review" && hasSourceReview(a) && " · 资料影响待核对"}
            {a.needsTakeoverReview && " · 待接管人核对当前版本"}
          </small>
          {a.feedback.some(blocksApproval) && (
            <small>反馈待复核，交付暂缓</small>
          )}
          <SourceReview p={p} a={a} busy={busy} perform={perform} />
          <ArtifactHistory
            key={
              a.version +
              ":" +
              a.approved +
              ":" +
              reviewScope +
              ":" +
              p.canReview +
              ":" +
              p.distributionCases
                .filter((c) => c.artifact === a.id)
                .map(
                  (c) =>
                    c.source +
                    "-" +
                    c.version +
                    "-" +
                    c.revision +
                    "-" +
                    c.canManage,
                )
                .join(",")
            }
            project={p.id}
            artifact={a.id}
          />
          <Feedback
            sources={sources}
            drafts={feedbackDrafts}
            artifact={a}
            project={p}
            user={user}
            busy={busy}
            perform={perform}
          />
          <ArtifactEditor
            project={p}
            artifact={a}
            draft={revisions?.[a.id]}
            update={(next, expected) => changeRevision(a.id, next, expected)}
            busy={busy}
            perform={perform}
            refresh={refresh}
          />
          {p.stage === "review" && p.canReview && (
            <div className="actions">
              {(!a.approved ||
                (p.takeoverAssignment && a.needsTakeoverReview)) &&
                !a.feedback.some(blocksApproval) &&
                !hasSourceReview(a) && (
                  <button
                    disabled={busy}
                    onClick={() =>
                      perform(() =>
                        api(base + "/artifacts/" + a.id + "/review", "POST", {
                          version: a.version,
                          assignment: p.takeoverAssignment || undefined,
                        }),
                      )
                    }
                  >
                    {p.takeoverAssignment && a.needsTakeoverReview
                      ? "接管人审核此版本"
                      : "审核此版本"}
                  </button>
                )}
            </div>
          )}
        </article>
      ))}
      {p.reviewer === user.id &&
        p.stage === "review" &&
        p.artifacts.length > 0 &&
        !p.distributionCases.some((c) => c.current && held(c.state)) &&
        p.artifacts.every(
          (a) =>
            a.approved &&
            !a.needsTakeoverReview &&
            !hasSourceReview(a) &&
            !a.feedback.some(blocksApproval),
        ) && (
          <DeliveryConfirmation
            project={p}
            busy={busy}
            hasDraft={!!revisions && Object.keys(revisions).length > 0}
            confirm={(handoff) =>
              perform(async () => {
                const request = { expected: p.revision, handoff };
                if (!p.recoveryUntil) {
                  await api(base + "/deliver", "POST", request);
                  return;
                }
                const res = await fetch(
                  "/api/" + base + "/deliver?download=1",
                  {
                    method: "POST",
                    headers: { "Content-Type": "application/json" },
                    body: JSON.stringify(request),
                  },
                );
                if (!res.ok) {
                  const problem = await res.json();
                  throw new Error(problem.error || "交付失败");
                }
                const id = res.headers.get("X-Delivery-ID")!;
                const url = URL.createObjectURL(await res.blob()),
                  link = document.createElement("a");
                link.href = url;
                link.download = "delivery-" + id + ".zip";
                link.click();
                setTimeout(() => URL.revokeObjectURL(url), 1000);
                onRecoveryDelivered(id);
              })
            }
          />
        )}
      {p.deliveries.length > 0 && (
        <DeliveryHistory
          project={p}
          base={base}
          busy={busy}
          revise={(data) =>
            void perform(() => api(base + "/delivery/revisions", "POST", data))
          }
        />
      )}
      {p.stage === "delivered" && p.owner === user.id && p.plan && (
        <SaveWorkflowTemplate project={p} openTemplates={openWorkflowTemplates} />
      )}
      <section
        className="project-options"
        aria-label="费用与协作"
        ref={options}
      >
        <ProjectCharges draft={budgetDraft} project={p} refresh={refresh} />
        <Participants project={p} user={user} busy={busy} perform={perform}>
          <ProjectAgentGrants
            project={p}
            agents={agents}
            refreshProject={refresh}
          />
          {p.owner === user.id && (
            <ReviewPolicy key={p.id} scope={"projects/" + p.id} />
          )}
        </Participants>
      </section>
      {err && <p role="alert">{err}</p>}
    </div>
  );
}

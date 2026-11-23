import { RequestRelease } from "./agent-release";
import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { capabilityMatcher, normalizeCapabilityText } from "./capability-match";
import { api, type Agent, type Model } from "./client";
import type { AgentTeam } from "./agent-team";
import { fileFormat, fileFormats, formatSearchAliases, outputOperations } from "./output-operations";
import { SimilarAgents } from "./similar-agents";
import { DraftInstructions } from "./draft-instructions";
import { DraftTrial } from "./draft-trial";
import { AgentChecks } from "./agent-checks";

import courseWriter from "./course-writer.json";

import {
  AggregateColumns,
  describeTool,
  suggestedTool,
  toolChoices,
  type ToolChoice,
} from "./tool-creation";

type Output = Model["operation"] | ToolChoice;
function isToolOutput(value: Output): value is ToolChoice {
  return value in toolChoices;
}
const modelLabels: Record<Model["operation"], string> = {
  text: "文本模型", png: "生图模型", vision: "视觉理解模型", "image-edit": "参考图编辑模型",
};

function ConfigurationFields({ missing, title, children }: {
  missing: boolean; title: string; children: ReactNode;
}) {
  const panel = useRef<HTMLDetailsElement>(null);
  useLayoutEffect(() => {
    if (missing && panel.current) panel.current.open = true;
  }, [missing]);
  return <details ref={panel}><summary>{title}</summary>{children}</details>;
}

export function AgentPicker({
  agents,
  models,
  teams,
  team,
  selection,
  goal,
  files,
  memberEmail,
  choose,
  manage,
  openTemplates,
  openWorkflowTemplates,
  configure,
  refresh,
  admin,
}: {
  agents: Agent[];
  models: Model[];
  teams: AgentTeam[];
  team: string;
  selection: Agent[];
  goal: string;
  memberEmail: string;
  files: File[];
  choose: (agents: Agent[], projectTeam: string, goalIfEmpty?: string) => void;
  manage: () => void;
  openTemplates: () => void;
  openWorkflowTemplates: () => void;
  configure: () => void;
  refresh: () => Promise<void>;
  admin: boolean;
}) {
  const [mode, setMode] = useState<"find" | "create">("find");
  const [combining, setCombining] = useState(selection.length > 1);
  const [steps, setSteps] = useState(selection);
  const [combinationBrief, setCombinationBrief] = useState("");
  const [destination, setDestination] = useState(team);
  const mixedTeams = new Set(steps.map((a) => a.team)).size > 1;
  const projectTeam = mixedTeams ? destination : steps[0]?.team;
  const destinationAvailable = teams.some((t) => t.id === projectTeam);
  const [search, setSearch] = useState("");
  const [draftDescription, describe] = useState<string | null>(null);
  const [formGeneration, restartForm] = useState(0);
  const description = draftDescription ?? "";
  const [draftName, setDraftName] = useState("");
  const name =
    draftName.trim() ||
    Array.from(description.trim().split("\n")[0]).slice(0, 28).join("");
  const [operation, setOperation] = useState<Output>("text");
  const [modelId, setModel] = useState("");
  const [draft, setDraft] = useState<{
    id: string;
    revision: number;
    name: string;
    operation: Output;
    team: string;
    payer: string;
    estimate?: string | null;
  } | null>(null);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const [toolTeam, setToolTeam] = useState("");
  const ownerID = toolTeam || team || (teams.length === 1 ? teams[0].id : "");
  const toolOwner = teams.find((t) => t.id === ownerID);
  const tool = isToolOutput(operation);
  const modelOperation = operation as Model["operation"];
  const outline = operation === "pptx";
  const availableModels = models.filter(
    (m) => !m.credentialBlocked && teams.some((t) => t.id === m.team),
  );
  const eligible = availableModels.filter((m) => m.operation === operation);
  function selectOutput(next: Output) {
    setOperation(next);
    setModel("");
  }
  // Resolve the suggested revision when the catalog arrives. An explicit
  // choice that disappears still requires the member to choose again.
  const modelKey = (m: Model) => m.id + "/" + m.revision;
  const localModels = eligible.filter(m => m.team === team);
  const current = modelId
    ? eligible.find(m => modelKey(m) === modelId)
    : localModels.length === 1 ? localModels[0] : eligible.length === 1 ? eligible[0] : undefined;
  const currentDraft = draft && agents.find((a) => a.id === draft.id);
  const verified =
    currentDraft &&
    currentDraft.revision === draft?.revision &&
    currentDraft.validated
      ? currentDraft
      : undefined;
  function usePublished(agent: Agent, brief: string) {
    if (combining) {
      if (steps.length >= 12) return;
      if (steps.length === 0) setCombinationBrief(brief);
      setSteps([...steps, agent]);
      setMode("find");
    } else choose([agent], agent.team, brief);
  }
  async function act(fn: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  const needle = normalizeCapabilityText(search);
  const requestedFormat = formatSearchAliases.get(needle);
  const available = agents.filter(
    (a) =>
      a.publishedReady && a.published > 0 && teams.some((t) => t.id === a.team),
  );
  const drafts = agents.filter(
    (a) =>
      (a.imageDraft || a.outlineDraft || a.quickDraft) &&
      teams.some((t) => t.id === a.team),
  );
  const match = useMemo(() => capabilityMatcher(needle || goal), [needle, goal]);
  const ready = available
    .filter(
      (agent) => !requestedFormat || fileFormat(agent.publishedOperation) === requestedFormat,
    )
    .map((agent) => {
      const title = agent.publishedName || "";
      const instructions = agent.publishedInstructions || "";
      const op = agent.publishedOperation || "";
      const labels = [
        outputOperations[op]?.split(" · ")[0],
        fileFormats[fileFormat(op) || ""],
        toolChoices[op as ToolChoice],
      ].filter(Boolean).join(" ");
      const fields = [title, instructions, labels, agent.teamName || ""];
      const direct = !!needle && fields.some((field) => normalizeCapabilityText(field).includes(needle));
      const exactTitle = !!needle && normalizeCapabilityText(title) === needle;
      const { words, score } = match(title, [instructions, labels].join(" "));
      const brief = needle && !exactTitle && needle !== normalizeCapabilityText(agent.teamName || "") &&
        words.length >= 2 ? search.trim() : "";
      return { agent, words, score, exactTitle, direct, brief };
    })
    .filter((result) => requestedFormat || !needle || result.direct || result.score > 0)
    .sort((a, b) =>
      Number(b.exactTitle) - Number(a.exactTitle) ||
      Number(b.direct) - Number(a.direct) ||
      b.words.length - a.words.length ||
      b.score - a.score,
    );
  const singleTeam = teams.length === 1 ? teams[0] : null;
  const isCurrent = (step: Agent) =>
    available.some((a) => a.id === step.id && a.published === step.published);
  return (
    <section className="agent-picker">
      {mode === "find" && (
        <>
          <div className="actions">
            <input
              type="search"
              aria-label="搜索 Agent"
              placeholder="搜索名称或任务"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <button
              type="button"
              className="primary"
              title={draftDescription !== null && !draft ? "按当前需求重新创建，替换未提交的配置" : undefined}
              onClick={() => {
                const request = requestedFormat ? goal.trim() : search.trim() || goal.trim();
                const suggested = suggestedTool(request);
                const output = requestedFormat === "xlsx" && suggested === "aggregate"
                  ? "aggregate" : requestedFormat || suggested;
                setDraft(null);
                setDraftName("");
                setToolTeam("");
                restartForm(n => n + 1);
                describe(request);
                selectOutput(
                  output ??
                    (availableModels.length && availableModels.every((m) => m.operation === "png")
                      ? "png" : "text"),
                );
                setMode("create");
                setError("");
              }}
            >
              创建 Agent
            </button>
          </div>
          <div className="actions agent-picker-actions">
            <button
              type="button"
              aria-pressed={combining}
              onClick={() => setCombining(!combining)}
            >
              {combining ? "单独使用" : "组合 Agent"}
            </button>
            <button type="button" onClick={openTemplates}>内置 Agent 模板</button>
            <button type="button" onClick={openWorkflowTemplates}>协作模板</button>
            {draftDescription !== null && !draft && (
              <button type="button" onClick={() => setMode("create")}>继续未提交的配置</button>
            )}
          </div>
          {combining && (
            <section className="agent-sequence" aria-label="协作顺序">
              <h3>协作顺序</h3>
              <p>后一步引用前一步的文本结果。图片和文档原件可在审核时打开。</p>
              {steps.length > 0 && (
                <>
                  <ol>
                    {steps.map((step, i) => (
                      <li key={i}>
                        <strong>{step.publishedName || step.name}</strong> · v
                        {step.published}
                        {!isCurrent(step) ? (
                          <small>此版本已不可选，请移除或重新选择。</small>
                        ) : (
                          mixedTeams && <small>{step.teamName}承担费用</small>
                        )}
                        <div className="actions">
                          {i > 0 && (
                            <button
                              type="button"
                              aria-label={`将第 ${i + 1} 步提前`}
                              onClick={() => {
                                const next = [...steps];
                                [next[i - 1], next[i]] = [next[i], next[i - 1]];
                                setSteps(next);
                              }}
                            >
                              提前
                            </button>
                          )}
                          <button
                            type="button"
                            aria-label={`移除第 ${i + 1} 步`}
                            onClick={() =>
                              setSteps(steps.filter((_, n) => n !== i))
                            }
                          >
                            移除
                          </button>
                        </div>
                      </li>
                    ))}
                  </ol>
                  {mixedTeams ? (
                    <label>
                      项目归属
                      <select
                        value={destinationAvailable ? destination : ""}
                        onChange={(e) => setDestination(e.target.value)}
                      >
                        <option value="">请选择项目归属</option>
                        {teams.map((t) => (
                          <option key={t.id} value={t.id}>
                            {t.name}
                          </option>
                        ))}
                      </select>
                    </label>
                  ) : (
                    <small>本次组合由{steps[0].teamName}承担费用</small>
                  )}
                  <button
                    className="primary"
                    type="button"
                    disabled={!destinationAvailable || !steps.every(isCurrent)}
                    onClick={() => choose(steps, projectTeam, combinationBrief)}
                  >
                    使用此组合
                  </button>
                </>
              )}
              {steps.length === 12 && <small>每份方案最多 12 步。</small>}
            </section>
          )}
          {ready.length === 0 && (
            <p role="status">
              {needle ? "没有匹配的 Agent" : "当前没有已验证并发布的 Agent。"}
            </p>
          )}
          {singleTeam &&
            ready.length > 0 &&
            !(combining && steps.length > 0) && (
              <small>{singleTeam.name}承担本次选用 Agent 的费用</small>
            )}
          {ready.map(({ agent: a, words, brief }) => (
            <button
              className="list agent-choice"
              type="button"
              key={a.id}
              disabled={combining && steps.length >= 12}
              onClick={() => usePublished(a, brief)}
            >
              <strong>{a.publishedName || a.name}</strong> · v{a.published}
              {a.publishedInstructions &&
                a.publishedInstructions !== a.publishedName && (
                  <span>{a.publishedInstructions}</span>
                )}
              <small>
                {[
                  words.length ? (needle ? "匹配搜索词「" : "匹配需求中的「") + words.slice(0, 2).join("、") + "」" : "",
                  a.publishedOperation &&
                    (outputOperations[a.publishedOperation] || a.publishedOperation).split(" · ")[0],
                  a.publishedVision && "读取图片",
                  a.publishedImageEdit && "根据参考图编辑",
                  !singleTeam && a.teamName + "承担费用",
                ].filter(Boolean).join(" · ")}
              </small>
            </button>
          ))}
          {[
            {
              value: drafts.filter((a) => a.imageDraft),
              label: "我的生图草稿与验收",
            },
            {
              value: drafts.filter((a) => ["text", "vision"].includes(a.quickDraft?.operation || "")),
              label: "我的文本草稿",
            },
            {
              value: drafts.filter((a) => !a.imageDraft && !["text", "vision"].includes(a.quickDraft?.operation || "")),
              label: "我的制作草稿与验收",
            },
          ].map(
            ({ value, label }) =>
              value.length > 0 && (
                <details key={label}>
                  <summary>
                    {label}（{value.length}）
                  </summary>
                  {value.map((a) => (
                    <button
                      type="button"
                      className="list"
                      key={a.id}
                      onClick={() => {
                        setDraft({
                          id: a.id,
                          revision: a.revision,
                          name: a.name,
                          operation:
                            a.quickDraft?.operation ??
                            (a.outlineDraft ? "pptx" : a.imageDraft?.reference ? "image-edit" : "png"),
                          team: a.team,
                          payer: a.teamName || "所属团队",
                          estimate:
                            a.quickDraft?.estimate ??
                            (a.outlineDraft ? "0" : a.imageDraft?.estimate),
                        });
                        setMode("create");
                        setError("");
                      }}
                    >
                      <strong>{a.name}</strong> · v{a.revision}
                      <small>
                        {a.validated ? "已验证，待发布" : "查看样例与验收"}
                      </small>
                    </button>
                  ))}
                </details>
              ),
          )}
          <div className="actions">
            <button type="button" onClick={manage}>
              查看能力库
            </button>
          </div>
        </>
      )}
      {(mode === "create" || (draftDescription !== null && !draft)) && (
        <div key={formGeneration} hidden={mode !== "create"}>
          <button type="button" disabled={busy} onClick={() => setMode("find")}>
            返回 Agent 列表
          </button>
          {!draft ? (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const fields = new FormData(e.currentTarget);
                const owner = tool ? toolOwner?.id : current?.team;
                if (!owner || !description.trim()) return;
                void act(async () => {
                  const receipt = await api<{ id: string; revision: number }>(
                    tool ? "agents/tools" : "agents",
                    "POST",
                    tool
                      ? {
                          team: owner,
                          name,
                          description: description.trim(),
                          tool: describeTool(operation, name, fields),
                        }
                      : {
                          name,
                          instructions: description.trim(),
                          resource: current?.id,
                          resourceRevision: current?.revision,
                        },
                  );
                  setDraft({
                    ...receipt,
                    operation,
                    team: owner,
                    name,
                    payer:
                      teams.find((t) => t.id === owner)?.name || "所属团队",
                    estimate: tool ? "0" : current?.estimate,
                  });
                  await refresh();
                });
              }}
            >
              <label>
                希望 Agent 完成什么任务？
                <textarea
                  required
                  value={description}
                  onChange={(e) => describe(e.target.value)}
                  placeholder="例如，根据访谈记录整理客户诉求，并附上原话依据。"
                />
              </label>
              <label>
                成果类型
                <select
                  value={operation}
                  onChange={(e) => selectOutput(e.target.value as Output)}
                >
                  <option value="text">文本</option>
                  <option value="vision">从图片提取或核对信息 · 文本</option>
                  <option value="png">图片（PNG）</option>
                  <option value="image-edit">编辑一张参考图 · PNG</option>
                  {Object.entries(toolChoices).map(([value, label]) => (
                    <option key={value} value={value}>
                      {label}
                    </option>
                  ))}
                </select>
              </label>
              {operation === "aggregate" && <AggregateColumns files={files} />}
              {operation === "docx" && (
                <small>
                  将已备正文或前一步的结果排版成
                  Word。需要撰稿时可在前一步选用文本 Agent。
                </small>
              )}
              {operation === "xlsx" && (
                <small>
                  将每行文字保存为 Excel
                  的一行，保留原值。需要按列求和时请选择「Excel 分组汇总」。
                </small>
              )}
              {operation === "extract" && (
                <small>按原件版本摘取所选资料的正文，可供后续步骤引用。</small>
              )}
              {outline && (
                <small>
                  内置排版工具把准备好的分页提纲制作成可编辑课件。需要代写内容时，可在前一步组合文本
                  Agent。
                </small>
              )}
              {outline && (
                <button
                  type="button"
                  onClick={() => {
                    const request = description.trim();
                    setDraftName(courseWriter.name);
                    describe(
                      request && !request.startsWith(courseWriter.instructions)
                        ? courseWriter.instructions + "\n\n业务要求：" + request
                        : request || courseWriter.instructions,
                    );
                    selectOutput("text");
                  }}
                >
                  从资料编稿助手开始
                </button>
              )}
              {tool && toolOwner && (
                <small>{toolOwner.name}承担工具用量，草稿对该团队可见。</small>
              )}
              {current && (
                <small>
                  使用 {current.name} v{current.revision} ·{" "}
                  {teams.find((t) => t.id === current.team)?.name}
                  承担费用，草稿对该团队可见。
                </small>
              )}
              <SimilarAgents
                agents={
                  outline
                    ? available.filter((a) => a.publishedOutline)
                    : available.filter(a => !!a.publishedVision === (operation === "vision") && !!a.publishedImageEdit === (operation === "image-edit"))
                }
                source={{ name, description }}
                publishedOutput={operation === "vision" ? "text" : operation === "image-edit" ? "png" : operation}
                choiceLabel={
                  combining
                    ? steps.length >= 12
                      ? "组合已达 12 步"
                      : "加入当前组合"
                    : "使用此 Agent"
                }
                choose={
                  combining && steps.length >= 12
                    ? undefined
                    : (a) => usePublished(a, description.trim())
                }
              />
              {(tool || eligible.length > 0) && (
                <ConfigurationFields missing={tool ? !toolOwner : !current} title={tool ? "名称与归属" : "名称与模型"}>
                  <label>
                    名称（可选）
                    <input
                      name="name"
                      value={draftName}
                      onChange={(e) => setDraftName(e.target.value)}
                      placeholder="默认取自任务描述"
                    />
                  </label>
                  {tool ? (
                    teams.length > 0 &&
                    (teams.length > 1 || !toolOwner) && (
                      <label>
                        {outline ? "课件工具归属团队" : "工具归属团队"}
                        <select
                          required
                          value={toolOwner?.id || ""}
                          onChange={(e) => setToolTeam(e.target.value)}
                        >
                          <option value="">请选择归属团队</option>
                          {teams.map((t) => (
                            <option key={t.id} value={t.id}>
                              {t.name}
                            </option>
                          ))}
                        </select>
                      </label>
                    )
                  ) : (
                    <label>
                      {modelLabels[modelOperation]}
                      <select
                        required
                        value={current ? modelKey(current) : ""}
                        onChange={(e) => setModel(e.target.value)}
                      >
                        <option value="">请选择模型</option>
                        {eligible.map((m) => (
                          <option key={modelKey(m)} value={modelKey(m)}>
                            {m.name} v{m.revision} ·{" "}
                            {teams.find((t) => t.id === m.team)?.name}
                          </option>
                        ))}
                      </select>
                    </label>
                  )}
                </ConfigurationFields>
              )}
              {!tool && !eligible.length && (
                <p>
                  {admin ? (
                    <button type="button" onClick={configure}>
                      配置{modelLabels[modelOperation]}
                    </button>
                  ) : (
                    `请联系管理员接入可使用的${modelLabels[modelOperation]}。`
                  )}
                </p>
              )}
              <button
                className="primary"
                disabled={
                  busy || !(tool ? toolOwner : current) || !description.trim()
                }
              >
                创建并准备试运行
              </button>
            </form>
          ) : !teams.some((t) => t.id === draft.team) ? (
            <p role="alert">当前无法访问这份 Agent 草稿。</p>
          ) : currentDraft && currentDraft.revision !== draft.revision ? (
            <p role="status">
              这份草稿已更新为 v{currentDraft.revision}
              。请核对最新版本。
            </p>
          ) : (
            <>
              <h3>
                {draft.name} · v{draft.revision}
              </h3>
              {draft.operation === "png" || draft.operation === "image-edit" ? (
                <AgentChecks
                  key={draft.id + "/" + draft.revision}
                  agent={draft.id}
                  revision={draft.revision}
                  refresh={refresh}
                  pictureInput={draft.operation === "image-edit"}
                  imageSetup={{
                    reviewer: memberEmail,
                    payer: draft.payer,
                    estimate: draft.estimate,
                  }}
                />
              ) : (
                <DraftTrial
                  key={draft.id + "/" + draft.revision}
                  draft={draft}
                  act={act}
                  busy={busy}
                  refresh={refresh}
                />
              )}
              {verified && admin && verified.published !== draft.revision && (
                <button
                  type="button"
                  disabled={busy}
                  onClick={() =>
                    void act(async () => {
                      await api("agents/" + draft.id + "/publish", "POST", {
                        revision: draft.revision,
                      });
                      await refresh();
                      setMode("find");
                      setSearch(draft.name);
                      setDraftName("");
                      setDraft(null);
                      describe(null);
                    })
                  }
                >
                  发布已验证版本
                </button>
              )}
              {currentDraft &&
                !admin &&
                currentDraft.revision === draft.revision &&
                currentDraft.published !== draft.revision &&
                (currentDraft.validated || currentDraft.release) && (
                  <RequestRelease
                    key={draft.id + "/" + draft.revision}
                    agent={currentDraft}
                    refresh={refresh}
                  />
                )}
              {verified?.publishedReady &&
                verified.published === draft.revision && (
                  <button
                    type="button"
                    disabled={combining && steps.length >= 12}
                    onClick={() => usePublished(verified, draft.name)}
                  >
                    {combining
                      ? "将已发布版本加入组合"
                      : "在此对话使用已发布版本"}
                  </button>
                )}
            </>
          )}
          {draft && currentDraft && teams.some(t => t.id === draft.team) &&
            (draft.operation === "text" || draft.operation === "png" || draft.operation === "image-edit") && (
              <DraftInstructions key={draft.id + "/" + draft.revision} agent={currentDraft}
                revision={draft.revision} busy={busy} act={act} refresh={refresh}
                saved={revision => setDraft({ ...draft, name: currentDraft.name, revision })} />
          )}
        </div>
      )}
      {error && <p role="alert">{error}</p>}
    </section>
  );
}

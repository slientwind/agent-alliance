import { ReleaseQueue, RequestRelease } from "./agent-release";
import { AgentTeamField, type AgentTeam } from "./agent-team";
import { ModelMaintenance } from "./model-maintenance";
import { MaintenancePanel } from "./maintenance";
import { ImprovementInbox } from "./improvements";
import { SimilarAgents, type CapabilityDescription } from "./similar-agents";
import { AgentChecks } from "./agent-checks";
import { authorizeTrial } from "./trial-request";
import { PictureTrialField } from "./picture-trial-field";
import { ExternalCardForm, EditExternalCard } from "./external-card";
import { TrialEvidence } from "./trial-evidence";
import { useRef, useState } from "react";
import { api, type Model, type Agent } from "./client";
type Props = {
  models: Model[];
  agents: Agent[];
  admin: boolean;
  refresh: () => Promise<void>;
};
export function Models({
  models,
  refresh,
  team,
}: Pick<Props, "models" | "refresh"> & { team?: string }) {
  const [msg, setMsg] = useState("");
  return (
    <section>
      <h2>模型资源</h2>
      {models.map((m) => (
        <p key={m.id}>
          <strong>{m.name}</strong> ·{" "}
          {{ png: "文生图 PNG", text: "文本", vision: "视觉理解", "image-edit": "参考图编辑" }[m.operation]} · {m.model} · v
          {m.revision}{m.streamText && " · 支持逐段预览"}
        </p>
      ))}
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          const form = e.currentTarget;
          try {
            const { StreamText, ...fields } = Object.fromEntries(new FormData(form));
            await api("resources", "POST", {
              ...fields,
              Team: team,
              Estimate: fields.Estimate || null,
              StreamText: StreamText === "on",
            });
            form.reset();
            await refresh();
            setMsg("已保存模型修订，需通过 Agent 试运行核验。");
          } catch (e) {
            setMsg((e as Error).message);
          }
        }}
      >
        <label>
          资源名称
          <input name="Name" required />
        </label>
        <label>
          模型操作
          <select name="Operation">
            <option value="text">文本对话</option>
            <option value="png">文生图 · 单张 PNG</option>
            <option value="vision">视觉理解 · PNG 或 JPEG 转文本</option>
            <option value="image-edit">参考图编辑 · 单张 PNG</option>
          </select>
        </label>
        <label>
          <input name="StreamText" type="checkbox" /> 桌面逐段预览（仅文本模型）
        </label>
        <label>
          模型服务根地址（含版本路径）
          <input
            name="Endpoint"
            type="url"
            placeholder="https://…/v1"
            required
          />
        </label>
        <label>
          模型 ID
          <input name="Model" required />
          <small>
            请使用与密钥同地域的兼容接口。生图和参考图编辑要求同步返回 PNG
            原件（b64_json），百炼可配置已开通的 qwen-image-3.0 系列。
          </small>
        </label>
        <label>
          单次调用预计金额（CNY，可留空）
          <input name="Estimate" inputMode="decimal" />
        </label>
        <label>
          供应商密钥
          <input name="Key" type="password" required autoComplete="off" />
        </label>
        <small>
          <a
            href="https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope"
            target="_blank"
            rel="noreferrer"
          >
            查看百炼兼容接口与地域配置
          </a>
        </small>
        <button>保存模型资源</button>
      </form>
      <p role="status">{msg}</p>
      <ModelMaintenance team={team} refresh={refresh} />
    </section>
  );
}
export function Library({
  agents,
  models,
  admin,
  refresh,
  teams,
  teamError,
  useAgent,
}: Props & { teams: AgentTeam[]; teamError: string; useAgent?: (agent: Agent) => void }) {
  const draftForm = useRef<HTMLFormElement>(null);
  const [description, describe] = useState<CapabilityDescription>({});
  const [editing, setEditing] = useState<Agent | null>(null),
    [epoch, bump] = useState(0),
    [trial, setTrial] = useState<Agent | null>(null),
    [text, setText] = useState(""),
    [busy, setBusy] = useState(false);
  async function refreshEvidence() {
    bump((value) => value + 1);
    await refresh();
  }
  const commonTeam = teams.length === 1 && agents.length > 0 &&
    agents.every(a => a.team === teams[0].id) ? teams[0] : undefined;
  return (
    <section>
      <h2>团队能力库</h2>
      {commonTeam && <small>归属团队 · {commonTeam.name}</small>}
      {teamError && <p role="alert">{teamError}</p>}
      {admin && <ReleaseQueue agents={agents} refresh={refresh} />}
      <ImprovementInbox />
      {agents.map((a) => (
        <div
          className="card"
          key={a.id}
          id={"library-agent-" + a.id}
          tabIndex={-1}
        >
          <strong>{a.name}</strong>
          {a.publishedVision && <small>读取图片 · 输出文本</small>}
          {a.publishedImageEdit && <small>参考图编辑 · 输出 PNG</small>}
          {!commonTeam && <small>归属团队 · {a.teamName}</small>}
          <p>{a.instructions}</p>
          <small>
            草稿 v{a.revision} · {a.validated ? "验证通过" : "待试运行"} ·{" "}
            {a.published
              ? "已发布 v" +
                a.published +
                (a.publishedReady ? "" : " · 需复验或恢复资源")
              : "尚未发布"}
          </small>
          {!admin && a.revision !== a.published && (a.validated || a.release) &&
            <RequestRelease agent={a} refresh={refresh} />}
          {a.publishedReady &&
            a.publishedReviewDue &&
            Date.parse(a.publishedReviewDue) <= Date.now() && (
              <strong>已到复查日期 · 当前发布版本仍可用</strong>
            )}
          {useAgent && a.publishedReady && a.published > 0 && teams.some(t => t.id === a.team) && (
            <button type="button" onClick={() => useAgent(a)}>
              使用{a.publishedName || a.name} v{a.published} 开始对话
            </button>
          )}
          <AgentChecks
            key={a.revision}
            version={epoch}
            agent={a.id}
            revision={a.revision}
            pictureInput={a.visionRevision || a.imageEditRevision}
            refresh={refreshEvidence}
          />
          {a.published > 0 && a.published !== a.revision && (
            <AgentChecks
              agent={a.id}
              revision={a.published}
              pictureInput={a.publishedVision || a.publishedImageEdit}
              version={epoch}
              refresh={refreshEvidence}
            />
          )}
          <MaintenancePanel
            subject="agent"
            path={"agents/" + a.id + "/maintenance"}
            invited={!!a.awaitingMaintenance}
            refresh={refresh}
          />
          <TrialEvidence
            agent={a.id}
            refresh={refreshEvidence}
            version={epoch}
          />
          <div className="actions">
            {a.card ? (
              <EditExternalCard agent={a} refresh={refresh} />
            ) : a.tool ? (
              <ToolDraft key={a.revision} agent={a} refresh={refresh} />
            ) : (
              <button onClick={() => setEditing(a)}>编辑草稿</button>
            )}
            <button onClick={() => setTrial(a)}>试运行</button>
            {admin && a.validated && a.published !== a.revision && a.release?.state !== "pending" && (
              <SimilarAgents
                agents={agents}
                source={{
                  name: a.name,
                  description: a.instructions,
                  operation: a.tool?.operation,
                  endpoint: a.endpoint,
                }}
                exclude={a.id}
              />
            )}
            {admin && a.validated && a.published !== a.revision && a.release?.state !== "pending" && (
              <button
                onClick={async () => {
                  try {
                    await api("agents/" + a.id + "/publish", "POST", {
                      revision: a.revision,
                    });
                    await refresh();
                  } catch (e) {
                    setText((e as Error).message);
                  }
                }}
              >
                发布此版本
              </button>
            )}
          </div>
        </div>
      ))}
      {trial && (
        <form
          className="card"
          onSubmit={async (e) => {
            e.preventDefault();
            const f = new FormData(e.currentTarget);
            setBusy(true);
            try {
              const message = await authorizeTrial(
                trial.id,
                trial.revision,
                String(f.get("input")),
                String(f.get("expected")),
                undefined,
                undefined,
                f.get("picture") as File | null,
              );
              setText(message);
              bump((x) => x + 1);
              setTrial(null);
              await refresh();
            } catch (e) {
              setText((e as Error).message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <h3>
            验证 {trial.name} v{trial.revision}
          </h3>
          <label>
            试运行输入
            <textarea name="input" required />
          </label>
          {(trial.visionRevision || trial.imageEditRevision) && <PictureTrialField />}
          <label>
            必须包含的内容
            <input name="expected" required />
          </label>
          <label>
            <input type="checkbox" required />
            授权一次模型调用，团队承担实际费用，金额待供应商核对。
          </label>
          <button disabled={busy}>运行并保存结果</button>
        </form>
      )}
      <ToolDraft refresh={refresh} agents={agents} teams={teams} />
      <details className="card">
        <summary>注册已有 Agent Card</summary>
        <ExternalCardForm refresh={refresh} agents={agents} teams={teams} />
      </details>
      <form
        ref={draftForm}
        key={editing?.id || "new"}
        onChange={(e) => {
          const data = new FormData(e.currentTarget);
          describe({
            name: String(data.get("name") || ""),
            description: String(data.get("instructions") || ""),
          });
        }}
        onSubmit={async (e) => {
          e.preventDefault();
          const form = e.currentTarget,
            f = new FormData(form),
            model = models.find((m) => m.id === f.get("resource"));
          if (!model) return;
          try {
            await api("agents", "POST", {
              id: editing?.id || "",
              expected: editing?.revision || 0,
              name: f.get("name"),
              instructions: f.get("instructions"),
              resource: model.id,
              resourceRevision: model.revision,
              key: f.get("key"),
            });
            setEditing(null);
            describe({});
            form.reset();
            await refresh();
            setText("草稿已保存。");
          } catch (e) {
            setText((e as Error).message);
          }
        }}
      >
        <h3>{editing ? "修订 " + editing.name : "描述一个新 Agent"}</h3>
        <label>
          名称
          <input name="name" required defaultValue={editing?.name} />
        </label>
        <label>
          任务、规则与输出要求
          <textarea
            name="instructions"
            required
            defaultValue={editing?.instructions}
          />
        </label>
        <label>
          模型
          <select
            name="resource"
            aria-label="模型"
            required
            defaultValue={editing?.resource || ""}
          >
            <option value="">请选择已配置资源</option>
            {models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name} · {m.model} v{m.revision}
                {m.operation === "png" && " · 文生图 PNG"}
                {m.operation === "vision" && " · 视觉理解"}
                {m.operation === "image-edit" && " · 参考图编辑"}
              </option>
            ))}
          </select>
        </label>
        <label>
          此版本专用凭据（留空采用团队模型凭据）
          <input name="key" type="password" />
        </label>
        {!editing && (
          <SimilarAgents
            agents={agents}
            source={description}
            template={(spec) => {
              const form = draftForm.current;
              if (!form) return;
              const name = form.elements.namedItem("name") as HTMLInputElement;
              const instructions = form.elements.namedItem(
                "instructions",
              ) as HTMLTextAreaElement;
              name.value = spec.name || "";
              instructions.value = spec.description || "";
              describe(spec);
              instructions.focus();
            }}
          />
        )}
        <button disabled={!models.length}>保存待验证草稿</button>
        {editing && (
          <button
            type="button"
            onClick={() => {
              setEditing(null);
              describe({});
            }}
          >
            取消编辑
          </button>
        )}
      </form>
      {!models.length && (
        <p>团队尚未配置模型。请管理员在企业管理中添加供应商资源。</p>
      )}
      <p role="status" className="preserve">
        {text}
      </p>
    </section>
  );
}

function ToolDraft({
  teams = [],
  agents = [],
  agent,
  refresh,
}: {
  teams?: AgentTeam[];
  agents?: Agent[];
  agent?: Agent;
  refresh: () => Promise<void>;
}) {
  const [description, describe] = useState<CapabilityDescription>({});
  const [text, setText] = useState("");
  return (
    <details className={agent ? undefined : "card"}>
      <summary>{agent ? "修订工具 Agent" : "启用内置工具助手"}</summary>
      <form
        onChange={(e) => {
          const f = new FormData(e.currentTarget);
          describe({
            name: String(f.get("name") || ""),
            operation: String(f.get("op") || ""),
            description: String(f.get("description") || ""),
          });
        }}
        onSubmit={async (e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget),
            op = String(f.get("op"));
          try {
            await api("agents/tools", "POST", {
              id: agent?.id,
              expected: agent?.revision || 0,
              team: agent?.team ?? f.get("team"),
              name: f.get("name"),
              description: f.get("description"),
              tool: {
                operation: op,
                contract: op === "pptx" ? f.get("contract") || "" : "",
                config: {
                  ...agent?.tool?.config,
                  title: f.get("title"),
                  group: f.get("group"),
                  amount: f.get("amount"),
                  unitColumn: f.get("unitColumn"),
                  filterColumn: f.get("filter"),
                  filterValue: f.get("value"),
                },
              },
            });
            await refresh();
            setText("已创建工具草稿，请使用代表性输入完成试运行。");
          } catch (e) {
            setText((e as Error).message);
          }
        }}
      >
        {!agent && <AgentTeamField teams={teams} />}
        <label>
          名称
          <input name="name" defaultValue={agent?.name} required />
        </label>
        <label>
          能力描述
          <input
            name="description"
            defaultValue={agent?.instructions}
            required
          />
        </label>
        <label>
          成果标题
          <input name="title" defaultValue={agent?.tool?.config?.title} />
        </label>
        <label>
          操作
          <select name="op" defaultValue={agent?.tool?.operation}>
            {Object.entries({
              extract: "资料摘取",
              aggregate: "分组汇总与 Excel 导出",
              docx: "生成文档",
              pptx: "生成课件",
              xlsx: "生成表格",
            }).map(([v, n]) => (
              <option value={v} key={v}>
                {n}
              </option>
            ))}
          </select>
        </label>
        {(description.operation || agent?.tool?.operation) === "pptx" && (
          <label>
            课件分页方式
            <select name="contract" defaultValue={agent?.tool?.contract || ""}>
              <option value="">每五行一页</option>
              <option value="pptx-outline-v1">
                按提纲页标题分页，可逐页修改
              </option>
            </select>
          </label>
        )}
        <details>
          <summary>分组汇总字段</summary>
          <small>
            按列名合并每份选定的
            CSV，请使用相同金额单位。工作簿列出各份资料的版本、纳入行数和小计。
          </small>
          <label>
            分组列
            <input name="group" defaultValue={agent?.tool?.config?.group} />
          </label>
          <label>
            金额列
            <input name="amount" defaultValue={agent?.tool?.config?.amount} />
          </label>
          <label>
            金额单位列（可选）
            <input
              name="unitColumn"
              defaultValue={agent?.tool?.config?.unitColumn}
              placeholder="例如 currency"
            />
          </label>
          <label>
            筛选列
            <input
              name="filter"
              defaultValue={agent?.tool?.config?.filterColumn}
            />
          </label>
          <label>
            保留值
            <input
              name="value"
              defaultValue={agent?.tool?.config?.filterValue}
            />
          </label>
        </details>
        {!agent && <SimilarAgents agents={agents} source={description} />}
        <button>保存工具草稿</button>
        {text && <p role="status">{text}</p>}
      </form>
    </details>
  );
}

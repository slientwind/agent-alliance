import { useEffect, useRef, useState, type FormEvent } from "react";
import { api, type Project, type WorkflowTemplate, type WorkflowTemplateReference } from "./client";
import { outputOperations } from "./output-operations";

type Summary = Pick<WorkflowTemplate, "id" | "name" | "version"> & {
  team: string;
  teamName: string;
  currentVersion: number;
  publishedVersion: number;
  requestedVersion: number;
  owner: boolean;
  canPublish: boolean;
  canUse: boolean;
  decision: string;
};
type Template = Summary & WorkflowTemplate;
type Fields = Pick<WorkflowTemplate, "name" | "instructions" | "inputs" | "outcomes">;
const newFields: Fields = { name: "", instructions: "", inputs: "", outcomes: "" };
const resource = (id: string) => "workflow-templates/" + id;
const detail = (entry: Summary) => resource(entry.id) + "?version=" + entry.version;

function useRequestKey() {
  const previous = useRef<{ body: string; key: string } | undefined>(undefined);
  return (data: object) => {
    const body = JSON.stringify(data);
    if (previous.current?.body !== body) previous.current = { body, key: crypto.randomUUID() };
    return { "Idempotency-Key": previous.current.key };
  };
}

export function SaveWorkflowTemplate({ project, openTemplates }: { project: Project; openTemplates: () => void }) {
  const [items, setItems] = useState<Summary[]>([]);
  const [existing, setExisting] = useState("");
  const [fields, setFields] = useState(newFields);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState<{ id: string; version: number }>();
  const requestKey = useRequestKey();

  async function choose(id: string) {
    setExisting(id);
    setError("");
    setSaved(undefined);
    const row = items.find(item => item.id === id);
    if (!row) {
      setFields(newFields);
      return;
    }
    setBusy(true);
    try {
      const { name, instructions, inputs, outcomes } = await api<Template>(detail(row));
      setFields({ name, instructions, inputs, outcomes });
    } catch (e) {
      setExisting("");
      setFields(newFields);
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setError("");
    setBusy(true);
    const body = {
      ...fields,
      expected: project.revision,
      replace: existing,
      expectedVersion: items.find(item => item.id === existing)?.version || 0,
    };
    try {
      setSaved(await api<{ id: string; version: number }>(
        `projects/${project.id}/workflow-templates`, "POST", body, requestKey(body),
      ));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <details className="card" onToggle={e => {
      if (e.currentTarget.open) void api<Summary[]>("workflow-templates").then(rows =>
        setItems(rows.filter(item => item.owner && item.team === project.team && item.version === item.currentVersion)),
      ).catch(error => setError(error.message));
    }}>
      <summary>保存为协作模板</summary>
      {saved ? (
        <>
          <p role="status">已保存草稿 v{saved.version}</p>
          <button type="button" onClick={openTemplates}>打开协作模板</button>
        </>
      ) : (
        <form aria-label="保存协作模板" onSubmit={save}>
          <p>复用已交付方案的步骤和审核流程。请填写下一次仍适用的要求。</p>
          <fieldset disabled={busy}>
            {items.length > 0 && (
              <label>
                保存方式
                <select value={existing} onChange={e => void choose(e.target.value)}>
                  <option value="">新建模板草稿</option>
                  {items.map(item => <option key={item.id} value={item.id}>更新 {item.name} · v{item.version}</option>)}
                </select>
              </label>
            )}
            <label>模板名称<input value={fields.name} maxLength={80} required onChange={e => setFields({ ...fields, name: e.target.value })} /></label>
            <label>可复用的执行要求<textarea value={fields.instructions} maxLength={4000} required onChange={e => setFields({ ...fields, instructions: e.target.value })} /></label>
            <label>需要准备的资料<textarea value={fields.inputs} maxLength={2000} required onChange={e => setFields({ ...fields, inputs: e.target.value })} /></label>
            <label>交付和审核约定<textarea value={fields.outcomes} maxLength={2000} required onChange={e => setFields({ ...fields, outcomes: e.target.value })} /></label>
            <button className="primary">保存模板草稿</button>
          </fieldset>
        </form>
      )}
      {error && <p role="alert">{error}</p>}
    </details>
  );
}

export function WorkflowTemplates({ initialGoal, openProject, back, initialTemplate }: { initialGoal: string; openProject: (id: string) => Promise<void>; back?: () => void; initialTemplate?: WorkflowTemplateReference }) {
  const [rows, setRows] = useState<Summary[] | null>(null);
  const [selected, setSelected] = useState<Template>();
  const [goal, setGoal] = useState(initialGoal);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const requestKey = useRequestKey();

  useEffect(() => {
    let live = true;
    void api<Summary[]>("workflow-templates").then(async result => {
      if (live) setRows(result);
      if (initialTemplate && live) {
        const entry = await api<Template>(resource(initialTemplate.id) + "?version=" + initialTemplate.version);
        if (live) setSelected(entry);
      }
    }).catch(error => {
      if (live) setError(error.message);
    });
    return () => { live = false; };
  }, [initialTemplate]);

  async function open(entry: Summary) {
    setBusy(true);
    setError("");
    setNotice("");
    setReason("");
    try {
      setSelected(await api<Template>(detail(entry)));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function publication(action: string) {
    if (!selected) return;
    setBusy(true);
    setError("");
    try {
      await api(resource(selected.id) + "/publication", "POST", { action, version: selected.version, reason });
      const updated = await api<Summary[]>("workflow-templates");
      setRows(updated);
      const entry = updated.find(item => item.id === selected.id && item.version === selected.version);
      setSelected(entry ? await api<Template>(detail(entry)) : undefined);
      setNotice({ request: "发布申请已提交", publish: "该版本已发布给团队", return: "发布申请已退回", archive: "模板已归档" }[action] || "");
      setReason("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function useTemplate(event: FormEvent) {
    event.preventDefault();
    if (!selected?.canUse || !goal.trim()) return;
    const body = { version: selected.version, goal: goal.trim() };
    setError("");
    setBusy(true);
    try {
      const project = await api<{ id: string }>(resource(selected.id) + "/projects", "POST", body, requestKey({ id: selected.id, ...body }));
      await openProject(project.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      {back && <button type="button" disabled={busy} onClick={back}>返回 Agent 列表</button>}
      {error && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
      {!rows && !error && <p role="status">正在读取协作模板…</p>}
      {rows?.length === 0 && <p>交付项目后，可以把经过审核的协作方案保存为模板。</p>}
      {rows?.map(entry => (
        <button key={entry.id + "/" + entry.version} className="list" type="button" disabled={busy}
          aria-pressed={selected?.id === entry.id && selected.version === entry.version} onClick={() => void open(entry)}>
          <strong>{entry.name} · v{entry.version}</strong>
          <span>{entry.teamName} · {entry.publishedVersion === entry.version ? "团队共享" : entry.requestedVersion === entry.version ? "待发布审核" : "个人草稿"}</span>
        </button>
      ))}
      {selected && (
        <section className="card" aria-label="模板详情">
          <h3>{selected.name}</h3>
          <p className="preserve">{selected.instructions}</p>
          <p><strong>输入</strong> {selected.inputs}</p>
          <p><strong>交付</strong> {selected.outcomes}</p>
          <ol>
            {selected.nodes.map((node, i) => (
              <li key={i}>
                <strong>{node.name || "待选择 Agent"}{node.name && " · v" + node.revision}</strong>
                <small>{outputOperations[node.operation] || node.operation}{node.lookupPhase === "search" ? " · 检索原文" : node.lookupPhase === "answer" ? " · 据原文回答" : node.lookupPhase === "answer_calc" ? " · 引文核算" : ""}{node.dependsOn.length ? " · 引用步骤 " + node.dependsOn.join("、") : " · 可独立开始"}</small>
                {(!node.ready || (node.toolChoice && !node.toolChoice.ready)) && <small>需在新方案核对可用的 Agent。</small>}
              </li>
            ))}
          </ol>
          {selected.decision && <p>发布意见 · {selected.decision}</p>}
          {selected.canUse && (
            <form onSubmit={useTemplate}>
              <label>本次任务目标<textarea value={goal} maxLength={16000} required disabled={busy} onChange={e => setGoal(e.target.value)} /></label>
              <button className="primary" disabled={busy || !goal.trim()}>使用模板创建对话</button>
            </form>
          )}
          {selected.version === selected.currentVersion && (
            <div className="actions">
              {selected.owner && selected.requestedVersion !== selected.version && selected.publishedVersion !== selected.version && (
                <button disabled={busy} onClick={() => void publication("request")}>申请发布给团队</button>
              )}
              {selected.canPublish && selected.requestedVersion === selected.version && (
                <>
                  <button disabled={busy} onClick={() => void publication("publish")}>发布该版本</button>
                  <label>退回原因<input value={reason} maxLength={1000} disabled={busy} onChange={e => setReason(e.target.value)} /></label>
                  <button disabled={busy || !reason.trim()} onClick={() => void publication("return")}>退回发布申请</button>
                </>
              )}
            </div>
          )}
          {((selected.canPublish && (selected.version === selected.currentVersion || selected.version === selected.publishedVersion)) ||
            (selected.owner && selected.publishedVersion === 0)) && (
            <button disabled={busy} onClick={() => void publication("archive")}>归档模板</button>
          )}
        </section>
      )}
    </>
  );
}

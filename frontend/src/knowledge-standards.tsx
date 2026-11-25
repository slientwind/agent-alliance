import { useEffect, useState, type FormEvent } from "react";
import { api, RequestError } from "./client";
import type { KnowledgeScope } from "./knowledge-workbench";

type Term = { id: string; spaceId: string; scope: string; term: string; aliases: string[]; definition: string;
  metricKey?: string; metricFormula?: string; enabled: boolean; revision: number };
type Template = { id: string; spaceId: string; scope: string; name: string; format: string;
  default: boolean; active: boolean; revision: number };
const formatLabel: Record<string, string> = { docx: "Word", pptx: "PPT", xlsx: "Excel" };

export function KnowledgeStandards({ space, commons }: KnowledgeScope) {
  const [company, setCompany] = useState(false);
  const current = (company && commons) || space;
  const admin = current.role === "space_admin" && current.kind !== "personal";
  const root = `spaces/${current.id}/`;
  const [terms, setTerms] = useState<Term[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [editing, setEditing] = useState<Term>();
  const [revision, setRevision] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    Promise.all([api<Term[]>(root + "glossary"), api<Template[]>(root + "templates")]).then(([glossary, brand]) => {
      if (active) { setTerms(glossary || []); setTemplates(brand || []); }
    }).catch(error => { if (active) setError(error.message); });
    return () => { active = false; };
  }, [root, revision]);

  async function saveTerm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!editing) return;
    const data = new FormData(event.currentTarget);
    await submit(async () => {
      await api(root + "glossary/" + editing.id, "PUT", {
        term: String(data.get("term") || "").trim(),
        aliases: String(data.get("aliases") || "").split(/[,，、]/).map(item => item.trim()).filter(Boolean),
        definition: String(data.get("definition") || "").trim(),
        metricKey: String(data.get("metricKey") || "").trim(),
        metricFormula: String(data.get("metricFormula") || "").trim(),
        enabled: data.has("enabled"), expectedVersion: editing.revision,
      });
      setEditing(undefined);
    });
  }

  async function upload(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    await submit(async () => {
      const response = await fetch("/api/" + root + `templates/${crypto.randomUUID()}/file`, {
        method: "PUT", body: data, credentials: "same-origin",
      });
      if (!response.ok) {
        const problem = await response.json().catch(() => ({}));
        throw new RequestError(response.status, problem.error || "母版上传失败");
      }
      form.reset();
    });
  }

  async function submit(action: () => Promise<unknown>) {
    setBusy(true);
    setError("");
    try {
      await action();
      setRevision(value => value + 1);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function configure(item: Template, active: boolean, preferred: boolean) {
    await submit(() => api(root + `templates/${item.id}/settings`, "PUT", { active, default: preferred, expectedVersion: item.revision }));
  }

  return <div className="knowledge-stack">
    {commons?.role === "space_admin" && <label className="knowledge-actions">
      <input type="checkbox" checked={company} disabled={busy} onChange={event => {
        setCompany(event.target.checked); setTerms([]); setTemplates([]); setEditing(undefined); setError("");
      }} />维护全企公共底座
    </label>}
    {error && <p role="alert">{error}</p>}
    <section className="card knowledge-panel" aria-label="企业术语口径词典">
      <div className="knowledge-toolbar"><h2>企业术语口径词典</h2>
        {admin && <button type="button" disabled={busy} onClick={() => setEditing({ id: crypto.randomUUID(), spaceId: current.id,
          scope: current.kind, term: "", aliases: [], definition: "", enabled: true, revision: 0 })}>新增术语</button>}
      </div>
      {terms.length === 0 && <p>当前空间没有术语口径。</p>}
      {terms.length > 0 && <div className="admin-table"><table>
        <thead><tr><th scope="col">术语</th><th scope="col">同义词</th><th scope="col">核定口径</th><th scope="col">作用域</th>{admin && <th scope="col">维护</th>}</tr></thead>
        <tbody>{terms.map(item => <tr key={item.id}><th scope="row">{item.term}</th>
          <td>{item.aliases.join("、") || "—"}</td><td>{item.definition}{item.metricFormula && <div><code>{item.metricFormula}</code></div>}</td>
          <td>{item.scope === "commons" ? "全企公共底座" : "当前团队"}{!item.enabled && " · 已停用"}</td>
          {admin && <td>{item.spaceId === current.id && <button type="button" disabled={busy} onClick={() => setEditing(item)}>编辑</button>}</td>}
        </tr>)}</tbody>
      </table></div>}
      {editing && <form className="knowledge-form" aria-label="编辑术语口径" key={editing.id + "/" + editing.revision} onSubmit={saveTerm}>
        <label>术语<input name="term" defaultValue={editing.term} maxLength={120} required /></label>
        <label>同义词<input name="aliases" defaultValue={editing.aliases.join("、")} placeholder="用逗号分隔" /></label>
        <label>口径说明<textarea name="definition" defaultValue={editing.definition} rows={3} maxLength={2000} required /></label>
        <label>指标编号<input name="metricKey" defaultValue={editing.metricKey} placeholder="例如 net_return_rate" /></label>
        <label>指标公式<input name="metricFormula" defaultValue={editing.metricFormula} placeholder="例如 sum(returns) / sum(sales)" maxLength={500} /></label>
        <label><span><input type="checkbox" name="enabled" defaultChecked={editing.enabled} /> 生效</span></label>
        <div className="knowledge-actions"><button type="submit" className="primary" disabled={busy}>保存术语</button>
          <button type="button" onClick={() => setEditing(undefined)}>取消</button></div>
      </form>}
    </section>
    <section className="card knowledge-panel" aria-label="官方交付母版">
      <h2>官方交付母版</h2>
      {templates.length === 0 && <p>当前空间没有官方交付母版。</p>}
      <ul className="knowledge-entries">{templates.map(item => <li key={item.id}><strong>{item.name}</strong>
        <div className="knowledge-badges">
          <span className="knowledge-badge">{formatLabel[item.format] || item.format}</span>
          <small>{item.scope === "commons" ? "全企公共底座" : "当前空间"}{item.default ? " · 默认母版" : ""}{!item.active ? " · 已停用" : ""}</small>
        </div>
        <div className="knowledge-actions">
          {item.active && <a href={"/api/" + root + `templates/${item.id}/file`} download>下载母版</a>}
          {admin && item.spaceId === current.id && <>
            {item.active && !item.default && <button type="button" disabled={busy} onClick={() => void configure(item, true, true)}>设为空间默认</button>}
            {item.active && item.default && <button type="button" disabled={busy} onClick={() => void configure(item, true, false)}>取消默认</button>}
            <button type="button" disabled={busy} onClick={() => void configure(item, !item.active, false)}>{item.active ? "停用母版" : "启用母版"}</button>
          </>}
        </div>
      </li>)}</ul>
      {admin && <details><summary>上传官方母版</summary><form className="knowledge-form" onSubmit={upload}>
        <label>母版名称<input name="name" maxLength={120} placeholder="可使用文件名" /></label>
        <label>母版文件<input name="file" type="file" accept=".docx,.pptx,.xlsx" required /></label>
        <label>填充字段<input name="slots" placeholder='例如 ["title","body"]' /></label>
        <button type="submit" disabled={busy}>上传母版</button>
      </form></details>}
    </section>
  </div>;
}

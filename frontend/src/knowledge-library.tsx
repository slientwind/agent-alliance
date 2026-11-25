import { useEffect, useState, type FormEvent } from "react";
import { FolderOpen, RefreshCw } from "lucide-react";
import { api } from "./client";
import { EvaluationPanel } from "./eval-preflight";
import type { KnowledgeScope } from "./knowledge-workbench";

type Collection = { id: string; name: string; provider: string; state: string; aclMode: string; lastSyncedAt?: string; enabled: boolean; inherited?: boolean };
type Document = { id: string; collectionId: string; title: string; path: string[]; docStatus: string; parseStatus: string;
  sensitivity: string; contentVersion: number; effectiveFrom?: string; effectiveUntil?: string; inherited?: boolean };
type DocResponse = { documents: Document[]; next: string };
type Gap = { id: string; topic: string; askCount: number; status: string; assigneeId?: string; assigneeName?: string;
  revision: number; sampleMaskedQueries: string[]; taskState?: string };
type ConflictSource = { documentId: string; version: number; ordinal: number; title: string; value: string; unit: string; comparator: string };
type Conflict = { id: string; metric: string; status: string; revision: number; left: ConflictSource; right: ConflictSource; resolutionNote?: string };
type Person = { id: string; name: string };

const providers: Record<string, string> = { manual: "本地归档", feishu_wiki: "飞书知识库", feishu_drive: "飞书云空间", dingtalk: "钉钉文档",
  wecom: "企微微盘", confluence: "Confluence", sharepoint: "SharePoint", s3: "对象存储" };
const states: Record<string, string> = { ready: "同步成功", queued: "待同步", syncing: "同步中", paused: "已暂停", denied: "授权失效", unavailable: "同步异常" };
const visibility: Record<string, string> = { public: "公开", internal: "内部", confidential: "机密", secret: "严格保密" };
const docStates: Record<string, string> = { active: "现行有效", deprecated: "已作废", archived: "已归档" };

export function citationPath(space: string, doc: string, version: number, ordinal: number) {
  return `spaces/${encodeURIComponent(space)}/knowledge/documents/${encodeURIComponent(doc)}/citations/${version}/${ordinal}`;
}

type Folder = { children: Map<string, Folder>; files: Document[] };
function assembleTree(items: Document[]): Folder {
  const root: Folder = { children: new Map(), files: [] };
  for (const file of items) {
    let group = root;
    for (const label of file.path || []) {
      if (!group.children.has(label)) group.children.set(label, { children: new Map(), files: [] });
      group = group.children.get(label)!;
    }
    group.files.push(file);
  }
  return root;
}

function DocumentTree({ folder, space, selected, collectionNames }: {
  folder: Folder; space: string; selected: string; collectionNames: Map<string, Collection>;
}) {
  return <ul className="knowledge-entries">
    {[...folder.children.entries()].sort(([a], [b]) => a.localeCompare(b, "zh")).map(([name, contents]) => <li key={name}>
      <details open><summary><FolderOpen size={15} aria-hidden="true" /> {name}</summary>
        <DocumentTree folder={contents} space={space} selected={selected} collectionNames={collectionNames} />
      </details>
    </li>)}
    {folder.files.map(file => {
      const collection = collectionNames.get(file.collectionId);
      const viewable = file.docStatus === "active" && file.parseStatus === "ready";
      return <li key={file.id}>
        {viewable ? <a href={"/api/" + citationPath(space, file.id, file.contentVersion, 1) + "/file"} target="_blank" rel="noreferrer">{file.title}</a> : <strong>{file.title}</strong>}
        <div className="knowledge-badges">
          <span className="knowledge-badge" data-state={file.docStatus}>{docStates[file.docStatus] || file.docStatus}</span>
          <small>{file.inherited ? "全企公共底座" : selected === "personal" ? "个人私夹" : "当前团队"}</small>
          {collection && <small>{providers[collection.provider] || collection.provider}</small>}
          <small>{collection?.aclMode === "source" ? "按原文授权" : "空间权限"} · {visibility[file.sensitivity]}</small>
        </div>
      </li>;
    })}
  </ul>;
}

export function KnowledgeLibrary({ space, commons }: KnowledgeScope) {
  const [collections, setCollections] = useState<Collection[]>([]);
  const [members, setMembers] = useState<Person[]>([]);
  const [gaps, setGaps] = useState<Gap[]>([]);
  const [conflicts, setConflicts] = useState<Conflict[]>([]);
  const [selected, setSelected] = useState("");
  const [search, setSearch] = useState("");
  const [q, setQuery] = useState("");
  const [status, setStatus] = useState("");
  const [primary, setPrimary] = useState<DocResponse>({ documents: [], next: "" });
  const [common, setCommon] = useState<DocResponse>({ documents: [], next: "" });
  const [revision, refresh] = useState(0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const admin = space.role === "space_admin";
  const base = `spaces/${space.id}/knowledge/`;
  const inherited = commons && commons.id !== space.id ? `spaces/${commons.id}/knowledge/` : "";
  const collectionsByID = new Map(collections.map(item => [item.id, item]));
  const isCommon = !!selected && collectionsByID.get(selected)?.inherited;

  useEffect(() => {
    let live = true;
    const current = api<Collection[]>(base + "collections");
    const shared = inherited ? api<Collection[]>(inherited + "collections") : Promise.resolve<Collection[]>([]);
    Promise.all([current, shared]).then(([a, b]) => {
      if (live) setCollections([...a, ...b.map(item => ({ ...item, inherited: true }))]);
    }).catch(error => { if (live) setError(error.message); });
    if (admin && space.kind === "team") {
      Promise.all([api<Person[]>(`spaces/${space.id}/members`), api<Gap[]>(base + "gaps"), api<Conflict[]>(base + "conflicts")])
        .then(([people, gapItems, conflictItems]) => { if (live) { setMembers(people); setGaps(gapItems); setConflicts(conflictItems); } })
        .catch(error => { if (live) setError(error.message); });
    }
    return () => { live = false; };
  }, [base, inherited, admin, space.id, space.kind, revision]);

  function query(after = "") {
    const params = new URLSearchParams();
    if (selected) params.set("collection", selected);
    if (q) params.set("q", q);
    if (status) params.set("status", status);
    if (after) params.set("after", after);
    return "documents?" + params.toString();
  }

  useEffect(() => {
    let live = true;
    const empty = Promise.resolve<DocResponse>({ documents: [], next: "" });
    Promise.all([
      isCommon ? empty : api<DocResponse>(base + query()),
      inherited && (!selected || isCommon) ? api<DocResponse>(inherited + query()) : empty,
    ]).then(([current, shared]) => { if (live) { setPrimary(current); setCommon(shared); } })
      .catch(error => { if (live) setError(error.message); });
    return () => { live = false; };
  }, [base, inherited, q, selected, isCommon, revision, status]);

  async function mutate(path: string, method: string, body?: unknown) {
    setBusy(true);
    setError("");
    try {
      await api(path, method, body);
      refresh(value => value + 1);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function more(shared: boolean) {
    const current = shared ? common : primary;
    setBusy(true);
    try {
      const next = await api<DocResponse>((shared ? inherited : base) + query(current.next));
      const update = shared ? setCommon : setPrimary;
      update({ documents: [...current.documents, ...next.documents], next: next.next });
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const docs = [...primary.documents, ...common.documents.map(item => ({ ...item, inherited: true }))];
  return <div className="knowledge-stack">
    {error && <p role="alert">{error}</p>}
    <div className="knowledge-split">
      <section className="card knowledge-panel" aria-label="同步来源">
        <h2>同步来源</h2>
        <button type="button" aria-pressed={!selected} onClick={() => setSelected("")}>全部目录</button>
        <ul className="knowledge-entries">{collections.map(collection => <li key={collection.id}>
          <button type="button" aria-pressed={selected === collection.id} onClick={() => setSelected(collection.id)}>{collection.name}</button>
          <div className="knowledge-badges">
            <small>{providers[collection.provider] || collection.provider}{collection.inherited && " · 全企公共底座"}</small>
            <span className="knowledge-badge" data-state={collection.state}>{states[collection.state] || collection.state}</span>
          </div>
          {admin && !collection.inherited && collection.provider !== "manual" && <button type="button" disabled={busy || !collection.enabled}
            onClick={() => void mutate(base + `collections/${collection.id}/sync`, "POST")}><RefreshCw size={14} aria-hidden="true" /> 同步</button>}
        </li>)}</ul>
        {collections.length === 0 && <p>当前空间没有可读的知识目录。</p>}
      </section>
      <section className="card knowledge-panel" aria-label="空间文档">
        <h2>空间文档</h2>
        <p className="knowledge-caption">混合检索 · 全文、语义向量与重排</p>
        <form className="knowledge-toolbar" onSubmit={event => { event.preventDefault(); setQuery(search.trim()); }}>
          <input type="search" aria-label="查找文档标题" placeholder="查找文档标题" value={search} onChange={event => setSearch(event.target.value)} />
          <select aria-label="文档状态" value={status} onChange={event => setStatus(event.target.value)}>
            <option value="">全部状态</option>{Object.entries(docStates).map(([key, name]) => <option key={key} value={key}>{name}</option>)}
          </select>
          <button type="submit">查找</button>
        </form>
        {docs.length > 0 ? <DocumentTree folder={assembleTree(docs)} space={space.id} selected={space.kind} collectionNames={collectionsByID} /> : <p>没有匹配的可读文档。</p>}
        {primary.next && <button type="button" disabled={busy} onClick={() => void more(false)}>更多空间文档</button>}
        {common.next && <button type="button" disabled={busy} onClick={() => void more(true)}>更多全企文档</button>}
      </section>
    </div>
    {admin && space.kind === "team" && <>
      <div className="knowledge-grid">
        <section className="card knowledge-panel"><h2>知识缺口</h2>
          {gaps.length === 0 && <p>暂无待处理的知识缺口。</p>}
          <ul className="knowledge-entries">{gaps.map(gap => <li key={gap.id}><strong>{gap.topic}</strong>
            <p className="knowledge-caption">{gap.askCount} 次提问{gap.assigneeName && ` · ${gap.assigneeName}`}{gap.taskState === "sent" && " · 已同步飞书"}</p>
            {gap.status === "open" && <GapAssignment people={members} busy={busy} submit={member => void mutate(base + `gaps/${gap.id}/assignee`, "PUT",
              { assigneeId: member, expectedVersion: gap.revision })} />}
          </li>)}</ul>
        </section>
        <section className="card knowledge-panel"><h2>制度冲突</h2>
          {conflicts.length === 0 && <p>暂无制度冲突记录。</p>}
          <ul className="knowledge-entries">{conflicts.map(item => <li key={item.id}><strong>{item.metric}</strong>
            <ul>{[item.left, item.right].map((source, index) => <li key={index}>
              <a href={"/api/" + citationPath(space.id, source.documentId, source.version, source.ordinal) + "/file"} target="_blank" rel="noreferrer">{source.title}</a>
              {" · "}{source.comparator} {source.value} {source.unit}
            </li>)}</ul>
            {item.status === "open" ? <ConflictDecision busy={busy} submit={(resolution, note) => void mutate(base + `conflicts/${item.id}/resolution`, "PUT",
              { resolution, note, expectedVersion: item.revision })} /> : <p>{item.resolutionNote || "已确认口径"}</p>}
          </li>)}</ul>
        </section>
      </div>
      <EvaluationPanel fixedSpace={space.id} initialKind="knowledge" />
    </>}
  </div>;
}

function GapAssignment({ people, busy, submit }: { people: Person[]; busy: boolean; submit: (id: string) => void }) {
  const [person, setPerson] = useState("");
  return <form className="knowledge-toolbar" onSubmit={event => { event.preventDefault(); if (person) submit(person); }}>
    <select aria-label="补齐文档负责人" value={person} onChange={event => setPerson(event.target.value)} required>
      <option value="">选择负责人</option>{people.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
    </select>
    <button type="submit" disabled={busy || !person}>指派补齐文档</button>
  </form>;
}

function ConflictDecision({ busy, submit }: { busy: boolean; submit: (decision: string, note: string) => void }) {
  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    submit(String(data.get("decision")), String(data.get("note")).trim());
  }
  return <details><summary>确认适用口径</summary><form className="knowledge-form" onSubmit={save}>
    <label>采用<select name="decision"><option value="use_left">第一份制度</option><option value="use_right">第二份制度</option>
      <option value="different_scope">分别适用</option></select></label>
    <label>消歧原因<input name="note" maxLength={600} required /></label>
    <button type="submit" disabled={busy}>保存口径决定</button>
  </form></details>;
}

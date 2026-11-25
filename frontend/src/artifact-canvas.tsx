import { X } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArtifactEditor, type RevisionDraft } from "./artifact-editor";
import { api, RequestError, type Artifact, type Member, type Project } from "./client";
import { canPreview, FilePreview } from "./file-preview";
import { MarkdownText } from "./markdown-text";

type CanvasVersion = { version: number; body: string | null; file: string | null };
type AnchorKind = "paragraph" | "row" | "page";
type Comment = {
  id: string;
  author: string;
  authorName: string;
  anchor: { kind: AnchorKind; index: number; sheet?: string };
  body: string;
  revision: number;
  resolvedAt?: string;
  mentions?: string[];
  agentRun?: { id: string; status: string; version?: number; failure?: string };
};
type CommentPage = { comments: Comment[]; before?: string };

type Focus = { comment: string; version: number };
type Participants = Project["participants"];

const anchorNames = { paragraph: "段落", row: "行", page: "页" };

export function artifactAnchorKind(file: string | null): AnchorKind | undefined {
  if (!file || /\.(docx|txt|md)$/i.test(file)) return "paragraph";
  if (/\.(xlsx|csv|tsv)$/i.test(file)) return "row";
  if (/\.(pptx|pdf)$/i.test(file)) return "page";
}

export function ArtifactCanvas({
  project, artifact, user, commentRevision, focus, draft, update, refresh, close,
}: {
  project: Project;
  artifact: Artifact;
  user: Member;
  commentRevision: number;
  focus?: Focus;
  draft?: RevisionDraft;
  update: (next?: RevisionDraft, expected?: RevisionDraft) => void;
  refresh: () => Promise<void>;
  close: () => void;
}) {
  const title = useRef<HTMLHeadingElement>(null);
  const [selection, setSelection] = useState<number | undefined>(focus?.version);
  const [focused, setFocused] = useState(focus);
  const [versions, setVersions] = useState<CanvasVersion[]>([]);
  const [historyError, setHistoryError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editError, setEditError] = useState("");
  const endpoint = "projects/" + project.id + "/artifacts/" + artifact.id;
  const version = selection ?? artifact.version;
  useEffect(() => {
    if (focus) {
      setFocused(focus);
      setSelection(focus.version);
    }
  }, [focus]);
  useLayoutEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    title.current?.focus();
    return () => {
      requestAnimationFrame(() => { if (previous?.isConnected) previous.focus(); });
    };
  }, []);
  useEffect(() => {
    let active = true;
    void api<CanvasVersion[]>(endpoint + "/versions").then(rows => {
      if (active) {
        setVersions(rows);
        setHistoryError("");
      }
    }).catch((error: Error) => {
      if (active) {
        setVersions([]);
        setHistoryError(error.message);
      }
    });
    return () => { active = false; };
  }, [endpoint, artifact.version, project.revision]);

  async function perform(action: () => Promise<unknown>) {
    setBusy(true);
    setEditError("");
    try {
      await action();
      await refresh();
    } catch (error) {
      setEditError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const current = version === artifact.version;
  return (
    <aside className="artifact-canvas" aria-labelledby="artifact-canvas-title" onKeyDown={event => {
      if (event.key === "Escape") close();
    }}>
      <header>
        <h2 id="artifact-canvas-title" ref={title} tabIndex={-1}>{artifact.name}</h2>
        <button className="rail-icon" type="button" aria-label="关闭成果面板" onClick={close}><X /></button>
      </header>
      <label>成果版本
        <select value={current ? "" : selection ?? ""} onChange={event => {
          setFocused(undefined);
          setSelection(Number(event.target.value) || undefined);
          setEditError("");
        }}>
          <option value="">当前 · v{artifact.version}</option>
          {versions.filter(entry => entry.version !== artifact.version).map(entry =>
            <option key={entry.version} value={entry.version}>v{entry.version}</option>)}
        </select>
      </label>
      {historyError && <p role="alert">{historyError}</p>}
      <CanvasEdition key={version} endpoint={endpoint} version={version}
        current={current ? artifact : undefined} commentRevision={commentRevision} accessRevision={project.revision}
        user={user.id} participants={project.participants ?? []} focused={focused?.version === version ? focused.comment : undefined}
        canModerate={project.canReview || user.id === project.owner}
        canAssign={project.stage === "review" || project.stage === "brief"}
        pin={comment => { setSelection(version); setFocused({ comment, version }); }}
        resetFocus={() => setFocused(undefined)}
        showVersion={next => { setFocused(undefined); setSelection(next); }} />
      {current && !historyError && !artifact.draftBlock && (
        <ArtifactEditor project={project} artifact={artifact} draft={draft} update={update} busy={busy} perform={perform} refresh={refresh} />
      )}
      {editError && <p role="alert">{editError}</p>}
    </aside>
  );
}

function CanvasEdition({ endpoint, version, current, commentRevision, accessRevision, ...comments }: {
  endpoint: string;
  version: number;
  current?: CanvasVersion;
  commentRevision: number;
  accessRevision: number;
} & CommentAccess) {
  const [detail, setDetail] = useState<CanvasVersion>();
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void api<CanvasVersion[]>(endpoint + "/versions?version=" + version).then(rows => {
      if (active) {
        setDetail(rows[0]);
        setError(rows[0] ? "" : "此版本暂不可查看。");
      }
    }).catch((error: Error) => {
      if (active) {
        setDetail(undefined);
        setError(error.message);
      }
    });
    return () => { active = false; };
  }, [endpoint, version, accessRevision]);
  if (error) return <p role="alert">{error}</p>;
  if (!detail) return <p role="status">正在打开成果…</p>;
  const edition = current ?? detail;
  const fileURL = "/api/" + endpoint + "/file?version=" + version;
  return (
    <>
      {edition.body && <MarkdownText content={edition.body} />}
      {edition.file && canPreview(edition.file) && <FilePreview url={fileURL} name={edition.file} expanded />}
      {edition.file && <a href={fileURL} download>下载 v{version}</a>}
      <CanvasComments endpoint={endpoint} version={version} file={edition.file} canComment={!!current}
        commentRevision={commentRevision} {...comments} />
    </>
  );
}

type CommentAccess = {
  user: string;
  participants: Participants;
  canModerate: boolean;
  canAssign: boolean;
  focused?: string;
  pin: (comment: string) => void;
  resetFocus: () => void;
  showVersion: (version: number) => void;
};

function CanvasComments({
  endpoint, version, file, canComment, commentRevision, user, participants, canModerate, canAssign, focused, pin, resetFocus, showVersion,
}: {
  endpoint: string;
  version: number;
  file: string | null;
  canComment: boolean;
  commentRevision: number;
} & CommentAccess) {
  const [page, setPage] = useState<CommentPage>({ comments: [] });
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [requested, requestRefresh] = useState(0);
  const [addingPeople, setAddingPeople] = useState(false);
  const [people, setPeople] = useState<string[]>([]);
  const [agent, setAgent] = useState(false);
  const submission = useRef<{ body: string; key: string } | undefined>(undefined);
  const focusRef = useRef<HTMLElement>(null);
  const names = new Map(participants.map(person => [person.id, person.name]));
  const options = participants.filter(person => person.enabled && person.id !== user);
  const pdf = /\.pdf$/i.test(file ?? "");
  const commentURL = endpoint + "/comments?version=" + version + (focused ? "&focus=" + focused : "");
  const kind = artifactAnchorKind(file);
  useEffect(() => {
    let active = true;
    void api<CommentPage>(commentURL).then(result => {
      if (active) setPage(result);
    }).catch((problem: Error) => {
      if (active) {
        setPage({ comments: [] });
        setError(problem.message);
      }
    });
    return () => { active = false; };
  }, [commentURL, commentRevision, requested]);
  const target = page.comments.find(comment => comment.id === focused)?.id;
  useEffect(() => {
    if (target) focusRef.current?.focus();
  }, [target]);

  async function change(request: () => Promise<unknown>, done: () => void) {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await request();
      done();
      requestRefresh(value => value + 1);
    } catch (problem) {
      setError((problem as Error).message);
      if (problem instanceof RequestError && problem.status === 409) requestRefresh(value => value + 1);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="canvas-comments" aria-label="行内评论">
      <h3>行内评论</h3>
      {focused && <button type="button" onClick={resetFocus}>查看全部评论</button>}
      {page.comments.map(comment => (
        <article key={comment.id} ref={comment.id === focused ? focusRef : undefined} tabIndex={comment.id === focused ? -1 : undefined}>
          <strong>{comment.authorName} · {comment.anchor.sheet && comment.anchor.sheet + " · "}
            第 {comment.anchor.index} {anchorNames[comment.anchor.kind]}</strong>
          <p className="preserve">{comment.body}</p>
          {!!comment.mentions?.length && <p><small>{comment.mentions.map(id => "@" + (names.get(id) ?? "协作成员")).join(" ")}</small></p>}
          {comment.agentRun?.version ? (
            <button type="button" onClick={() => showVersion(comment.agentRun!.version!)}>查看局部修订 v{comment.agentRun.version}</button>
          ) : comment.agentRun && <small>{comment.agentRun.failure ?? (
            comment.agentRun.status === "cancelled" ? "局部修改已取消" : "Agent 正在处理此处"
          )}</small>}
          {comment.resolvedAt && <small>已解决</small>}
          {(canModerate || comment.author === user) && (
            <button type="button" disabled={busy} onClick={() => void change(
              () => api(endpoint + "/comments/" + comment.id, "PATCH", { expected_revision: comment.revision, resolved: !comment.resolvedAt }),
              () => setNotice(comment.resolvedAt ? "评论已重新打开。" : "评论已解决。"),
            )}>{comment.resolvedAt ? "重新打开" : "标记已解决"}</button>
          )}
        </article>
      ))}
      {page.before && <button type="button" disabled={busy} onClick={async () => {
        setBusy(true);
        try {
          const next = await api<CommentPage>(commentURL + "&before=" + page.before);
          setPage(previous => previous.before === page.before ? { ...next, comments: [...previous.comments, ...next.comments] } : previous);
        } catch (error) {
          setError((error as Error).message);
        } finally {
          setBusy(false);
        }
      }}>加载更早评论</button>}
      {canComment && kind && (
        <form onSubmit={event => {
          event.preventDefault();
          const form = event.currentTarget;
          const data = new FormData(form);
          const mentions = people.filter(id => options.some(person => person.id === id));
          const assign = agent && canAssign && canModerate;
          const body = {
            expected_version: version, anchor: { kind, index: Number(data.get("index")), sheet: data.get("sheet") || undefined },
            body: data.get("body"),
            ...(mentions.length ? { mentions } : {}),
            ...(assign ? { agent: true } : {}),
          };
          const serialized = JSON.stringify(body);
          if (submission.current?.body !== serialized) submission.current = { body: serialized, key: crypto.randomUUID() };
          const key = submission.current.key;
          let posted: Comment;
          void change(async () => {
            posted = await api<Comment>(endpoint + "/comments", "POST", body, { "Idempotency-Key": key });
          }, () => {
            form.reset();
            submission.current = undefined;
            setAgent(false);
            setPeople([]);
            setAddingPeople(false);
            if (posted.agentRun) {
              pin(posted.id);
              setNotice("评论已发送，Agent 将处理选定位置。");
            } else {
              resetFocus();
              setNotice("评论已发送。");
            }
          });
        }}>
          <label>{anchorNames[kind]}编号 <input name="index" type="number" min={1} max={kind === "row" ? 1048576 : 100000} required /></label>
          {file && /\.xlsx$/i.test(file) && <label>工作表（可选）<input name="sheet" maxLength={31} /></label>}
          <label>评论 <textarea name="body" rows={3} maxLength={4000} required /></label>
          {options.length > 0 && <button type="button" aria-expanded={addingPeople} onClick={() => setAddingPeople(!addingPeople)}>
            @同事{people.length ? "（" + people.length + "）" : ""}
          </button>}
          {addingPeople && <fieldset>
            <legend>提醒谁查看此处</legend>
            {options.map(person => <label key={person.id}>
              <input type="checkbox" checked={people.includes(person.id)} disabled={people.length >= 12 && !people.includes(person.id)}
                onChange={event => setPeople(previous => event.target.checked ? [...previous, person.id] : previous.filter(id => id !== person.id))} />
              {person.name}
            </label>)}
          </fieldset>}
          {canAssign && canModerate && <label>
            <input type="checkbox" checked={agent} onChange={event => setAgent(event.target.checked)} />
            @Agent {pdf ? "为此页生成修订说明" : "局部修改此处"}
          </label>}
          <button type="submit" disabled={busy}>发送评论</button>
        </form>
      )}
      {notice && <p role="status">{notice}</p>}
      {error && <p role="alert">{error}</p>}
    </section>
  );
}

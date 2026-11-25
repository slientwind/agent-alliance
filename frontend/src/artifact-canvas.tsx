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
};
type CommentPage = { comments: Comment[]; before?: string };

const anchorNames = { paragraph: "段落", row: "行", page: "页" };

export function artifactAnchorKind(file: string | null): AnchorKind | undefined {
  if (!file || /\.(docx|txt|md)$/i.test(file)) return "paragraph";
  if (/\.(xlsx|csv|tsv)$/i.test(file)) return "row";
  if (/\.(pptx|pdf)$/i.test(file)) return "page";
}

export function ArtifactCanvas({
  project, artifact, user, commentRevision, draft, update, refresh, close,
}: {
  project: Project;
  artifact: Artifact;
  user: Member;
  commentRevision: number;
  draft?: RevisionDraft;
  update: (next?: RevisionDraft, expected?: RevisionDraft) => void;
  refresh: () => Promise<void>;
  close: () => void;
}) {
  const title = useRef<HTMLHeadingElement>(null);
  const [selection, setSelection] = useState<number>();
  const [versions, setVersions] = useState<CanvasVersion[]>([]);
  const [historyError, setHistoryError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editError, setEditError] = useState("");
  const endpoint = "projects/" + project.id + "/artifacts/" + artifact.id;
  const version = selection ?? artifact.version;
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
        <select value={selection ?? ""} onChange={event => { setSelection(Number(event.target.value) || undefined); setEditError(""); }}>
          <option value="">当前 · v{artifact.version}</option>
          {versions.filter(entry => entry.version !== artifact.version).map(entry =>
            <option key={entry.version} value={entry.version}>v{entry.version}</option>)}
        </select>
      </label>
      {historyError && <p role="alert">{historyError}</p>}
      <CanvasEdition key={version} endpoint={endpoint} version={version}
        current={current ? artifact : undefined} commentRevision={commentRevision} accessRevision={project.revision}
        user={user.id} canModerate={project.canReview || user.id === project.owner} />
      {current && !historyError && !artifact.draftBlock && (
        <ArtifactEditor project={project} artifact={artifact} draft={draft} update={update} busy={busy} perform={perform} refresh={refresh} />
      )}
      {editError && <p role="alert">{editError}</p>}
    </aside>
  );
}

function CanvasEdition({ endpoint, version, current, commentRevision, accessRevision, user, canModerate }: {
  endpoint: string;
  version: number;
  current?: CanvasVersion;
  commentRevision: number;
  accessRevision: number;
  user: string;
  canModerate: boolean;
}) {
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
        commentRevision={commentRevision} user={user} canModerate={canModerate} />
    </>
  );
}

function CanvasComments({ endpoint, version, file, canComment, commentRevision, user, canModerate }: {
  endpoint: string;
  version: number;
  file: string | null;
  canComment: boolean;
  commentRevision: number;
  user: string;
  canModerate: boolean;
}) {
  const [page, setPage] = useState<CommentPage>({ comments: [] });
  const [notice, setNotice] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [requested, requestRefresh] = useState(0);
  const commentURL = endpoint + "/comments?version=" + version;
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
      {page.comments.map(comment => (
        <article key={comment.id}>
          <strong>{comment.authorName} · {comment.anchor.sheet && comment.anchor.sheet + " · "}
            第 {comment.anchor.index} {anchorNames[comment.anchor.kind]}</strong>
          <p className="preserve">{comment.body}</p>
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
          void change(() => api(endpoint + "/comments", "POST", {
            expected_version: version, anchor: { kind, index: Number(data.get("index")), sheet: data.get("sheet") || undefined },
            body: data.get("body"),
          }), () => { form.reset(); setNotice("评论已发送。"); });
        }}>
          <label>{anchorNames[kind]}编号 <input name="index" type="number" min={1} max={kind === "row" ? 1048576 : 100000} required /></label>
          {file && /\.xlsx$/i.test(file) && <label>工作表（可选）<input name="sheet" maxLength={31} /></label>}
          <label>评论 <textarea name="body" rows={3} maxLength={4000} required /></label>
          <button type="submit" disabled={busy}>发送评论</button>
        </form>
      )}
      {notice && <p role="status">{notice}</p>}
      {error && <p role="alert">{error}</p>}
    </section>
  );
}

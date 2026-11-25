import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { X } from "lucide-react";
import { api, type KnowledgeCitation } from "./client";
import { FilePreview } from "./file-preview";
import { citationPath } from "./knowledge-citation";

type CitationDetails = KnowledgeCitation & { excerpt: string; docStatus: string };

export function citationLocation(anchor: KnowledgeCitation["citationAnchor"]) {
  const span = (first: number, last?: number) => last && last !== first ? first + "–" + last : first;
  if (!anchor) return "原文";
  const page = anchor.page && (anchor.kind.startsWith("slide") ? "幻灯片 " : "第 ") + span(anchor.page, anchor.endPage) + " 页";
  const row = anchor.startRow && "第 " + span(anchor.startRow, anchor.endRow) + " 行";
  const line = anchor.startLine && "第 " + span(anchor.startLine, anchor.endLine) + " 行";
  return [page, anchor.sheet, row, line, anchor.paragraph && `第 ${anchor.paragraph} 段`].filter(Boolean).join(" · ") || "原文";
}

export function KnowledgeCanvas({ citation, revision, close }: {
  citation: KnowledgeCitation; revision: string; close: () => void;
}) {
  const title = useRef<HTMLHeadingElement>(null);
  const [result, setResult] = useState<{ revision: string; item?: CitationDetails; error?: string }>();
  const path = citationPath(citation.spaceId, citation.documentId, citation.version, citation.ordinal);
  useLayoutEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    title.current?.focus();
    return () => { requestAnimationFrame(() => { if (previous?.isConnected) previous.focus(); }); };
  }, []);
  useEffect(() => {
    let active = true;
    void api<CitationDetails>(path).then(item => {
      if (active) setResult({ revision, item });
    }).catch(error => {
      if (active) setResult({ revision, error: (error as Error).message });
    });
    return () => { active = false; };
  }, [path, revision]);

  const current = result?.revision === revision ? result : undefined;
  const item = current?.item;
  const anchor = item?.citationAnchor;
  return <aside className="artifact-canvas knowledge-canvas" aria-labelledby="knowledge-canvas-title" onKeyDown={event => {
    if (event.key === "Escape") { event.preventDefault(); close(); }
  }}>
    <header>
      <h2 id="knowledge-canvas-title" ref={title} tabIndex={-1}>知识溯源</h2>
      <button type="button" onClick={close} aria-label="关闭知识溯源"><X size={18} aria-hidden="true" /></button>
    </header>
    {!current && <p role="status">正在核验原文权限…</p>}
    {current?.error && <p role="alert">{current.error}</p>}
    {item && <>
      <h3>{item.title} · v{item.version}</h3>
      {!!item.breadcrumb?.length && <p>{item.breadcrumb.join(" / ")}</p>}
      <p>{citationLocation(item.citationAnchor)} · {item.docStatus === "active" ? "现行有效" : "已归档"}</p>
      <blockquote className="knowledge-excerpt">{item.excerpt}</blockquote>
      {anchor?.kind.startsWith("pdf") && anchor.page && <FilePreview url={"/api/" + path + "/file"} name={item.title}
        expanded pdfLocation={{ page: anchor.page, endPage: anchor.endPage, box: anchor.box, endBox: anchor.endBox }} />}
      <a href={"/api/" + path + "/file"} target="_blank" rel="noreferrer">打开原文</a>
    </>}
  </aside>;
}

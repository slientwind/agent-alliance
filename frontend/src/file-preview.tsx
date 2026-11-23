import { useEffect, useRef, useState } from "react";
import type {
  PDFDocumentProxy,
  PDFDocumentLoadingTask,
  RenderTask,
} from "pdfjs-dist";
import pdfWorker from "pdfjs-dist/build/pdf.worker.min.mjs?url";

export function canPreview(name: string, readable = false) {
  return readable || /\.(pdf|docx|xlsx|pptx|png|jpe?g|webp)$/i.test(name);
}
export function FilePreview({ url, name }: { url: string; name: string }) {
  const [open, setOpen] = useState(false);
  return (
    <details
      className="file-preview"
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary>预览 {name}</summary>
      {open && <PreviewContent key={url} url={url} name={name} />}
    </details>
  );
}

function PreviewContent({ url, name }: { url: string; name: string }) {
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null),
    [page, setPage] = useState(1),
    [text, setText] = useState<string | null>(null),
    [image, setImage] = useState(""),
    [error, setError] = useState(""),
    [pageText, setPageText] = useState(""),
    [ready, setReady] = useState(false);
  const canvas = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const abort = new AbortController();
    let blobURL = "",
      task: PDFDocumentLoadingTask | undefined;
    async function load() {
      const path = new URL(url, location.origin);
      path.searchParams.set("preview", "1");
      const response = await fetch(path, {
        signal: abort.signal,
        cache: "no-store",
      });
      if (!response.ok)
        throw new Error((await response.json()).error || "预览不可用");
      const blob = await response.blob();
      if (abort.signal.aborted) return;
      if (blob.type === "application/pdf") {
        const lib = await import("pdfjs-dist");
        lib.GlobalWorkerOptions.workerSrc = pdfWorker;
        const bytes = await blob.arrayBuffer();
        if (abort.signal.aborted) return;
        task = lib.getDocument({
          data: bytes,
          isEvalSupported: false,
          enableXfa: false,
          useSystemFonts: true,
          maxImageSize: 16000000,
        });
        const pdf = await task.promise;
        if (!abort.signal.aborted) setDoc(pdf);
      } else if (blob.type.startsWith("image/")) {
        blobURL = URL.createObjectURL(blob);
        setImage(blobURL);
      } else if (blob.type.startsWith("text/plain")) {
        const body = await blob.text();
        if (!abort.signal.aborted) setText(body);
      } else throw new Error("此格式支持下载原件");
    }
    void load().catch((e) => {
      if (!abort.signal.aborted) setError(e.message);
    });
    return () => {
      abort.abort();
      if (blobURL) URL.revokeObjectURL(blobURL);
      if (task) void task.destroy().catch(() => {});
    };
  }, [url]);
  useEffect(() => {
    if (!doc || !canvas.current) return;
    const output = canvas.current;
    let active = true,
      render: RenderTask | undefined;
    setReady(false);
    setPageText("");
    setError("");
    async function draw(pdf: PDFDocumentProxy) {
      const sheet = await pdf.getPage(page);
      if (!active) return;
      const natural = sheet.getViewport({ scale: 1 });
      const scale = Math.min(2, 1500 / Math.max(natural.width, natural.height));
      const viewport = sheet.getViewport({ scale });
      output.width = Math.ceil(viewport.width);
      output.height = Math.ceil(viewport.height);
      render = sheet.render({ canvas: output, viewport });
      await render.promise;
      if (!active) return;
      setReady(true);
      const content = await sheet.getTextContent();
      if (active)
        setPageText(
          content.items
            .map((item) => ("str" in item ? item.str : ""))
            .join(" "),
        );
    }
    void draw(doc).catch((e) => {
      if (active) setError(e.message);
    });
    return () => {
      active = false;
      render?.cancel();
    };
  }, [doc, page]);
  return (
    <section aria-label={"文件预览 " + name}>
      {error && <p role="alert">{error}</p>}
      {!error && !doc && !image && text === null && (
        <small role="status">正在准备预览…</small>
      )}
      {doc && (
        <>
          <div className="actions">
            <button
              type="button"
              disabled={page <= 1}
              onClick={() => setPage((n) => n - 1)}
            >
              上一页
            </button>
            <span>
              第 {page} / {doc.numPages} 页
            </span>
            <button
              type="button"
              disabled={page >= doc.numPages}
              onClick={() => setPage((n) => n + 1)}
            >
              下一页
            </button>
          </div>
          <canvas
            ref={canvas}
            role="img"
            aria-label={name + " 第 " + page + " 页"}
            data-rendered={ready}
          />
          {pageText && (
            <details>
              <summary>本页可读取文字</summary>
              <p className="preserve">{pageText}</p>
            </details>
          )}
        </>
      )}
      {image && (
        <img
          src={image}
          alt={name}
          onError={() => setError("图像无法解码，可下载原件")}
        />
      )}
      {text !== null && <pre>{text}</pre>}
    </section>
  );
}

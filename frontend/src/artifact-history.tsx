import { FilePreview, canPreview } from "./file-preview";
import { useEffect, useState } from "react";
import { api, type ChangeRun } from "./client";

type Receipt = {
  sourceInput: ChangeRun["sourceInput"];
  version: number;
  reason: string | null;
  createdAt: string;
  reviewer: string | null;
  reviewedAt: string | null;
  file: string | null;
  body: string | null;
};
export function ArtifactHistory({
  project,
  artifact,
}: {
  project: string;
  artifact: string;
}) {
  const base = "projects/" + project + "/artifacts/" + artifact;
  const [open, setOpen] = useState(false),
    [list, setList] = useState<Receipt[]>([]),
    [version, setVersion] = useState<number | null>(null),
    [detail, setDetail] = useState<Receipt | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let active = true;
    setError("");
    api<Receipt[]>(
      base + "/versions" + (version === null ? "" : "?version=" + version),
    )
      .then((rows) => {
        if (!active) return;
        if (version === null) setList(rows);
        else setDetail(rows[0]);
      })
      .catch((e) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [base, open, version]);
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>版本记录</summary>
      {error && <p role="alert">{error}</p>}
      <ul>
        {list.map((v) => (
          <li key={v.version}>
            <button
              type="button"
              onClick={() => {
                setDetail(null);
                setVersion(v.version);
              }}
              disabled={v.version === version}
            >
              查看 v{v.version}
            </button>{" "}
            {v.reason ?? "版本说明受限"} ·{" "}
            {v.reviewer ? "已由 " + v.reviewer + " 审核" : "未审核"}
          </li>
        ))}
      </ul>
      {detail && (
        <section>
          <strong>
            v{detail.version} · {new Date(detail.createdAt).toLocaleString()}
          </strong>
          {detail.sourceInput && (
            <small>
              改稿使用《{detail.sourceInput.name || "资料"}》v
              {detail.sourceInput.selected} · 方案固定 v
              {detail.sourceInput.planPin}
            </small>
          )}
          <p className="preserve">{detail.body}</p>
          {detail.file && canPreview(detail.file) && (
            <FilePreview
              url={"/api/" + base + "/file?version=" + detail.version}
              name={detail.file}
            />
          )}
          {detail.file && (
            <a
              href={"/api/" + base + "/file?version=" + detail.version}
              download
            >
              下载 v{detail.version} · {detail.file}
            </a>
          )}
        </section>
      )}
    </details>
  );
}

import { useState } from "react";
import { api, type CoverageItem, type FileEvidenceExcerpt } from "./client";

type FileEvidencePassage = FileEvidenceExcerpt & { proof: string };
type FileEvidencePage = { name: string; rows: FileEvidencePassage[]; more: boolean };

export function FileEvidencePicker({ project, item, selected, choose }: {
  project: string;
  item: CoverageItem;
  selected?: string;
  choose: (passage: FileEvidencePassage & { name: string }) => void;
}) {
  const [page, setPage] = useState<FileEvidencePage>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [loadedSearch, setLoadedSearch] = useState("");

  async function load(term = search, after = 0) {
    if (pending || !item.artifact || !item.run || !item.version) return;
    setPending(true);
    setError("");
    const query = new URLSearchParams({
      artifact: item.artifact, run: item.run, version: String(item.version), search: term, after: String(after),
    });
    try {
      const result = await api<FileEvidencePage>(`projects/${project}/delivery/requirements/${item.item}/file-evidence?${query}`);
      setPage(result);
      setLoadedSearch(term);
    } catch (issue) {
      setPage(undefined);
      setError(issue instanceof Error ? issue.message : "暂无法打开成果原文");
    } finally {
      setPending(false);
    }
  }

  return (
    <details onToggle={event => {
      if (event.currentTarget.open && !page && !pending && !error) void load();
    }}>
      <summary>从成果原件选择依据</summary>
      <label>
        在原件中查找
        <input value={search} maxLength={100} onChange={event => setSearch(event.target.value)}
          onKeyDown={event => {
            if (event.key !== "Enter") return;
            event.preventDefault();
            void load();
          }} />
      </label>
      <button type="button" disabled={pending} onClick={() => void load()}>查找</button>
      {pending && <p role="status">正在读取成果原件</p>}
      {error && <p role="alert">{error}</p>}
      {page && (
        <>
          <p>{page.name} · v{item.version}</p>
          {page.rows.length === 0 && <p>未找到符合条件的原文</p>}
          <ul>
            {page.rows.map(row => (
              <li key={row.start}>
                <strong>{row.location}</strong><p className="preserve">{row.text}</p>
                <button type="button" aria-pressed={selected === row.proof}
                  onClick={() => choose({ ...row, name: page.name })}>
                  {selected === row.proof ? "已选为依据" : "选为依据"}
                </button>
              </li>
            ))}
          </ul>
          {page.more && (
            <button type="button" disabled={pending} onClick={() => void load(loadedSearch, page.rows.at(-1)!.end)}>
              下一页
            </button>
          )}
        </>
      )}
    </details>
  );
}

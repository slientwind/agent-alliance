import { useRef, useState } from "react";
import { api } from "./client";

type Hit = {
  id: string;
  name: string;
  version: number;
  start: number;
  end: number;
  excerpt: string;
  location?: string | null;
  exact: boolean;
  lexical: boolean;
  semantic: boolean;
};
type Search = {
  mode: "lexical" | "hybrid" | "unavailable";
  result: { passages: number; indexed: number; hits: Hit[] };
};

export function SourceSearch({
  project,
  pins,
  pin,
}: {
  project: string;
  pins?: Record<string, number>;
  pin?: (id: string, version: number) => void;
}) {
  const [query, setQuery] = useState("");
  const [response, setResponse] = useState<Search>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const sequence = useRef(0);
  async function search() {
    const ticket = ++sequence.current;
    setBusy(true);
    setError("");
    setResponse(undefined);
    try {
      const data = await api<Search>(
        "projects/" +
          project +
          "/retrieval?" +
          new URLSearchParams({ q: query.trim() }),
      );
      if (sequence.current === ticket) setResponse(data);
    } catch (e) {
      if (sequence.current === ticket) setError((e as Error).message);
    } finally {
      if (sequence.current === ticket) setBusy(false);
    }
  }
  return (
    <details className="source-search">
      <summary>在资料中查找依据</summary>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (query.trim() && !busy) void search();
        }}
      >
        <label>
          问题或关键词
          <input
            type="search"
            required
            value={query}
            onChange={(e) => {
              ++sequence.current;
              setBusy(false);
              setResponse(undefined);
              setError("");
              setQuery(e.target.value);
            }}
          />
        </label>
        <button disabled={busy || !query.trim()}>
          {busy ? "查找中…" : "查找原文"}
        </button>
      </form>
      {error && <p role="alert">{error}</p>}
      {response && (
        <>
          <small>
            {response.mode === "hybrid"
              ? `原文与语义检索 · 已索引 ${response.result.indexed} / ${response.result.passages} 段`
              : response.mode === "unavailable"
                ? "语义服务暂不可用，本次按原文查找。"
                : "按原文查找"}
          </small>
          {!response.result.hits.length && (
            <p role="status">当前可读资料中未找到依据。</p>
          )}
          {response.result.hits.map((hit) => (
            <article className="card" key={hit.id + ":" + hit.start}>
              <strong>
                {hit.name} · v{hit.version}
              </strong>
              <small>
                {hit.location && hit.location + " · "}第 {hit.start}–{hit.end}{" "}
                字 ·{" "}
                {hit.exact
                  ? "原词命中"
                  : hit.semantic
                    ? "语义相关，需核对"
                    : "词句相关，需核对"}
              </small>
              <p className="preserve">{hit.excerpt}</p>
              <div className="actions">
                <a
                  href={`/api/projects/${project}/sources/${hit.id}/content?version=${hit.version}`}
                >
                  查看此版原件
                </a>
                {pin && (
                  <button
                    type="button"
                    disabled={pins?.[hit.id] === hit.version}
                    onClick={() => pin(hit.id, hit.version)}
                  >
                    {pins?.[hit.id] === hit.version
                      ? "已在方案中引用"
                      : pins?.[hit.id]
                        ? `将方案引用从 v${pins[hit.id]} 换为 v${hit.version}`
                        : "在方案中引用整份资料"}
                  </button>
                )}
              </div>
            </article>
          ))}
        </>
      )}
    </details>
  );
}

import { useEffect, useState } from "react";
import { api, type Artifact, type Project, type SourceQuote } from "./client";
import type { Source } from "./knowledge";

type Sentence = {
  hash: string;
  text: string;
  start: number;
  end: number;
  location?: string;
  matches: number[];
  suggested?: number;
};
type Comparison = {
  name: string;
  pin: number;
  page: number;
  total: number;
  all: number;
  unmatched: number;
  more: boolean;
  rows: Sentence[];
};
type Pin = { id: string; version: number; name: string };
type Note = { body: string; position: string; sourceQuote: SourceQuote };

export function SourceComparison({
  artifact,
  project,
  sources,
  onFeedback,
}: {
  artifact: Artifact;
  project: Project;
  sources: Source[];
  onFeedback?: (note: Note) => void;
}) {
  const [open, setOpen] = useState(false);
  const pins: Pin[] = [];
  for (const pin of project.plan?.sources || []) {
    const source = sources.find((item) => item.id === pin.id && !item.revoked);
    if (
      source &&
      !artifact.sourceChanges.some(
        (change) => change.source === pin.id && change.revoked,
      )
    )
      pins.push({ ...pin, name: source.name });
  }
  if (
    !pins.length ||
    (artifact.file && !artifact.verifiedWord && artifact.editMode !== "outline")
  )
    return null;
  return (
    <details onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>与方案原文对照</summary>
      {open && (
        <ComparisonView
          key={
            artifact.version +
            ":" +
            pins.map((p) => p.id + "/" + p.version).join(",")
          }
          base={"projects/" + project.id + "/artifacts/" + artifact.id}
          artifact={artifact}
          pins={pins}
          onFeedback={onFeedback}
        />
      )}
    </details>
  );
}

function ComparisonView({
  base,
  artifact,
  pins,
  onFeedback,
}: {
  base: string;
  artifact: Artifact;
  pins: Pin[];
  onFeedback?: (note: Note) => void;
}) {
  const [source, chooseSource] = useState(pins[0].id);
  const [page, setPage] = useState(1);
  const [unmatchedOnly, filter] = useState(false);
  const [reload, setReload] = useState(0);
  const [data, setData] = useState<Comparison>();
  const [error, setError] = useState("");
  const [selected, select] = useState<Record<number, number>>({});
  const isSlides = artifact.editMode === "outline";
  const titles = isSlides
    ? artifact.body.split("\n\n").map((block) => block.split("\n")[0].slice(3))
    : [];
  useEffect(() => {
    let current = true;
    let request = 0;
    setData(undefined);
    setError("");
    select({});
    async function load() {
      const id = ++request;
      try {
        const result = await api<Comparison>(
          base +
            "/source-comparison/" +
            source +
            "?version=" +
            artifact.version +
            "&page=" +
            page +
            (unmatchedOnly ? "&filter=unmatched" : ""),
        );
        if (current && id === request) {
          setData(result);
          setError("");
        }
      } catch (e) {
        if (current && id === request) {
          setData(undefined);
          setError((e as Error).message);
        }
      }
    }
    const sync = () => {
      if (document.visibilityState === "visible") void load();
    };
    void load();
    const timer = window.setInterval(sync, 10000);
    window.addEventListener("focus", sync);
    document.addEventListener("visibilitychange", sync);
    return () => {
      current = false;
      clearInterval(timer);
      window.removeEventListener("focus", sync);
      document.removeEventListener("visibilitychange", sync);
    };
  }, [base, source, artifact.version, page, reload, unmatchedOnly]);
  return (
    <section aria-label="原句对照">
      {pins.length > 1 && (
        <label>
          方案引用的资料
          <select
            value={source}
            onChange={(e) => {
              setData(undefined);
              setPage(1);
              chooseSource(e.target.value);
            }}
          >
            {pins.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name} · v{p.version}
              </option>
            ))}
          </select>
        </label>
      )}
      {error ? (
        <p role="alert">
          {error}{" "}
          <button type="button" onClick={() => setReload(reload + 1)}>
            重新读取原文
          </button>
        </p>
      ) : !data ? (
        <p role="status">正在核对原句…</p>
      ) : (
        <>
          <strong>
            {data.name} · v{data.pin} 对照成果 v{artifact.version}
          </strong>
          <p>
            按句末标点和换行分段，对照整句文字。改写和遗漏的影响由审核人判断。
          </p>
          <a
            href={
              "/api/" +
              base.substring(0, base.indexOf("/artifacts/")) +
              "/sources/" +
              source +
              "/content?version=" +
              data.pin
            }
          >
            打开此版资料
          </a>
          <label>
            <input
              type="checkbox"
              checked={unmatchedOnly}
              onChange={(e) => {
                setData(undefined);
                setPage(1);
                filter(e.target.checked);
              }}
            />
            只看未逐字出现的原句（{data.unmatched} / {data.all}）
          </label>
          {data.total === 0 && (
            <p>
              {data.all
                ? "每条原句均在成果中逐字出现。"
                : "这份资料没有可核对的原句。"}
            </p>
          )}
          {data.rows.map((row) => {
            const target = selected[row.start] ?? row.suggested ?? 0;
            return (
              <article className="card" key={row.start}>
                <small>
                  {row.location && row.location + " · "}原文第 {row.start}–
                  {row.end} 字
                </small>
                <p className="preserve">{row.text}</p>
                <strong>
                  {row.matches.length
                    ? isSlides
                      ? "逐字出现于第 " + row.matches.join("、") + " 页"
                      : "逐字出现于成果正文"
                    : "未找到相同原句"}
                </strong>
                {row.suggested && (
                  <p>
                    用词最接近第 {row.suggested} 页 ·{" "}
                    {titles[row.suggested - 1]}
                  </p>
                )}
                {onFeedback && !row.matches.length && (
                  <>
                    {isSlides && (
                      <label>
                        反馈到哪一页？
                        <select
                          value={target || ""}
                          onChange={(e) =>
                            select({
                              ...selected,
                              [row.start]: Number(e.target.value),
                            })
                          }
                        >
                          <option value="">选择页</option>
                          {titles.map((title, i) => (
                            <option value={i + 1} key={i}>
                              第 {i + 1} 页 · {title}
                            </option>
                          ))}
                        </select>
                      </label>
                    )}
                    <button
                      type="button"
                      disabled={isSlides && !target}
                      onClick={() => {
                        onFeedback({
                          sourceQuote: {
                            source,
                            version: data.pin,
                            start: row.start,
                            end: row.end,
                            hash: row.hash,
                          },
                          position: isSlides
                            ? "第 " + target + " 页"
                            : "成果正文",
                          body:
                            "请对照《" +
                            data.name +
                            "》v" +
                            data.pin +
                            " 第 " +
                            row.start +
                            "–" +
                            row.end +
                            " 字，核对表述是否保留原文要求。",
                        });
                      }}
                    >
                      起草核对意见
                    </button>
                  </>
                )}
              </article>
            );
          })}
          {data.total > 12 && (
            <div className="actions">
              <button
                type="button"
                disabled={page === 1}
                onClick={() => {
                  setData(undefined);
                  setPage(page - 1);
                }}
              >
                上一组原句
              </button>
              <small>
                共 {data.total} 句 · 第 {page} 组
              </small>
              <button
                type="button"
                disabled={!data.more}
                onClick={() => {
                  setData(undefined);
                  setPage(page + 1);
                }}
              >
                下一组原句
              </button>
            </div>
          )}
        </>
      )}
    </section>
  );
}

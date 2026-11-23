import { useState } from "react";
import { api, type Project } from "./client";
import { upload, type Source } from "./knowledge";

type Pin = { id: string; version: number };
type Passage = { text: string; start: number; end: number; location?: string; preceding?: string };
type Page = { rows: Passage[]; more: boolean };

export function SourceReceipt({
  project,
  sources,
  busy,
  perform,
  reloadSources,
}: {
  project: Project;
  sources: Source[];
  busy: boolean;
  perform: (action: () => Promise<unknown>) => Promise<void>;
  reloadSources: () => Promise<void>;
}) {
  const [uploadKey, setUploadKey] = useState(() => crypto.randomUUID());
  const [selection, select] = useState<Pin | null>(null);
  const [page, setPage] = useState<Page | null>(null);
  const [cursors, setCursors] = useState([0]);
  const [quote, setQuote] = useState("");
  const [position, setPosition] = useState<number>();
  const run = project.run;
  const request = run?.receiptRequest;
  if (!run || !request) return null;
  const existing = sources.filter(
    (s) =>
      s.readable &&
      !s.revoked &&
      !project.plan?.sources.some((pin) => pin.id === s.id),
  );
  const stale =
    !!selection &&
    !existing.some(
      (s) => s.id === selection.id && s.version === selection.version,
    );
  function reset(pin: Pin | null) {
    select(pin);
    setUploadKey(crypto.randomUUID());
    setPage(null);
    setCursors([0]);
    setQuote("");
    setPosition(undefined);
  }
  async function source(data: FormData) {
    if (stale) throw new Error("原件已变化，请重新选择原件与引用。");
    if (selection) return selection;
    const file = data.get("file");
    if (!(file instanceof File) || !file.size)
      throw new Error("请选择补充原件");
    const pin = await upload(project.id, file, undefined, undefined, uploadKey);
    if (!pin.readable) throw new Error("原件已保存，请选择可读取文字的文件");
    return pin;
  }
  const route = "runs/" + run.id;
  const step = request.step;
  const lookup = request.kind === "lookup";
  async function list(form: HTMLFormElement, history: number[]) {
    const data = new FormData(form);
    await perform(async () => {
      const pin = await source(data);
      const query = new URLSearchParams({
        source: pin.id,
        version: String(pin.version),
        step: String(step),
        after: String(history[history.length - 1]),
      });
      const result = await api<Page>(route + "/receipt-passages?" + query);
      if (!selection) {
        await reloadSources();
        select(pin);
      }
      setPage(result);
      setCursors(history);
    });
  }
  return (
    <section className="card">
      <h2>{lookup ? "补充引用原件" : "补充核算原件"}</h2>
      <p>{request.reason}</p>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          const data = new FormData(event.currentTarget);
          void perform(async () => {
            const pin = await source(data);
            await api(
              route + (lookup ? "/lookup-receipts" : "/calculation-receipts"),
              "POST",
              {
                expected: run.controlRevision,
                step: request.step,
                source: pin.id,
                version: pin.version,
                excerpt: quote,
                excerptStart: position,
              },
              {
                "Idempotency-Key":
                  "receipt-" +
                  run.id +
                  "-" +
                  run.controlRevision +
                  "-" +
                  request.step +
                  "-" +
                  uploadKey,
              },
            );
          });
        }}
      >
        <fieldset disabled={busy}>
          {(existing.length > 0 || selection) && (
            <label>
              补件来源
              <select
                value={stale ? "unavailable" : selection?.id || ""}
                onChange={(e) =>
                  reset(existing.find((s) => s.id === e.target.value) || null)
                }
              >
                <option value="">上传新原件</option>
                {stale && (
                  <option value="unavailable" disabled>
                    原件已变化，请重新选择
                  </option>
                )}
                {existing.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name} · v{s.version}
                  </option>
                ))}
              </select>
            </label>
          )}
          {!selection && (
            <label>
              补充原件
              <input
                type="file"
                name="file"
                required
                onChange={() => reset(null)}
              />
            </label>
          )}
          {!stale && (
            <>
              <button
                type="button"
                onClick={(e) => void list(e.currentTarget.form!, [0])}
              >
                {lookup ? (selection ? "查看原句" : "上传并查看原句") : (selection ? "查看金额原句" : "上传并查看金额原句")}
              </button>
              {page && (
                <section className="receipt-passages" aria-label={lookup ? "原件中的可引用原句" : "原件中的金额原句"}>
                  {page.rows.length === 0 && (
                    <p>
                      {lookup ? "没有找到可选原句。" : "没有找到带币种的可选金额。"}可在下方填写最多 320 字的连续原文。
                    </p>
                  )}
                  {page.rows.map((row) => (
                    <button
                      type="button"
                      className="list"
                      key={row.start}
                      aria-pressed={position === row.start}
                      onClick={() => {
                        setQuote(row.text);
                        setPosition(row.start);
                      }}
                    >
                      {row.preceding && <small>前文 · {row.preceding}</small>}
                      <span>{row.text}</span>
                      <small>
                        {row.location && row.location + " · "}第 {row.start}–
                        {row.end} 字
                      </small>
                    </button>
                  ))}
                  {cursors.length > 1 && (
                    <button
                      type="button"
                      onClick={(e) =>
                        void list(e.currentTarget.form!, cursors.slice(0, -1))
                      }
                    >
                      上一页原句
                    </button>
                  )}
                  {page.more && (
                    <button
                      type="button"
                      onClick={(e) =>
                        void list(e.currentTarget.form!, [
                          ...cursors,
                          page.rows[page.rows.length - 1].start,
                        ])
                      }
                    >
                      下一页原句
                    </button>
                  )}
                </section>
              )}
              <label>
                {lookup ? "要引用的原句（单句原件可留空）" : "含金额的原句（单句原件可留空）"}
                <textarea
                  name="excerpt"
                  rows={2}
                  value={quote}
                  onChange={(e) => {
                    setQuote(e.target.value);
                    setPosition(undefined);
                  }}
                />
              </label>
              {position !== undefined && (
                <small>已选原句 · 第 {position} 字起</small>
              )}
            </>
          )}
          <small>
            原句用于第 {request.step}{" "}
            步。{lookup ? "有依据的回答与引用" : "算式和引用"}随成果传给依赖步骤及审核人。继续使用原任务的调用额度。
          </small>
          <button className="primary" disabled={stale}>
            {lookup ? "确认原句并继续回答" : "确认补件并继续核算"}
          </button>
        </fieldset>
      </form>
    </section>
  );
}

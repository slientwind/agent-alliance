import { useRef, useState, type RefObject } from "react";
import { api } from "./client";

export type SlideText = { title: string; body: string };
const emptyPage = () => ({ title: "", body: "" });

function displayWidth(text: string) {
  let units = 0;
  for (const c of text) {
    if (/\p{M}/u.test(c)) continue;
    units += /^[\x20-\x7e]$/.test(c) && !/[A-Zwm@%]/.test(c) ? 1 : 2;
  }
  return units;
}

function pageError(page: SlideText, field: keyof SlideText) {
  if (
    /[\u0000-\u0009\u000b-\u001f\u007f-\u009f\u2028\u2029]/u.test(page[field])
  )
    return "请使用普通文字，并在正文中使用回车换行。";
  if (field === "title")
    return !page.title.trim() || displayWidth(page.title.trim()) > 50
      ? "请填写短标题，最多 25 个全角字符。"
      : "";
  const lines = page.body
    .trim()
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
  if (!lines.length || lines.length > 7) return "本页需要 1 至 7 行正文。";
  if (lines.some((l) => l.startsWith("##"))) return "请在标题栏填写页标题。";
  if (lines.some((l) => l.includes("\t") || displayWidth(l) > 72))
    return "请将长行拆开，每行最多 36 个全角字符，并使用空格缩进。";
  return "";
}

function PageFields({
  page,
  index,
  change,
  bodyField,
}: {
  bodyField?: RefObject<HTMLTextAreaElement | null>;
  page: SlideText;
  index: number;
  change: (field: keyof SlideText, value: string) => void;
}) {
  return (
    <fieldset>
      <legend>第 {index + 1} 页</legend>
      <label>
        第 {index + 1} 页标题
        <input
          name="slideTitle"
          required
          value={page.title}
          ref={(el) => el?.setCustomValidity(pageError(page, "title"))}
          onChange={(e) => change("title", e.target.value)}
        />
      </label>
      <label>
        第 {index + 1} 页正文
        <textarea
          name="slideBody"
          required
          rows={3}
          value={page.body}
          ref={(el) => {
            el?.setCustomValidity(pageError(page, "body"));
            if (bodyField) bodyField.current = el;
          }}
          onChange={(e) => change("body", e.target.value)}
        />
      </label>
    </fieldset>
  );
}

export function SlideOutlineFields({
  initial,
  name,
  save,
}: {
  initial?: SlideText[];
  name: string;
  save?: (pages: SlideText[], text: string) => void;
}) {
  const [pages, setPages] = useState<SlideText[]>(initial || [emptyPage()]);
  const text = pages
    .map((p) => "## " + p.title.trim() + "\n" + p.body.trim())
    .join("\n\n");
  const fields = (
    <>
      <small>
        每项生成一张可编辑的幻灯片。标题最多 25 个全角字符，正文最多 7
        行，每行最多 36 个全角字符。
      </small>
      <input type="hidden" name={name} value={text} />
      {pages.map((p, index) => (
        <div key={index}>
          <PageFields
            page={p}
            index={index}
            change={(field, value) =>
              setPages((items) =>
                items.map((item, i) =>
                  i === index ? { ...item, [field]: value } : item,
                ),
              )
            }
          />
          {pages.length > 1 && (
            <button
              type="button"
              onClick={() => setPages(pages.filter((_, i) => i !== index))}
            >
              移除第 {index + 1} 页
            </button>
          )}
        </div>
      ))}
      <button
        type="button"
        disabled={pages.length >= 40}
        onClick={() => setPages([...pages, emptyPage()])}
      >
        添加一页
      </button>
    </>
  );
  if (save)
    return (
      <form
        onSubmit={(e) => {
          e.preventDefault();
          save(pages, text);
        }}
      >
        {fields}
        <button className="primary">附到当前对话</button>
      </form>
    );
  return fields;
}

export function SlideRevisionFields({
  body,
  version,
  position,
  quotePath,
}: {
  quotePath?: string;
  position: string;
  body: string;
  version: number;
}) {
  const pages = body.split("\n\n").map((block) => {
    const [heading, ...lines] = block.split("\n");
    return { title: heading.slice(3), body: lines.join("\n") };
  });
  const [base, setBase] = useState(version);
  const matched = /^第 (\d+) 页$/.exec(position);
  const target = matched ? Number(matched[1]) - 1 : -1;
  const [index, setIndex] = useState(pages[target] ? target : -1);
  const [page, setPage] = useState<SlideText>(
    () => pages[target] || emptyPage(),
  );
  const bodyField = useRef<HTMLTextAreaElement>(null);
  const latestQuote = useRef(quotePath);
  latestQuote.current = quotePath;
  const [loadingQuote, setLoadingQuote] = useState(false);
  const [quoteError, setQuoteError] = useState("");
  async function insertQuote() {
    const field = bodyField.current;
    if (!quotePath || !field || loadingQuote || version !== base) return;
    const { value, selectionStart: start, selectionEnd: end } = field;
    setLoadingQuote(true);
    setQuoteError("");
    try {
      const quote = await api<{ text: string }>(quotePath);
      if (
        field !== bodyField.current ||
        field.value !== value ||
        latestQuote.current !== quotePath
      ) {
        setQuoteError("页面或正文已变化，请重新选择插入位置。");
        return;
      }
      const body =
        end > start
          ? value.slice(0, start) + quote.text + value.slice(end)
          : [value.trimEnd(), quote.text].filter(Boolean).join("\n");
      setPage((p) => ({ ...p, body }));
      field.focus();
    } catch (e) {
      setQuoteError((e as Error).message);
    } finally {
      setLoadingQuote(false);
    }
  }
  return (
    <>
      <input name="slideBase" type="hidden" value={base} />
      <label>
        要修改哪一页？
        <select
          required
          name="slideNumber"
          value={index < 0 ? "" : index + 1}
          onChange={(e) => {
            const next = Number(e.target.value) - 1;
            setIndex(next);
            setPage(pages[next] || emptyPage());
          }}
        >
          <option value="">请选择一页</option>
          {pages.map((p, i) => (
            <option key={i} value={i + 1}>
              第 {i + 1} 页 · {p.title}
            </option>
          ))}
        </select>
      </label>
      {index >= 0 && (
        <PageFields
          key={index + "/" + version}
          bodyField={bodyField}
          page={page}
          index={index}
          change={(field, value) => setPage({ ...page, [field]: value })}
        />
      )}
      {index >= 0 && quotePath && base === version && (
        <>
          <small>选中正文可用原句替换；没有选中文字时将原句加在页末。</small>
          <button
            type="button"
            disabled={loadingQuote}
            onClick={() => void insertQuote()}
          >
            从反馈引用插入原句
          </button>
          {quoteError && <p role="alert">{quoteError}</p>}
        </>
      )}
      {base !== version && (
        <p role="alert">
          成果已更新到 v{version}，当前编辑基于 v{base}。
          <button
            type="button"
            onClick={() => {
              setBase(version);
              setIndex(-1);
              setPage(emptyPage());
            }}
          >
            重新选择当前版本的页面
          </button>
        </p>
      )}
    </>
  );
}

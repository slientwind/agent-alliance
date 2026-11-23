import { useRef, useState } from "react";

export const toolChoices = {
  docx: "Word 文档",
  aggregate: "Excel 分组汇总",
  xlsx: "Excel 逐行清单",
  extract: "原文摘录",
  pptx: "可编辑课件（PowerPoint）",
};

export type ToolChoice = keyof typeof toolChoices;

export function describeTool(
  operation: ToolChoice,
  name: string,
  fields: FormData,
) {
  const config =
    operation === "aggregate"
      ? Object.fromEntries(
          ["group", "amount", "unitColumn", "filterColumn", "filterValue"].map(
            (key) => [key, String(fields.get(key) || "")],
          ),
        )
      : { title: name };
  return {
    operation,
    config,
    contract: operation === "pptx" ? "pptx-outline-v1" : undefined,
  };
}

export function suggestedTool(request: string): ToolChoice | undefined {
  if (/powerpoint|pptx?|幻灯片|课件/i.test(request)) return "pptx";
  if (
    /汇总|合计|求和|小计/.test(request) &&
    /excel|xlsx|csv|分组|金额列/i.test(request)
  )
    return "aggregate";
  if (
    /(?:生成|整理成|导出|输出|制作|交付|写成)(?:一份|为|成|可编辑的|的|\s)*(?:word|docx)/i.test(
      request,
    )
  )
    return "docx";
  if (
    /(?:生成|整理成|导出|输出|制作)(?:一份|为|成|\s)*(?:excel|xlsx).*清单/i.test(
      request,
    )
  )
    return "xlsx";
}

const headerBytes = 65536;

// Read only the first logical record. Preserve quoted delimiters and the exact
// characters of each column name for the case-sensitive calculation contract.
async function readHeader(file: File) {
  const bytes = await file.slice(0, headerBytes).arrayBuffer();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  const text = decoder.decode(bytes, { stream: file.size > headerBytes });
  const columns: string[] = [];
  let field = "",
    quoted = false,
    closed = false,
    ended = false;
  for (let i = 0; i < text.length; i++) {
    const char = text[i];
    if (quoted) {
      if (char !== '"') field += char;
      else if (text[i + 1] === '"') {
        field += '"';
        i++;
      } else {
        quoted = false;
        closed = true;
      }
    } else if (char === ",") {
      columns.push(field);
      field = "";
      closed = false;
    } else if (char === "\n" || char === "\r") {
      ended = true;
      break;
    } else if (char === '"' && !field && !closed) {
      quoted = true;
    } else if (closed || char === '"') {
      throw new Error("表头引号格式有误，请核对 CSV。");
    } else field += char;
  }
  if (quoted || (!ended && file.size > headerBytes))
    throw new Error("未能读取完整表头，可手动填写列名。");
  columns.push(field);
  if (
    columns.some((c) => !c.trim()) ||
    new Set(columns).size !== columns.length
  )
    throw new Error("表头含空列名或重名，请核对 CSV。");
  return columns.filter((c) => !/[\r\n]/.test(c));
}

export function AggregateColumns({ files }: { files: File[] }) {
  const [headers, setHeaders] = useState<string[]>([]);
  const [status, setStatus] = useState("");
  const request = useRef(0);
  const csv = files.filter((f) => /\.csv$/i.test(f.name));
  async function suggest(file: File) {
    const sequence = ++request.current;
    setStatus("");
    setHeaders([]);
    try {
      const columns = await readHeader(file);
      if (sequence !== request.current) return;
      setHeaders(columns);
      setStatus("已读取 " + file.name + " 的列名，可在下方选择或填写。");
    } catch {
      if (sequence === request.current)
        setStatus("无法读取有效的 UTF-8 CSV 表头，请手动填写列名。");
    }
  }
  return (
    <fieldset>
      <legend>汇总口径</legend>
      {csv.length > 0 && (
        <details>
          <summary>从附件读取列名</summary>
          {csv.map((file, i) => (
            <button key={i} type="button" onClick={() => void suggest(file)}>
              {file.name}
            </button>
          ))}
          <small>仅读取本机表头，试运行使用单独填写的样例。</small>
        </details>
      )}
      {status && <small role="status">{status}</small>}
      <datalist id="aggregate-headers">
        {headers.map((h) => (
          <option key={h} value={h} />
        ))}
      </datalist>
      <label>
        分组列
        <input
          name="group"
          list="aggregate-headers"
          placeholder="例如 区域"
          required
        />
      </label>
      <label>
        金额列
        <input
          name="amount"
          list="aggregate-headers"
          placeholder="例如 已收金额"
          required
        />
      </label>
      <details>
        <summary>单位与筛选条件</summary>
        <label>
          金额单位列
          <input name="unitColumn" list="aggregate-headers" />
        </label>
        <label>
          筛选列
          <input name="filterColumn" list="aggregate-headers" />
        </label>
        <label>
          保留值
          <input name="filterValue" />
        </label>
      </details>
    </fieldset>
  );
}

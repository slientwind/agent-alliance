import { useRef, useState } from "react";
import { api, RequestError } from "./client";

type Window = { from: string; to: string };
type Receipt = { id: string; expiresAt: string };
const when = (value: string) =>
  new Date(value).toLocaleString("zh-CN", { hour12: false });

export function ManagementExport({
  id,
  scope,
  period,
  costs,
  financial = false,
}: {
  id: string;
  scope: string;
  period: Window;
  costs: boolean;
  financial?: boolean;
}) {
  const [fixed, setFixed] = useState<Window>();
  const [recent, setRecent] =
    useState<(Receipt & Window & { costs: boolean; createdAt: string })[]>();
  const [error, setError] = useState("");
  return fixed ? (
    <ExportForm
      id={id}
      scope={scope}
      financial={financial}
      fixed={fixed}
      costs={costs}
      close={() => setFixed(undefined)}
    />
  ) : (
    <>
      <button
        onClick={() => {
          setRecent(undefined);
          setError("");
          setFixed({ from: period.from, to: period.to });
        }}
      >
        {financial ? "准备导出逐笔费用" : "准备导出汇总"}
      </button>
      <button
        onClick={async () => {
          setError("");
          setRecent(undefined);
          try {
            setRecent(
              await api(
                "management-scopes/" +
                  id +
                  "/exports" +
                  (financial ? "?kind=charges" : ""),
              ),
            );
          } catch (e) {
            setError((e as Error).message);
          }
        }}
      >
        {financial ? "查看近期费用文件" : "查看近期导出"}
      </button>
      {error && <p role="alert">{error}</p>}
      {recent &&
        (recent.length === 0 ? (
          <p>暂无有效下载</p>
        ) : (
          <ul>
            {recent.map((item) => (
              <li key={item.id}>
                {when(item.from)} 至 {when(item.to)} ·{" "}
                {financial ? "逐笔费用" : item.costs ? "含费用" : "业务汇总"}
                <small>
                  {when(item.createdAt)} 生成 · 有效至 {when(item.expiresAt)}
                </small>
                <Download grant={id} receipt={item} financial={financial} />
              </li>
            ))}
          </ul>
        ))}
    </>
  );
}

function ExportForm({
  id,
  scope,
  fixed,
  financial,
  costs,
  close,
}: {
  id: string;
  scope: string;
  fixed: Window;
  financial: boolean;
  costs: boolean;
  close: () => void;
}) {
  const [receipt, setReceipt] = useState<Receipt>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const request = useRef<{ body: string; key: string } | undefined>(undefined);
  const root = "management-scopes/" + id + "/exports";
  async function generate(form: FormData) {
    setBusy(true);
    setError("");
    try {
      const period = { ...fixed };
      if (financial) {
        for (const key of ["from", "to"] as const) {
          const selected = form.get(key);
          if (selected) period[key] = new Date(String(selected)).toISOString();
        }
        if (
          Date.parse(period.from) < Date.parse(fixed.from) ||
          Date.parse(period.to) > Date.parse(fixed.to) ||
          Date.parse(period.from) >= Date.parse(period.to)
        ) {
          throw new Error("请在所示统计周期内选择有效的起止时间");
        }
      }
      const input = {
        ...period,
        ...(financial ? { costLines: true } : {}),
        recipient: form.get("recipient"),
        purpose: form.get("purpose"),
        costs: financial || form.has("costs"),
      };
      const body = JSON.stringify(input);
      if (request.current?.body !== body)
        request.current = { body, key: crypto.randomUUID() };
      setReceipt(
        await api<Receipt>(root, "POST", input, {
          "Idempotency-Key": request.current.key,
        }),
      );
    } catch (e) {
      if (e instanceof RequestError && e.status === 410)
        request.current = undefined;
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <section aria-label={financial ? "逐笔费用导出" : "管理汇总导出"}>
      <h3>{financial ? "导出逐笔费用" : "导出管理汇总"}</h3>
      <p>
        {scope} · {when(fixed.from)} 至 {when(fixed.to)}
      </p>
      <p>
        {financial
          ? "每行对应一次调用，含发起人和承担团队编号、记账时的资源名称与版本、已知 token、记账金额、剩余预估及入账记录。未知费用单列。按调用时间筛选，金额以生成时的账本为准。"
          : "JSON 文件包含范围名称、统计时间和生成时的运行、阻塞、检查、审核、采纳及交付数量。不含成员名单、任务明细或正文。"}
      </p>
      <small>接收对象与用途用于审计，文件由你下载。</small>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void generate(new FormData(e.currentTarget));
        }}
      >
        <fieldset disabled={busy || !!receipt} className="plan-fields">
          {financial && (
            <details>
              <summary>缩短导出周期</summary>
              <small>
                按本地时间填写，开始时间计入，结束时间排除。每份文件最多一万次调用。
              </small>
              <label>
                开始时间
                <input type="datetime-local" name="from" step="any" />
              </label>
              <label>
                结束时间
                <input type="datetime-local" name="to" step="any" />
              </label>
            </details>
          )}
          {costs && !financial && (
            <label>
              <input name="costs" type="checkbox" />{" "}
              加入调用次数、token、已记账金额、剩余预估、待结算与未知费用笔数
            </label>
          )}
          <label>
            接收对象
            <input name="recipient" maxLength={200} required />
          </label>
          <label>
            使用目的
            <input name="purpose" maxLength={400} required />
          </label>
        </fieldset>
        {!receipt && <button disabled={busy}>生成 JSON 文件</button>}
      </form>
      {receipt && (
        <>
          <p>下载有效至 {when(receipt.expiresAt)}，下载时再次核验当前授权。</p>
          <Download grant={id} receipt={receipt} financial={financial} />
        </>
      )}
      {error && <p role="alert">{error}</p>}
      <button disabled={busy} onClick={close}>
        关闭导出
      </button>
    </section>
  );
}

function Download({
  grant,
  receipt,
  financial,
}: {
  grant: string;
  receipt: Receipt;
  financial: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function download() {
    setBusy(true);
    setError("");
    try {
      const response = await fetch(
        "/api/management-scopes/" + grant + "/exports/" + receipt.id,
        {
          credentials: "same-origin",
          cache: "no-store",
        },
      );
      if (!response.ok) {
        const problem = await response.json().catch(() => ({}));
        throw new Error(problem.error || "下载失败，请核对授权和有效期");
      }
      const url = URL.createObjectURL(await response.blob());
      const link = document.createElement("a");
      link.href = url;
      link.download = financial ? "逐笔费用.json" : "管理汇总.json";
      link.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <button disabled={busy} onClick={() => void download()}>
        {financial ? "下载逐笔费用" : "下载管理汇总"}
      </button>
      {error && <p role="alert">{error}</p>}
    </>
  );
}

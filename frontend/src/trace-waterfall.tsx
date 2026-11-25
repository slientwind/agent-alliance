import { useEffect, useRef, useState } from "react";
import { api, statusLabel } from "./client";

type Span = { spanId: string; turn?: number; name?: string; model?: string; status: string; type: string; start?: string; end?: string; attempt?: number };
type Trace = {
  traceId: string; status: string; start: string; end?: string;
  spans: (Span & { step: number; attempts: (Span & { number: number })[] })[] | null;
  gatewaySpans?: Span[]; actionSpans?: Span[]; sandboxSpans?: Span[];
};

const kinds: Record<string, string> = { model: "模型", tool: "工具", external: "外部 Agent", sandbox: "沙箱产物导入" };
const states: Record<string, string> = { ...statusLabel, success: "完成", completed: "完成", running: "执行中", rejected: "已跳过" };

export function TraceWaterfall({ run, close }: { run: string; close: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [trace, setTrace] = useState<Trace>();
  const [error, setError] = useState("");

  useEffect(() => {
    dialog.current?.showModal();
  }, []);

  useEffect(() => {
    let active = true, sequence = 0;
    function load() {
      if (document.hidden) return;
      const ticket = ++sequence;
      api<{ trace: Trace }>(`admin/monitor/trace/${encodeURIComponent(run)}`).then(value => {
        if (!active || ticket !== sequence) return;
        setTrace(value.trace);
        setError("");
      }).catch(error => {
        if (!active || ticket !== sequence) return;
        setTrace(undefined);
        setError((error as Error).message);
      });
    }
    load();
    const timer = window.setInterval(load, 10000);
    document.addEventListener("visibilitychange", load);
    return () => {
      active = false;
      window.clearInterval(timer);
      document.removeEventListener("visibilitychange", load);
    };
  }, [run]);

  const spans = trace ? [...(trace.spans || []).flatMap<Span>(span => span.attempts.length > 1
    ? span.attempts.map(attempt => ({ ...attempt, type: span.type, name: `步骤 ${span.step} · 第 ${attempt.number} 次` }))
    : [{ ...span, name: `步骤 ${span.step}` }]), ...(trace.gatewaySpans || []), ...(trace.actionSpans || []), ...(trace.sandboxSpans || [])] : [];
  const turns = [...new Set(spans.map(span => span.turn ?? 0))].sort((a, b) => a - b);
  const start = trace ? new Date(trace.start).getTime() : 0;
  const end = Math.max(start + 1, trace?.end ? Date.parse(trace.end) : Date.now(),
    ...spans.map(span => Date.parse(span.end || span.start || "") || 0));

  function spanRow(span: Span) {
    const type = kinds[span.type] || span.type;
    const label = [type, span.model || span.name, span.attempt && `第 ${span.attempt} 次`].filter(Boolean).join(" · ");
    const since = span.start ? Date.parse(span.start) : null;
    const until = span.end ? Date.parse(span.end) : end;
    const finish = (until - start) * 100 / (end - start);
    const left = since === null ? finish : (since - start) * 100 / (end - start);
    return <li key={span.spanId}>
      <div className="row"><strong>{label}</strong><span>{states[span.status] || span.status}</span></div>
      {span.start || span.end ? <div className="trace-track" data-kind={span.type}
        title={since !== null ? `${Math.max(0, until - since)} ms` : span.end}
        aria-label={since !== null ? `约 ${Math.max(0, until - since)} 毫秒` : "时间点"}>
        <span style={{ left: `${Math.max(0, left)}%`, width: `${Math.max(0, finish - left)}%` }} />
      </div> : null}
    </li>;
  }

  return <dialog className="trace-drawer" ref={dialog} aria-labelledby="trace-drawer-title" onCancel={close}>
    <header className="row"><h2 id="trace-drawer-title">运行时间线</h2><button type="button" onClick={close}>关闭</button></header>
    {error && <p role="alert">{error}</p>}
    {!trace && !error && <p role="status">正在读取运行详情…</p>}
    {trace && <>
      <p>{states[trace.status] || trace.status} · Trace <code>{trace.traceId}</code></p>
      {turns.length === 0 && <p>该运行暂无可显示的诊断步骤。</p>}
      {turns.map(turn => <section key={turn}>
        <h3>{turn ? `第 ${turn} 轮` : "任务步骤"}</h3>
        <ol className="trace-spans">{spans.filter(span => (span.turn || 0) === turn).map(spanRow)}</ol>
      </section>)}
    </>}
  </dialog>;
}

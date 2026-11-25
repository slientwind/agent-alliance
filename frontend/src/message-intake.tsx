import { useEffect, useState } from "react";
import { api } from "./client";

type BatchSummary = { id: string; state: string; revision: number; createdAt: string };
type CaseSummary = { id: string; objectType: string; objectKey: string; intent: string; revision: number };
type Candidate = { position: number; event: number; objectType: string; objectKey: string; intent: string; evidence: string; note: string };
type Batch = BatchSummary & { events: { ordinal: number; text: string }[]; failure?: string; candidates: Candidate[]; unresolved: { event: number; reason: string }[]; cases: string[] };
type Case = CaseSummary & { evidence: { event: string; text: string }[]; history: { id: string; status: string; result: string; failure?: string }[] };

const types: Record<string, string> = {
  order: "订单", shipment: "运单", invoice: "发票", ticket: "工单", purchase: "采购", document: "文档",
};
const intents: Record<string, string> = {
  refund: "退款", return: "退货", replace: "补发或更换", delivery: "物流", billing: "费用与发票",
  inquiry: "咨询", correction: "更正", approval: "审核", other: "其他",
};
const states: Record<string, string> = { queued: "等待整理", working: "正在整理", review: "待核对", confirmed: "已归并", failed: "整理未完成" };

export function MessageIntake({ project, mayReview, revision }: { project: string; mayReview: boolean; revision: number }) {
  const base = "projects/" + project;
  const [batches, setBatches] = useState<BatchSummary[]>([]);
  const [cases, setCases] = useState<CaseSummary[]>([]);
  const [batchID, setBatchID] = useState("");
  const [batch, setBatch] = useState<Batch>();
  const [caseID, setCaseID] = useState("");
  const [detail, setDetail] = useState<Case>();
  const [ignored, setIgnored] = useState<number[]>([]);
  const [manual, setManual] = useState<Omit<Candidate, "position">[]>([]);
  const [messages, setMessages] = useState("");
  const [instruction, setInstruction] = useState("");
  const [pending, setPending] = useState<{ input: string; key: string; events?: string[] }>();
  const [serial, setSerial] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refresh = () => setSerial(value => value + 1);
  useEffect(() => {
    let active = true;
    void Promise.all([api<BatchSummary[]>(base + "/intake-batches"), api<CaseSummary[]>(base + "/intake-cases")])
      .then(([batches, cases]) => { if (active) { setBatches(batches); setCases(cases); } })
      .catch(error => { if (active) setError((error as Error).message); });
    return () => { active = false; };
  }, [base, revision, serial]);
  useEffect(() => {
    if (!batchID) return;
    let active = true;
    void api<Batch>(base + "/intake-batches/" + batchID)
      .then(value => { if (active) setBatch(value); })
      .catch(error => { if (active) { setBatch(undefined); setError((error as Error).message); } });
    return () => { active = false; };
  }, [base, batchID, revision, serial]);
  useEffect(() => {
    if (!caseID) return;
    let active = true;
    void api<Case>(base + "/intake-cases/" + caseID)
      .then(value => { if (active) setDetail(value); })
      .catch(error => { if (active) { setDetail(undefined); setError((error as Error).message); } });
    return () => { active = false; };
  }, [base, caseID, revision, serial]);

  async function change(fn: () => Promise<void>) {
    setBusy(true);
    setError("");
    try {
      await fn();
      setPending(undefined);
      refresh();
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="card">
    <form onSubmit={event => {
      event.preventDefault();
      void change(async () => {
        const rows = messages.split(/\n/).map(text => text.trim()).filter(Boolean);
        if (rows.length === 0 || rows.length > 20) throw new Error("请填写 1 至 20 条消息，每行一条。");
        const input = JSON.stringify(rows);
        const attempt = pending?.input === input && pending.events
          ? pending : { input, key: crypto.randomUUID(), events: rows.map(() => crypto.randomUUID()) };
        setPending(attempt);
        const result = await api<{ id: string }>(base + "/intake-batches", "POST",
          { messages: rows.map((text, i) => ({ key: attempt.events![i], text })) }, { "Idempotency-Key": attempt.key });
        setMessages("");
        setBatchID(result.id);
        setIgnored([]);
        setManual([]);
      });
    }}>
      <label>待整理消息，每行一条<textarea value={messages} onChange={event => setMessages(event.target.value)} rows={5} required /></label>
      <button type="submit" disabled={busy}>整理消息</button>
    </form>
    {batches.length > 0 && <label>消息批次<select value={batchID} onChange={event => {
      setBatch(undefined); setBatchID(event.target.value); setIgnored([]); setManual([]);
    }}>
      <option value="">选择批次</option>
      {batches.map(item => <option key={item.id} value={item.id}>{new Date(item.createdAt).toLocaleString()} · {states[item.state]}</option>)}
    </select></label>}
    {batch?.id === batchID && <section aria-label="归并结果">
      <h3>{states[batch.state]}</h3>
      {batch.failure && <p role="alert">{batch.failure}</p>}
      {batch.candidates.map(item => <article key={item.position}>
        <label>
          {batch.state === "review" && mayReview && <input type="checkbox" checked={!ignored.includes(item.position)} onChange={event =>
            setIgnored(previous => event.target.checked ? previous.filter(value => value !== item.position) : [...previous, item.position])} />}
          {types[item.objectType]} {item.objectKey} · {intents[item.intent]}
        </label>
        <blockquote>{item.evidence}</blockquote>
      </article>)}
      {batch.unresolved.map(item => {
        const source = batch.events.find(event => event.ordinal === item.event);
        return <details key={item.event}>
          <summary>第 {item.event} 条待核对 · {item.reason}</summary>
          {source && <blockquote className="preserve">{source.text}</blockquote>}
          {manual.filter(entry => entry.event === item.event).map(entry => (
            <p key={entry.objectKey + "/" + entry.intent}>{types[entry.objectType]} {entry.objectKey} · {intents[entry.intent]}</p>
          ))}
          {batch.state === "review" && mayReview && <form onSubmit={event => {
            event.preventDefault();
            const values = new FormData(event.currentTarget);
            const evidence = String(values.get("evidence") ?? "");
            const objectKey = String(values.get("key") ?? "").trim();
            if (!source?.text.includes(evidence) || !evidence.toUpperCase().includes(objectKey.toUpperCase())) {
              setError("请从原文复制一段包含对象编号的依据。");
              return;
            }
            const entry = { event: item.event, objectType: String(values.get("type")), objectKey,
              intent: String(values.get("intent")), evidence, note: "" };
            setManual(previous => [...previous.filter(value => value.event !== entry.event ||
              value.objectKey !== entry.objectKey || value.intent !== entry.intent), entry]);
            setError("");
          }}>
            <label>对象类别<select name="type">{Object.entries(types).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
            <label>对象编号<input name="key" required maxLength={64} /></label>
            <label>诉求<select name="intent">{Object.entries(intents).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
            <label>原文依据<textarea name="evidence" required maxLength={500} rows={2} /></label>
            <button type="submit" disabled={busy}>添加归并事项</button>
          </form>}
        </details>;
      })}
      {batch.state === "review" && mayReview && <button type="button" disabled={busy} onClick={() => void change(async () => {
        await api(base + "/intake-batches/" + batch.id + "/confirm", "POST",
          { expected: batch.revision, positions: batch.candidates.filter(item => !ignored.includes(item.position)).map(item => item.position), manual });
      })}>确认所选归并</button>}
    </section>}
    {cases.length > 0 && <label>已归并事项<select value={caseID} onChange={event => { setDetail(undefined); setCaseID(event.target.value); setInstruction(""); }}>
      <option value="">选择事项</option>
      {cases.map(item => <option key={item.id} value={item.id}>{types[item.objectType]} {item.objectKey} · {intents[item.intent]}</option>)}
    </select></label>}
    {detail?.id === caseID && <section aria-label="事项进展">
      {detail.evidence.map(item => <blockquote key={item.event}>{item.text}</blockquote>)}
      {detail.history.map(item => <p className="preserve" key={item.id}>{item.result || item.failure || "正在处理此事项…"}</p>)}
      <form onSubmit={event => {
        event.preventDefault();
        void change(async () => {
          const input = JSON.stringify({ expected: detail.revision, instruction });
          const attempt = pending?.input === input ? pending : { input, key: crypto.randomUUID() };
          setPending(attempt);
          await api(base + "/intake-cases/" + caseID + "/continue", "POST", JSON.parse(attempt.input), { "Idempotency-Key": attempt.key });
          setInstruction("");
        });
      }}>
        <label>补充这项工作的要求<textarea value={instruction} onChange={event => setInstruction(event.target.value)} rows={3} required /></label>
        <button type="submit" disabled={busy}>继续此事项</button>
      </form>
    </section>}
    {error && <p role="alert">{error}</p>}
  </div>;
}

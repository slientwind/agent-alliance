import { useRef, useState } from "react";

export function RemoteInputForm({
  destination,
  question,
  send,
}: {
  destination: string;
  question?: string;
  send: (input: string, key: string) => Promise<boolean>;
}) {
  const pending = useRef<{ input: string; key: string }>(undefined);
  const [busy, setBusy] = useState(false);
  return (
    <form aria-label="补充原远端任务" onSubmit={async event => {
      event.preventDefault();
      if (busy) return;
      const form = event.currentTarget;
      const input = String(new FormData(form).get("input") || "").trim();
      if (!input) return;
      if (pending.current?.input !== input) pending.current = { input, key: crypto.randomUUID() };
      setBusy(true);
      try {
        if (await send(input, pending.current.key)) {
          form.reset();
          pending.current = undefined;
        }
      } finally {
        setBusy(false);
      }
    }}>
      {question && <blockquote className="preserve">{question}</blockquote>}
      <label>
        发给{destination}的补件文字
        <textarea name="input" required maxLength={4000} rows={3} />
      </label>
      <label>
        <input name="authorized" type="checkbox" required />授权向原远端任务发送一次，费用待核算
      </label>
      <button type="submit" disabled={busy}>提交补件</button>
    </form>
  );
}

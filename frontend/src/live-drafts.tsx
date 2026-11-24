export type DraftFragment = {
  step: number;
  attempt: number;
  offset: number;
  text: string;
  status: string;
};

export type SessionDrafts = { run: string; drafts: DraftFragment[] };

const encoder = new TextEncoder();

export function applyDraftDelta(current: DraftFragment[], fragment: DraftFragment): DraftFragment[] | null {
  const draft = current.find(item => item.step === fragment.step && item.attempt === fragment.attempt);
  const size = encoder.encode(draft?.text ?? "").length;
  const end = fragment.offset + encoder.encode(fragment.text).length;
  if (fragment.offset < size && end <= size) return current;
  if (fragment.offset !== size) return null;
  const combined = { ...fragment, text: (draft?.text ?? "") + fragment.text };
  return [...current.filter(item => item.step !== fragment.step), combined].sort((a, b) => a.step - b.step);
}

export function LiveDrafts({ drafts }: { drafts: DraftFragment[] }) {
  return drafts.map(draft => (
    <section className="bubble" key={draft.step + ":" + draft.attempt} aria-label="回复草稿">
      <p role="status">{draft.status === "unknown"
        ? "生成中断 · 保留已生成的草稿"
        : draft.status === "completed" ? "生成结束 · 等待核对" : "正在生成 · 草稿"}</p>
      <div>{draft.text}</div>
    </section>
  ));
}

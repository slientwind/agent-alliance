import { useEffect, useState } from "react";

type Fragment = {
  step: number;
  attempt: number;
  offset: number;
  text: string;
  status: string;
};

export function LiveDrafts({ project, run }: { project: string; run: string }) {
  const [drafts, setDrafts] = useState<Fragment[]>([]);
  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    const url = "/api/projects/" + encodeURIComponent(project) + "/runs/" + encodeURIComponent(run) + "/draft-events";
    const encoder = new TextEncoder();
    let source: EventSource;
    let current: Fragment[] = [];
    function connect() {
      const connection = new EventSource(url);
      source = connection;
      const replace = (next: Fragment[]) => {
        if (connection !== source) return;
        current = next;
        setDrafts(next);
      };
      connection.addEventListener("draft.snapshot", (event) => {
        replace(JSON.parse(event.data));
      });
      connection.addEventListener("draft.delta", (event) => {
        if (connection !== source) return;
        const fragment = JSON.parse(event.data) as Fragment;
        const index = current.findIndex((item) => item.step === fragment.step && item.attempt === fragment.attempt);
        const draft = current[index];
        const size = encoder.encode(draft?.text ?? "").length;
        const end = fragment.offset + encoder.encode(fragment.text).length;
        if (fragment.offset < size && end <= size) return;
        if (fragment.offset !== size) {
          replace([]);
          connection.close();
          connect();
          return;
        }
        const next = current.filter((item) => item.step !== fragment.step || item.attempt === fragment.attempt);
        const combined = { ...fragment, text: (draft?.text ?? "") + fragment.text };
        replace([...next.filter((item) => item !== draft), combined].sort((a, b) => a.step - b.step));
      });
      connection.addEventListener("access-ended", () => {
        replace([]);
        connection.close();
      });
      connection.onerror = () => {
        if (connection.readyState === EventSource.CLOSED) replace([]);
      };
    }
    connect();
    return () => source.close();
  }, [project, run]);

  return drafts.map((draft) => (
    <section className="bubble" key={draft.step + ":" + draft.attempt} aria-label="回复草稿">
      <p role="status">{draft.status === "unknown" ? "生成中断 · 保留已生成的草稿" : "正在生成 · 草稿"}</p>
      <div>{draft.text}</div>
    </section>
  ));
}

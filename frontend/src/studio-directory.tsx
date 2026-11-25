import { useEffect, useState, type FormEvent } from "react";
import { api } from "./client";
import { type AgentTeam } from "./agent-team";
import { scopeLabel, type PublishedAssistant } from "./studio-client";

type Summary = { id: string; team: string; revision: number; name: string };

export function StudioDirectory({ teams, preferredTeam, open, use }: {
  teams: AgentTeam[];
  preferredTeam: string;
  open: (id: string) => void;
  use: (assistant: PublishedAssistant) => void;
}) {
  const [team, setTeam] = useState("");
  const [drafts, setDrafts] = useState<Summary[]>([]);
  const [published, setPublished] = useState<PublishedAssistant[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const selected = teams.some(item => item.id === team) ? team
    : teams.some(item => item.id === preferredTeam) ? preferredTeam
      : teams.find(item => item.default)?.id || (teams.length === 1 ? teams[0].id : "");
  useEffect(() => {
    let live = true;
    Promise.all([api<Summary[]>("studio"), api<PublishedAssistant[]>("studio/published")])
      .then(([drafts, published]) => { if (live) { setDrafts(drafts); setPublished(published); } })
      .catch(error => { if (live) setError((error as Error).message); })
      .finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, []);

  async function create(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const draft = await api<Summary>("studio", "POST", { team: selected });
      open(draft.id);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="studio-directory">
      {error && <p role="alert">{error}</p>}
      <form className="row" onSubmit={create}>
        <label>所属团队<select required value={selected} disabled={busy} onChange={event => setTeam(event.target.value)}>
          <option value="">请选择</option>
          {teams.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}
        </select></label>
        <button type="submit" className="primary" disabled={!selected || busy}>创建助手</button>
      </form>
      {loading && <p role="status">正在读取助手…</p>}
      {!!drafts.length && <section aria-label="我的草稿">
        <h2>我的草稿</h2>
        <ul>{drafts.map(draft => <li key={draft.id}>
          <button type="button" onClick={() => open(draft.id)}>{draft.name || "未命名助手"}</button>
        </li>)}</ul>
      </section>}
      {!!published.length && <section aria-label="可以使用的助手">
        <h2>可以使用的助手</h2>
        <ul>{published.map(assistant => <li className="card" key={assistant.id}>
          <strong>{assistant.name}</strong> · {scopeLabel[assistant.scope]}
          {assistant.purpose && <p>{assistant.purpose}</p>}
          <button type="button" onClick={() => use(assistant)}>在对话中使用</button>
        </li>)}</ul>
      </section>}
    </div>
  );
}

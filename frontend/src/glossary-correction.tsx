import { useState, type FormEvent } from "react";
import { api } from "./client";

export function GlossaryCorrection({ project, message }: { project: string; message: number }) {
  const [draft, setDraft] = useState("");
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const term = String(data.get("term") ?? "").trim();
    const definition = String(data.get("definition") ?? "").trim();
    const aliases = String(data.get("aliases") ?? "").split(/[,，、]/).map(value => value.trim()).filter(Boolean);
    setBusy(true);
    setError("");
    try {
      await api("projects/" + project + "/glossary", "POST", { id: draft, messageSeq: message, term, aliases, definition });
      setDraft("");
      setSaved(true);
    } catch (error) {
      setError((error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return <div className="glossary-correction">
    {!draft && <button type="button" onClick={() => { setSaved(false); setDraft(crypto.randomUUID()); }}>
      沉淀至团队口径词典
    </button>}
    {draft && <form aria-label="保存团队术语" onSubmit={save}>
      <label>术语<input name="term" required maxLength={120} /></label>
      <label>同义词<input name="aliases" placeholder="用逗号分隔" /></label>
      <label>口径说明<textarea name="definition" required maxLength={2000} rows={3} /></label>
      {error && <p role="alert">{error}</p>}
      <div className="row">
        <button type="submit" className="primary" disabled={busy}>保存团队术语</button>
        <button type="button" disabled={busy} onClick={() => { setDraft(""); setError(""); }}>取消</button>
      </div>
    </form>}
    {saved && <small role="status">已保存到团队口径词典</small>}
  </div>;
}

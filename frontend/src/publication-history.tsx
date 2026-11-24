import { useEffect, useState } from "react";
import { BuildReview, buildSelection } from "./agent-build";
import { api, type Agent } from "./client";

type History = {
  generation: number;
  currentRevision: number;
  currentRecorded: boolean;
  versions: {
    revision: number;
    name: string;
    output: string;
    publishedAt: string;
    ready: boolean;
    requiresModelBatch: boolean;
    priorRevision?: number;
    restoreReason?: string;
  }[];
};

export function PublicationHistory({ agent, refresh }: { agent: Agent; refresh: () => Promise<void> }) {
  const [open, setOpen] = useState(false);
  const [history, setHistory] = useState<History>();
  const [selected, setSelected] = useState(0);
  const [reason, setReason] = useState("");
  const [loadError, setLoadError] = useState("");
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const [epoch, reload] = useState(0);
  const path = "agents/" + agent.id;

  useEffect(() => {
    if (!open) return;
    let active = true;
    setHistory(undefined);
    setLoadError("");
    void api<History>(path + "/publication-history").then(
      result => { if (active) setHistory(result); },
      failure => { if (active) setLoadError((failure as Error).message); },
    );
    return () => { active = false; };
  }, [path, agent.published, epoch, open]);

  const current = history?.versions.find(v => v.revision === history.currentRevision);
  const alternatives = history?.versions.filter(v => v.revision !== history.currentRevision) ?? [];
  const target = alternatives.find(v => v.revision === selected);

  async function restore(selection: ReturnType<typeof buildSelection>) {
    if (!history || !target) return;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await api(path + "/publication-restore", "POST", {
        generation: history.generation, revision: target.revision, reason: reason.trim(), ...selection,
      });
      setNotice("当前发布版本已恢复为 v" + target.revision + "。");
      setSelected(0);
      setReason("");
      await refresh();
    } catch (failure) {
      setError((failure as Error).message);
    } finally {
      reload(v => v + 1);
      setBusy(false);
    }
  }

  return (
    <details open={open} onToggle={event => setOpen(event.currentTarget.open)}>
      <summary>发布历史与恢复</summary>
      {loadError && <p role="alert">{loadError}</p>}
      {error && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
      {open && !history && !loadError && <p role="status">正在读取发布历史</p>}
      {open && history && (
        <>
          <p>当前发布 v{history.currentRevision} {current && `· ${current.name} · ${current.output}`}</p>
          {current?.restoreReason && <p>从 v{current.priorRevision} 恢复的原因 · {current.restoreReason}</p>}
          {!history.currentRecorded && <p>当前版本早于发布清单功能。发布有清单的新修订后，才可恢复已记录的历史版本。</p>}
          {history.currentRecorded && alternatives.length === 0 && <p>暂无可对照的历史修订。</p>}
          {history.currentRecorded && alternatives.length > 0 && (
            <label>
              对照历史修订
              <select value={target?.revision ?? ""} disabled={busy} onChange={event => {
                setSelected(Number(event.target.value));
                setReason("");
                setNotice("");
                setError("");
              }}>
                <option value="">选择修订</option>
                {alternatives.map(v => (
                  <option key={v.revision} value={v.revision}>v{v.revision} · {v.name} · {v.output}</option>
                ))}
              </select>
            </label>
          )}
          {target && history.currentRecorded && (
            <>
              <p>该版上次发布于 {new Date(target.publishedAt).toLocaleString("zh-CN")}。</p>
              {current && <BuildReview agent={agent} revision={current.revision} />}
              {target.requiresModelBatch && <p>此修订请在模型替换批次中恢复。</p>}
              {!target.ready && <p>此修订的资源、Skill 或验收证据需要重新核验。</p>}
              <BuildReview key={target.revision} agent={agent} revision={target.revision} openInitially>
                {snapshot => target.ready && !target.requiresModelBatch && snapshot.evidence.passed && (
                  <>
                    <p>恢复后，新方案将优先采用 v{target.revision}。已确认项目继续使用各自绑定的修订。</p>
                    <label>
                      恢复原因
                      <textarea value={reason} maxLength={1000} disabled={busy}
                        onChange={event => setReason(event.target.value)} />
                    </label>
                    <button type="button" disabled={busy || !reason.trim()}
                      onClick={() => void restore(buildSelection(snapshot))}>
                      确认恢复 v{target.revision}
                    </button>
                  </>
                )}
              </BuildReview>
            </>
          )}
        </>
      )}
    </details>
  );
}

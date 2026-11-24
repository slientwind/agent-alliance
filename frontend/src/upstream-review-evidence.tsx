import { useState } from "react";
import { api } from "./client";

type UpstreamEntry = {
  originVersion: number;
  step: number;
  attempt?: number;
  name?: string;
  sourceVersion?: number;
  currentVersion?: number;
  headBytes?: number;
  tailFrom?: number;
  available: boolean;
};

type UpstreamExcerpt = { start: number; end: number; text: string; next?: number };

export function UpstreamReviewEvidence({ project, artifact, version, ready, selected, choose }: {
  project: string;
  artifact: string;
  version: number;
  ready: boolean;
  selected?: number;
  choose?: (step: number) => void;
}) {
  const [entries, setEntries] = useState<UpstreamEntry[]>();
  const [excerpt, setExcerpt] = useState<{ step: number; batch: UpstreamExcerpt }>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const query = new URLSearchParams({ version: String(version) });
  const path = `projects/${project}/artifacts/${artifact}/upstream-evidence?${query}`;

  async function loadEntries() {
    if (pending || !ready) return;
    setPending(true);
    setError("");
    try {
      setEntries(await api<UpstreamEntry[]>(path));
    } catch (issue) {
      setError(issue instanceof Error ? issue.message : "暂无法核对上游依据");
    } finally {
      setPending(false);
    }
  }

  async function loadExcerpt(step: number, cursor = 0) {
    if (pending) return;
    setPending(true);
    setError("");
    setExcerpt(undefined);
    try {
      const batch = await api<UpstreamExcerpt>(path + "&step=" + step + "&cursor=" + cursor);
      setExcerpt({ step, batch });
    } catch (issue) {
      setError(issue instanceof Error ? issue.message : "暂无法打开上游摘录");
    } finally {
      setPending(false);
    }
  }

  return (
    <details onToggle={event => {
      if (event.currentTarget.open && !entries && !pending) void loadEntries();
    }}>
      <summary>{selected ? `已选上游依据 · 步骤 ${selected}` : "查看汇合输入"}</summary>
      {!ready && <p>本次汇合的原依据已失效。</p>}
      {pending && <p role="status">正在核对上游依据</p>}
      {error && <p role="alert">{error}</p>}
      {entries && (
        <>
          {entries[0]?.originVersion !== version && (
            <p>当前成果沿用 v{entries[0]?.originVersion} 的执行依据。</p>
          )}
          <ul>
            {entries.map(entry => {
              const current = excerpt?.step === entry.step ? excerpt.batch : undefined;
              return (
                <li key={entry.step}>
                  <strong>步骤 {entry.step}</strong>
                  {entry.available ? (
                    <>
                      <p>
                        {entry.name ? `${entry.name} v${entry.sourceVersion}` : `第 ${entry.attempt} 次调用的结果`}
                        {entry.currentVersion !== undefined && entry.sourceVersion !== entry.currentVersion &&
                          ` · 上游已有 v${entry.currentVersion}，本次汇合采用 v${entry.sourceVersion}`}
                        {entry.headBytes !== undefined && entry.tailFrom !== undefined && entry.headBytes < entry.tailFrom &&
                          " · 原文有省略"}
                      </p>
                      <button type="button" disabled={pending} onClick={() => void loadExcerpt(entry.step)}>查看实际送入的原文</button>
                      {current && (
                        <>
                          <blockquote className="preserve">{current.text || "该上游结果没有正文"}</blockquote>
                          {current.next !== undefined &&
                            <button type="button" disabled={pending} onClick={() => void loadExcerpt(entry.step, current.next)}>查看下一段</button>}
                          {choose && selected !== entry.step &&
                            <button type="button" onClick={() => choose(entry.step)}>将此上游结果记为本项依据</button>}
                        </>
                      )}
                    </>
                  ) : <p>原依据已变化或不可读取。</p>}
                </li>
              );
            })}
          </ul>
          {choose && !!selected && <button type="button" onClick={() => choose(0)}>移除所选上游依据</button>}
        </>
      )}
    </details>
  );
}

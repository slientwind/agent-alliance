import { useEffect, useState, type ReactNode } from "react";
import { api, type Agent } from "./client";

type BuildSnapshot = {
  build: {
    digest: string;
    definition: {
      revision: number;
      output: string;
      model?: { name: string; revision: number };
      tool?: { operation: string };
      remote?: { protocol: string };
      skills: { id: string; revision: number; name: string }[];
    };
  };
  evidence: {
    passed: boolean;
    digest: string;
    snapshot: { items: { passed: boolean; legacy: boolean }[] };
  };
  review?: { id: string; stale: boolean; legacy: boolean };
  publication?: { generation: number; buildDigest: string };
};

export function BuildReview({
  agent, openInitially = false, children,
}: {
  agent: Agent;
  openInitially?: boolean;
  children?: (build: BuildSnapshot, reload: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(openInitially);
  const [version, reload] = useState(0);
  const [result, setResult] = useState<{
    agent: Agent;
    version: number;
    build?: BuildSnapshot;
    error?: string;
  }>();
  useEffect(() => {
    if (!open) return;
    let active = true;
    void api<BuildSnapshot>("agents/" + agent.id + "/builds/" + agent.revision).then(
      build => { if (active) setResult({agent, version, build}); },
      error => { if (active) setResult({agent, version, error: (error as Error).message}); },
    );
    return () => { active = false; };
  }, [agent, open, version]);

  const current = result?.agent === agent && result.version === version ? result : undefined;
  const snapshot = current?.build;
  const definition = snapshot?.build.definition;
  const items = snapshot?.evidence.snapshot.items || [];
  const refresh = () => reload(v => v + 1);
  return (
    <details open={open} onToggle={e => setOpen(e.currentTarget.open)}>
      <summary>发布清单 · v{agent.revision}</summary>
      {open && !current && <p role="status">正在读取清单与验收证据</p>}
      {open && current?.error && <p role="alert">{current.error}</p>}
      {open && snapshot && definition && (
        <>
          <small>
            {definition.model && `模型 ${definition.model.name} v${definition.model.revision} · `}
            {definition.tool && `工具 ${definition.tool.operation} · `}
            {definition.remote && `第三方 ${definition.remote.protocol.toUpperCase()} · `}
            成果 {definition.output} · 清单 {snapshot.build.digest.slice(0, 12)}
          </small>
          {!!definition.skills.length && (
            <p>Skill · {definition.skills.map(skill => `${skill.name} v${skill.revision}`).join("、")}</p>
          )}
          <p>
            验收{snapshot.evidence.passed ? "通过" : "待完成"}
            {!!items.length && ` · ${items.filter(item => item.passed).length}/${items.length} 项`}
            {items.some(item => item.legacy) && " · 含升级前证据"}
          </p>
          {snapshot.review?.stale && (
            <p role="status">
              {snapshot.review.legacy
                ? "升级前的待审申请需要重新核对清单。"
                : "待审清单、验收证据或发布基线已变化，请更新申请。"}
            </p>
          )}
          {snapshot.publication && (
            <p>
              发布记录 #{snapshot.publication.generation}
              {snapshot.publication.buildDigest !== snapshot.build.digest && " · 定义与发布清单不一致"}
            </p>
          )}
          {children?.(snapshot, refresh)}
        </>
      )}
      {open && <button type="button" onClick={refresh}>刷新清单</button>}
    </details>
  );
}

export function buildSelection(snapshot: BuildSnapshot) {
  return { buildDigest: snapshot.build.digest, evidenceDigest: snapshot.evidence.digest };
}

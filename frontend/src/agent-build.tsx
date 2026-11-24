import { useEffect, useState, type ReactNode } from "react";
import { api, type Agent } from "./client";

type RemoteOperations = {
  send: boolean;
  directText: boolean;
  query: boolean;
  asyncText: boolean;
  cancel: boolean;
  supplement?: boolean;
  supplementText?: boolean;
};

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
    snapshot: {
      items: { passed: boolean; legacy: boolean }[];
      conditionalSkills?: { id: string; revision: number; passed: boolean }[];
    };
    remoteOperations?: RemoteOperations;
  };
  review?: { id: string; stale: boolean; legacy: boolean };
  publication?: { generation: number; buildDigest: string };
  publishedRemoteOperations?: RemoteOperations;
};

const operations = [
  ["send", "提交"],
  ["directText", "同步文本"],
  ["query", "查询原任务"],
  ["asyncText", "异步取回文本"],
  ["cancel", "取消确认"],
  ["supplement", "向原任务补件"],
  ["supplementText", "补件后直接取得文本"],
] as const;

export function BuildReview({
  agent, revision = agent.revision, openInitially = false, children,
}: {
  agent: Agent;
  revision?: number;
  openInitially?: boolean;
  children?: (build: BuildSnapshot, reload: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(openInitially);
  const [version, reload] = useState(0);
  const [result, setResult] = useState<{
    agent: Agent;
    revision: number;
    version: number;
    build?: BuildSnapshot;
    error?: string;
  }>();
  useEffect(() => {
    if (!open) return;
    let active = true;
    void api<BuildSnapshot>("agents/" + agent.id + "/builds/" + revision).then(
      build => { if (active) setResult({agent, revision, version, build}); },
      error => { if (active) setResult({agent, revision, version, error: (error as Error).message}); },
    );
    return () => { active = false; };
  }, [agent, revision, open, version]);

  const current = result?.agent === agent && result.revision === revision && result.version === version ? result : undefined;
  const snapshot = current?.build;
  const definition = snapshot?.build.definition;
  const items = snapshot?.evidence.snapshot.items || [];
  const refresh = () => reload(v => v + 1);
  return (
    <details open={open} onToggle={e => setOpen(e.currentTarget.open)}>
      <summary>发布清单 · v{revision}</summary>
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
          {!!snapshot.evidence.snapshot.conditionalSkills?.length && (
            <section aria-label="按需 Skill 验收">
              <strong>按需 Skill 验收</strong>
              <ul>
                {snapshot.evidence.snapshot.conditionalSkills.map(skill => (
                  <li key={skill.id}>
                    {definition.skills.find(binding => binding.id === skill.id)?.name} v{skill.revision} · {skill.passed ? "样例已通过" : "待试跑命中样例"}
                  </li>
                ))}
              </ul>
            </section>
          )}
          {snapshot.evidence.remoteOperations && (
            <section aria-label="外部操作验收">
              <strong>{snapshot.publishedRemoteOperations ? "已发布的操作范围" : "此修订的操作验收"}</strong>
              <ul>
                {operations.map(([key, label]) => (
                  <li key={key}>
                    {label} · {(snapshot.publishedRemoteOperations ?? snapshot.evidence.remoteOperations)?.[key] ? "证据有效" : "待核验"}
                  </li>
                ))}
              </ul>
              {agent.published === revision && !snapshot.publishedRemoteOperations && (
                <small>旧版发布沿用原操作范围。可创建新修订并按项试跑后发布。</small>
              )}
              {snapshot.publishedRemoteOperations && (
                <small>新增操作的试跑可用于下一修订，当前发布沿用原验收范围。</small>
              )}
            </section>
          )}
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

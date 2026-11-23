const labels = {
  queued: "待派发",
  running: "执行中",
  blocked: "执行阻塞",
  reconcile: "待核对回执",
  paused: "已暂停",
  completed: "执行完成",
  failed: "执行失败",
  cancelled: "已取消",
  other: "其他状态",
} as const;
export type RunStates = Partial<Record<keyof typeof labels, number>>;

export function RunStateSummary({ groups }: { groups: RunStates[] }) {
  const keys = Object.keys(labels) as (keyof typeof labels)[];
  const count = (key: keyof typeof labels) =>
    groups.reduce((n, g) => n + (g[key] ?? 0), 0);
  const counts = keys.map((key) => ({
    key,
    label: labels[key],
    value: count(key),
  }));
  return (
    <details>
      <summary>
        运行状态分布 · 执行中 {count("running")} ·{" "}
        {count("failed") > 0 ? (
          <strong>执行失败 {count("failed")}</strong>
        ) : (
          "执行失败 0"
        )}
      </summary>
      <p>
        按运行的发起时间归入周期，每次运行只计入一个当前状态。回执未明的运行计入待核对。
      </p>
      <dl className="usage-outcomes" aria-label="各运行状态的数量">
        {counts
          .filter((item) => item.key !== "other" || item.value > 0)
          .map(({ key, label, value }) => (
            <div key={key}>
              <dt>{label}</dt>
              <dd>
                {value > 0 && ["failed", "blocked", "reconcile"].includes(key) ? (
                  <strong>{value}</strong>
                ) : (
                  value
                )}
              </dd>
            </div>
          ))}
      </dl>
    </details>
  );
}

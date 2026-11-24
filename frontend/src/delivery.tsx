import { held, distributionLabel } from "./distribution";
import { useState } from "react";
import { api, type Project, type DeliveryCoverage, type DeliveryEdition } from "./client";

const fields = [
  ["purpose", "成果用途"],
  ["instructions", "使用说明"],
  ["limitations", "已接受的使用限制"],
] as const;
type Handoff = Partial<Record<(typeof fields)[number][0], string>>;
type Item = { step: number; name: string; version: number };
type SealedItem = Item & { dependencies: Item[] };

function Contents({ items }: { items: SealedItem[] }) {
  return (
    <ol>
      {items.map((item) => (
        <li key={item.step}>
          <strong>
            {item.name} · v{item.version}
          </strong>
          {item.dependencies.length > 0 && (
            <small>
              依赖{" "}
              {item.dependencies
                .map((d) => `步骤 ${d.step} · ${d.name} v${d.version}`)
                .join("、")}
            </small>
          )}
        </li>
      ))}
    </ol>
  );
}

export function DeliveryConfirmation({
  project,
  busy,
  hasDraft,
  partial,
  confirm,
}: {
  project: Project;
  busy: boolean;
  hasDraft: boolean;
  partial: boolean;
  confirm: (handoff: Handoff) => void;
}) {
  const accepted = new Set(project.deliveryCoverage?.items.filter(item => item.state === "covered").map(item => item.artifact));
  const items = project.artifacts.filter(a => !partial || accepted.has(a.id)).map((a) => ({
    step: a.ordinal,
    name: a.name,
    version: a.version,
  }));
  const contents = items.map((item) => ({
    ...item,
    dependencies: items.filter((upstream) =>
      project.plan?.nodes[item.step - 1]?.dependsOn.includes(upstream.step),
    ),
  }));
  return (
    <form
      className="card"
      aria-label="最终交付清单"
      onSubmit={(e) => {
        e.preventDefault();
        const data = new FormData(e.currentTarget);
        confirm(
          Object.fromEntries(fields.map(([key]) => [key, data.get(key) || ""])),
        );
      }}
    >
      <h2>最终交付清单</h2>
      <Contents items={contents} />
      {partial && <DeliveryGaps coverage={project.deliveryCoverage} />}
      <details>
        <summary>给接收人的交付说明（选填）</summary>
        <p>以下内容随成果包交付，拥有下载权限的接收人可以查看。</p>
        {fields.map(([key, label]) => (
          <label key={key}>
            {label}
            <textarea
              name={key}
              defaultValue={project.deliveries[0]?.handoff[key] || ""}
              rows={2}
              maxLength={2000}
            />
          </label>
        ))}
      </details>
      {hasDraft && (
        <p role="status">
          本页还有未提交的修订草稿。确认交付会固定以上已审核版本。
        </p>
      )}
      <button
        id={"deliver-" + project.revision}
        className="primary"
        disabled={busy}
      >
        {partial ? "确认部分交付" : project.recoveryUntil ? "确认交付并下载成果包" : "确认并固定以上版本"}
      </button>
    </form>
  );
}

function DeliveryGaps({ coverage }: { coverage?: DeliveryCoverage | null }) {
  if (!coverage) return null;
  const missing = coverage.items.filter(item => item.state === "missing");
  if (!missing.length) return null;
  return (
    <section>
      <h3>仍待补齐的必交项</h3>
      <ul>
        {missing.map(item => (
          <li key={item.item}>
            <strong>{item.label}</strong> · {item.reason}
            <p>接手人 {item.responsibleName} · {item.continueWhen}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function DeliveryHistory({
  base,
  project,
  busy,
  revise,
}: {
  base: string;
  project: Project;
  busy: boolean;
  revise: (data: {
    artifact: string;
    version: number;
    reason: string;
    edition: number;
    expected: number;
  }) => void;
}) {
  const [choice, setChoice] = useState<number>();
  const latest = project.deliveries[0];
  if (!latest) return null;
  const selected =
    project.deliveries.find((d) => d.edition === choice) || latest;
  const revisable = project.artifacts.filter(a => project.deliveryRevisionTargets?.includes(a.id));
  const suffix = "?edition=" + selected.edition;
  return (
    <>
      {project.deliveries.length > 1 && (
        <label>
          已确认的交付批次
          <select
            value={selected.edition}
            onChange={(e) => setChoice(Number(e.target.value))}
          >
            {project.deliveries.map((d) => (
              <option key={d.id} value={d.edition}>
                第 {d.edition} 次交付{d.isPartial ? " · 部分" : ""} · {new Date(d.time).toLocaleString()}
              </option>
            ))}
          </select>
        </label>
      )}
      {selected.isPartial && <p role="status"><strong>已部分交付</strong></p>}
      {selected.distribution !== "clear" && (
        <p role="status">
          <strong>{distributionLabel[selected.distribution]}</strong> · 第{" "}
          {selected.edition} 次交付
        </p>
      )}
      <DeliveryReceipt
        key={selected.id + "-" + project.revision}
        base={base}
        selected={selected}
      />
      {!held(selected.distribution) && (
        <div className="actions">
          <a href={"/api/" + base + "/delivery/package" + suffix} download>
            下载交付包（ZIP）
          </a>
          <a href={"/api/" + base + "/delivery" + suffix} download>
            下载交付清单与成果正文
          </a>
        </div>
      )}
      {project.canReview &&
        project.stage === "delivered" &&
        revisable.length > 0 &&
        selected.id === latest.id && (
          <details className="card">
            <summary>{latest.isPartial ? "补齐缺项或修订已交付成果" : "发起交付后修订"}</summary>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const data = new FormData(e.currentTarget);
                const item = revisable.find(
                  (a) => a.id === data.get("artifact"),
                );
                if (item)
                  revise({
                    artifact: item.id,
                    version: item.version,
                    reason: String(data.get("reason")),
                    edition: latest.edition,
                    expected: project.revision,
                  });
              }}
            >
              <label>
                {latest.isPartial ? "要补齐或修改的成果" : "需要修改的已交付成果"}
                <select name="artifact">
                  {revisable.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name} · v{a.version}
                    </option>
                  ))}
                </select>
              </label>
              <label>
                修订原因
                <textarea name="reason" maxLength={4000} required />
              </label>
              <small>
                修订会生成一条待办。可人工修改或另行授权 Agent
                改稿，已确认批次的下载依当前分发状态开放。
              </small>
              <button disabled={busy}>创建修订待办</button>
            </form>
          </details>
        )}
    </>
  );
}
type VersionChange = {
  artifact: string;
  name: string;
  step: number;
  from: number;
  to: number;
  bodyChanged: boolean;
  fileChanged: boolean;
  dependenciesChanged: boolean;
};
function EditionDifference({
  base,
  change,
}: {
  base: string;
  change: VersionChange;
}) {
  const [versions, setVersions] =
    useState<{ version: number; body: string; file: string | null }[]>();
  const [error, setError] = useState("");
  const path = base + "/artifacts/" + change.artifact;
  async function load() {
    setError("");
    try {
      const result = await Promise.all(
        [change.from, change.to].map((v) =>
          api<{ version: number; body: string; file: string | null }[]>(
            path + "/versions?version=" + v,
          ),
        ),
      );
      setVersions(result.map((rows) => rows[0]));
    } catch (e) {
      setError((e as Error).message);
    }
  }
  const kinds =
    [
      change.bodyChanged && "正文或摘要变更",
      change.fileChanged && "文件变更",
      change.dependenciesChanged && "上游引用换版",
    ]
      .filter(Boolean)
      .join("、") || "复核换版";
  return (
    <details
      onToggle={(e) => {
        if (e.currentTarget.open && !versions) void load();
      }}
    >
      <summary>
        步骤 {change.step} · {change.name}：v{change.from} → v{change.to} ·{" "}
        {kinds}
      </summary>
      {versions?.map((v) => (
        <section key={v.version}>
          <strong>v{v.version}</strong>
          <p className="preserve">{v.body}</p>
          {v.file && (
            <a download href={"/api/" + path + "/file?version=" + v.version}>
              下载 v{v.version} · {v.file}
            </a>
          )}
        </section>
      ))}
      {error && (
        <p role="alert">
          {error} <button onClick={load}>重试</button>
        </p>
      )}
    </details>
  );
}

function DeliveryReceipt({
  base,
  selected,
}: {
  base: string;
  selected: DeliveryEdition;
}) {
  const [receipt, setReceipt] = useState<{
    handoff: Handoff;
    items: SealedItem[];
    changes: VersionChange[];
    changedNotes: string[];
    unchanged?: number | null;
    comparisonHeld: boolean;
    distribution: import("./client").DistributionState;
    coverage?: DeliveryCoverage | null;
  }>();
  const [error, setError] = useState("");
  async function load() {
    setError("");
    try {
      setReceipt(
        await api(base + "/delivery/receipt?edition=" + selected.edition),
      );
    } catch (e) {
      setError((e as Error).message);
    }
  }
  return (
    <details
      className="card"
      onToggle={(e) => {
        if (e.currentTarget.open && !receipt) void load();
      }}
    >
      <summary>查看已确认的交付清单</summary>
      {receipt && (
        <>
          <Contents items={receipt.items} />
          {selected.isPartial && <DeliveryGaps coverage={receipt.coverage} />}
          {receipt.comparisonHeld && (
            <p>前一批次已暂停分发，版本内容对照暂时关闭。</p>
          )}
          {selected.edition > 1 &&
            !receipt.comparisonHeld &&
            !held(receipt.distribution) && (
              <section>
                <h3>与第 {selected.edition - 1} 次交付比较</h3>
                {receipt.changes.map((c) => (
                  <EditionDifference key={c.artifact} base={base} change={c} />
                ))}
                <small>
                  沿用 {receipt.unchanged ?? receipt.items.length - receipt.changes.length}{" "}
                  项已审核版本
                </small>
                {receipt.changedNotes.length > 0 && (
                  <p>
                    交付说明已调整 ·{" "}
                    {fields
                      .filter(([key]) => receipt.changedNotes.includes(key))
                      .map(([, label]) => label)
                      .join("、")}
                  </p>
                )}
              </section>
            )}
          {fields.map(
            ([key, label]) =>
              receipt.handoff[key] && (
                <p key={key} className="preserve">
                  <strong>{label}</strong>
                  <br />
                  {receipt.handoff[key]}
                </p>
              ),
          )}
        </>
      )}
      {error && (
        <p role="alert">
          {error} <button onClick={load}>重试</button>
        </p>
      )}
    </details>
  );
}

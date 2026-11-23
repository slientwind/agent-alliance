import { useEffect, useState } from "react";
import { api, statusLabel, type Project, type Artifact } from "./client";
import type { Source } from "./knowledge";
import { isPictureName } from "./attachment-data";
import { FilePreview } from "./file-preview";

type History = {
  current: number;
  projectRevision: number;
  revoked: boolean;
  recycledAt?: string | null;
  purgedAt?: string | null;
  originallyRevoked?: boolean;
  versions: {
    version: number;
    name: string | null;
    author: string;
    time: string;
    size: number;
    readable: boolean;
  }[];
  plans: { plan: number; pinned: number; current: boolean }[];
  runs: { id: string; plan: number; pinned: number; status: string }[];
  artifacts: {
    id: string;
    step: number;
    name: string;
    version: number;
    pinned: number;
    delivered: number | null;
    changed: boolean;
    decision: string | null;
  }[];
  assessments: {
    artifact: string;
    version: number;
    pinned: number;
    observed: number;
    revoked: boolean;
    decision: string;
    reason: string | null;
    actor: string;
  }[];
};
export function SourceHistory({
  project,
  source,
  reload,
  onUpdate,
}: {
  project: string;
  source: Source;
  reload: () => Promise<void>;
  onUpdate: (file: File, version: number, revision: number) => Promise<void>;
}) {
  const [open, setOpen] = useState(false),
    [data, setData] = useState<History | null>(null),
    [confirm, setConfirm] = useState<{
      current: number;
      projectRevision: number;
      file?: File;
    } | null>(null),
    [request, retry] = useState(0),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const path = "projects/" + project + "/sources/" + source.id;
  useEffect(() => {
    if (!open) return;
    let live = true,
      seq = 0;
    const load = async () => {
      const n = ++seq;
      try {
        const res = await api<History>(path + "/history");
        if (live && n === seq) {
          setData(res);
          setError("");
        }
      } catch (e) {
        if (live && n === seq) {
          setData(null);
          setError((e as Error).message);
        }
      }
    };
    void load();
    window.addEventListener("focus", load);
    return () => {
      live = false;
      window.removeEventListener("focus", load);
    };
  }, [
    open,
    path,
    source.version,
    source.revoked,
    source.recycledAt,
    source.purgedAt,
    request,
  ]);
  async function apply() {
    if (!confirm) return;
    setBusy(true);
    try {
      if (confirm.file)
        await onUpdate(confirm.file, confirm.current, confirm.projectRevision);
      else
        await api(
          path +
            "?version=" +
            confirm.current +
            "&expectedProject=" +
            confirm.projectRevision,
          "DELETE",
        );
      setData(null);
      setConfirm(null);
      await reload();
    } catch (e) {
      setError((e as Error).message);
      setConfirm(null);
      await reload();
    } finally {
      setBusy(false);
    }
  }
  return (
    <details
      onToggle={(e) => {
        setOpen(e.currentTarget.open);
        if (!e.currentTarget.open) {
          setData(null);
          setConfirm(null);
        }
      }}
    >
      <summary>资料版本与引用影响</summary>
      {data && (
        <>
          <h3>版本记录</h3>
          <ol>
            {data.versions.map((v) => (
              <li key={v.version}>
                <strong>
                  v{v.version} ·{" "}
                  {v.name ||
                    (data.purgedAt ? "文件名已清除" : "历史文件名未记录")}
                </strong>{" "}
                · {v.author} · {new Date(v.time).toLocaleString()} ·{" "}
                {data.purgedAt
                  ? "正文已清除"
                  : data.revoked || source.revoked
                    ? "原件访问已撤销"
                    : v.size +
                      " 字节 · " +
                      (v.readable ? "可读取文本" : "原件")}
                {!source.revoked && !data.revoked && (
                  <a href={"/api/" + path + "/content?version=" + v.version}>
                    {" "}
                    取回 v{v.version} 原件
                  </a>
                )}
              </li>
            ))}
          </ol>
          <h3>本项目引用</h3>
          {data.plans.map((p) => (
            <p key={p.plan}>
              {p.current ? "当前" : "历史"}方案 v{p.plan} · 固定资料 v{p.pinned}
            </p>
          ))}
          {!data.plans.length && <p>尚无保存的方案引用这份资料。</p>}
          {data.runs.length > 0 && (
            <details>
              <summary>关联运行 · {data.runs.length}</summary>
              <ul>
                {data.runs.map((r) => (
                  <li key={r.id}>
                    {r.id} · {statusLabel[r.status] || r.status} · 资料 v
                    {r.pinned}
                  </li>
                ))}
              </ul>
            </details>
          )}
          {data.artifacts.length > 0 && (
            <>
              <small>
                以下成果的执行步骤收到这份资料作为输入。具体内容由审核人核对。
              </small>
              <ul>
                {data.artifacts.map((a) => (
                  <li key={a.id}>
                    步骤 {a.step} · {a.name} v{a.version} · 方案资料 v{a.pinned}
                    {a.delivered ? " · 已交付 v" + a.delivered : ""}
                    {a.changed
                      ? " · " +
                        (a.decision === "retain"
                          ? "已核对沿用"
                          : a.decision === "change"
                            ? "已要求改稿"
                            : "依据有变化")
                      : ""}
                  </li>
                ))}
              </ul>
            </>
          )}
          {data.assessments.length > 0 && (
            <details>
              <summary>历次影响核对 · {data.assessments.length}</summary>
              <ol>
                {data.assessments.map((c, i) => (
                  <li key={i}>
                    {c.artifact} v{c.version} · 依据 v{c.pinned} · 核对时 v
                    {c.observed}
                    {c.revoked ? " 已撤权" : ""} ·{" "}
                    {c.decision === "retain" ? "沿用" : "要求修改"} · {c.actor}
                    <p>{c.reason ?? "关联资料已撤权，核对依据受限。"}</p>
                  </li>
                ))}
              </ol>
            </details>
          )}
          {data.purgedAt && (
            <p>
              原资料编号 {source.id}。
              {source.recoveredAs &&
                "已恢复为新资料 " + source.recoveredAs + "。"}
              版本与处理记录供追溯。
              {source.sharedFiles
                ? "另有有效引用的共用文件 " + source.sharedFiles + " 份。"
                : ""}
              备份依企业轮换周期清理。
            </p>
          )}
          {source.canMaintain && !data.purgedAt && (
            <SourceRetention
              key={path + ":" + (data.recycledAt || "")}
              data={data}
              source={source}
              path={path}
              reload={async () => {
                await reload();
                retry((v) => v + 1);
              }}
            />
          )}
          {source.canMaintain &&
            !source.revoked &&
            !data.revoked &&
            (!confirm ? (
              <div>
                <label>
                  提交新版本
                  <input
                    type="file"
                    onChange={(e) => {
                      const file = e.target.files?.[0];
                      if (file)
                        setConfirm({
                          current: data.current,
                          projectRevision: data.projectRevision,
                          file,
                        });
                      e.target.value = "";
                    }}
                  />
                </label>
                <button
                  onClick={() =>
                    setConfirm({
                      current: data.current,
                      projectRevision: data.projectRevision,
                    })
                  }
                >
                  撤销此资料访问
                </button>
              </div>
            ) : (
              <div>
                <p>
                  {confirm.file
                    ? "将 " +
                      confirm.file.name +
                      " 登记为 v" +
                      (confirm.current + 1) +
                      "。已保存的方案继续引用固定版本。"
                    : "撤销这份资料全部版本的原件访问。新的依赖调用将受限，引用它的已交付批次会暂停分发，待逐项核对。"}{" "}
                  当前关联成果 {data.artifacts.length} 份。
                </p>
                {(confirm.current !== data.current ||
                  confirm.projectRevision !== data.projectRevision) && (
                  <p role="alert">资料或引用已变化，请取消后重新选择。</p>
                )}
                <button
                  disabled={
                    busy ||
                    confirm.current !== data.current ||
                    confirm.projectRevision !== data.projectRevision
                  }
                  onClick={() => void apply()}
                >
                  {confirm.file ? "确认提交新版本" : "确认撤权"}
                </button>
                <button disabled={busy} onClick={() => setConfirm(null)}>
                  取消
                </button>
              </div>
            ))}
        </>
      )}
      {error && (
        <div>
          <p role="alert">{error}</p>
          <button onClick={() => retry((v) => v + 1)}>重新读取影响</button>
        </div>
      )}
    </details>
  );
}
const lifecycleLabels = {
  recycle: "移入回收区",
  restore: "恢复为新资料",
  purge: "彻底清除原资料",
} as const;
type SourceAction = keyof typeof lifecycleLabels;

function SourceRetention({
  source,
  data,
  path,
  reload,
}: {
  source: Source;
  data: History;
  path: string;
  reload: () => Promise<void>;
}) {
  const [choice, choose] = useState<{
    action: SourceAction;
    version: number;
    revision: number;
  } | null>(null);
  const [reason, setReason] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const expired =
    !!source.purgeDue && Date.parse(source.purgeDue) <= Date.now();
  const options: SourceAction[] = data.recycledAt
    ? expired
      ? ["purge"]
      : ["restore", "purge"]
    : ["recycle"];
  if (!choice)
    return (
      <div className="actions">
        {options.map((action) => (
          <button
            key={action}
            onClick={() => {
              choose({
                action,
                version: data.current,
                revision: data.projectRevision,
              });
              setReason("");
              setError("");
            }}
          >
            {lifecycleLabels[action]}
          </button>
        ))}
      </div>
    );
  const stale =
    choice.version !== data.current || choice.revision !== data.projectRevision;
  return (
    <form
      className="card"
      aria-label="资料生命周期确认"
      onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true);
        setError("");
        try {
          await api(path + "/lifecycle", "POST", {
            action: choice.action,
            version: choice.version,
            expectedProject: choice.revision,
            reason,
          });
          await reload();
          choose(null);
        } catch (e) {
          setError((e as Error).message);
          await reload().catch(() => {});
        } finally {
          setBusy(false);
        }
      }}
    >
      <strong>
        {lifecycleLabels[choice.action]} · {source.name}
      </strong>
      <small>
        {data.versions.length} 个资料版本 · {data.plans.length} 个方案版本 ·{" "}
        {data.runs.length} 次关联运行 · {data.artifacts.length} 项关联成果
      </small>
      {choice.action === "recycle" ? (
        <p>
          全部版本立即停止预览、检索和新调用。关联成果按版本重新核对分发。30
          天内可恢复，期满后清除工作库原文。
        </p>
      ) : choice.action === "restore" ? (
        <p>
          以新编号恢复全部版本。
          {data.originallyRevoked
            ? "这份资料原已撤权，恢复后仍限制访问。"
            : "当前项目成员可访问新资料。"}
          旧链接和任务引用继续受限；使用新资料时请重新选择。
        </p>
      ) : (
        <p>
          清除文件名、提取文本和索引，释放全部版本的原件。若其他资料或成果仍合法引用相同文件，将保留共用文件。此操作无法从回收区撤销。
        </p>
      )}
      {choice.action !== "restore" && (
        <small>
          备份按企业备份策略轮换，已被下载的外部副本需由接收方处理。已确认的交付保留记录。
        </small>
      )}
      <label>
        处理原因
        <input
          value={reason}
          maxLength={500}
          required
          onChange={(e) => setReason(e.target.value)}
        />
      </label>
      {error && <p role="alert">{error}</p>}
      {stale && <p role="alert">引用已变化，请取消后重新选择。</p>}
      <div className="actions">
        <button
          type="submit"
          className="primary"
          disabled={busy || stale || !reason.trim()}
        >
          确认{lifecycleLabels[choice.action]}
        </button>
        <button type="button" disabled={busy} onClick={() => choose(null)}>
          取消
        </button>
      </div>
    </form>
  );
}

type Span = { start: number; lines: number; text: string; truncated: boolean };
function SourceComparison({
  url,
  from,
  to,
}: {
  url: string;
  from: number;
  to: number;
}) {
  const [open, setOpen] = useState(false),
    [data, setData] = useState<{
      readable: boolean;
      before?: Span;
      after?: Span;
    } | null>(null),
    [error, setError] = useState("");
  useEffect(() => {
    if (!open) return;
    let active = true;
    const load = () => {
      setData(null);
      api<{ readable: boolean; before?: Span; after?: Span }>(
        url + "/compare?from=" + from + "&to=" + to,
      )
        .then((result) => {
          if (active) {
            setData(result);
            setError("");
          }
        })
        .catch((e) => {
          if (active) setError(e.message);
        });
    };
    load();
    return () => {
      active = false;
    };
  }, [url, from, to, open]);
  return (
    <details
      onToggle={(e) => {
        setOpen(e.currentTarget.open);
        if (!e.currentTarget.open) {
          setData(null);
          setError("");
        }
      }}
    >
      <summary>对照文本变化</summary>
      {data &&
        (!data.readable ? (
          <p>此版本组合需要取回原件，使用对应工具比较。</p>
        ) : data.before?.lines === 0 && data.after?.lines === 0 ? (
          <p>可提取的文本逐行一致。</p>
        ) : (
          [data.before, data.after].map(
            (span, index) =>
              span && (
                <div key={index}>
                  <strong>
                    v{index === 0 ? from : to} ·{" "}
                    {span.lines
                      ? "从第 " + span.start + " 行起，共 " + span.lines + " 行"
                      : "此处无内容"}
                  </strong>
                  {span.lines > 0 && <p className="preserve">{span.text}</p>}
                  {span.truncated && (
                    <small>仅展示部分变化范围，可取回原件查看全文。</small>
                  )}
                </div>
              ),
          )
        ))}
      {error && <p role="alert">{error}</p>}
    </details>
  );
}
export function hasSourceReview(a: Artifact) {
  return a.sourceChanges.some((c) => c.decision !== "retain");
}
export function SourceReview({
  p,
  a,
  busy,
  perform,
}: {
  p: Project;
  a: Artifact;
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
}) {
  if (!a.sourceChanges.length) return null;
  return (
    <details>
      <summary className={hasSourceReview(a) ? "action-needed" : undefined}>
        {p.stage === "delivered" ? "交付后的资料状态" : "资料依据核对"} ·{" "}
        {a.sourceChanges.length}
      </summary>
      {a.sourceChanges.map((c) => (
        <section key={[a.version, c.source, c.observed, c.revoked].join("-")}>
          <p>
            <strong>
              {c.originalName || "历史资料"} v{c.pinned}
            </strong>{" "}
            ·{" "}
            {c.revoked
              ? "原件访问已撤销"
              : "现有 " + c.name + " v" + c.observed}
          </p>
          {!c.revoked && (
            <p>
              <a
                href={
                  "/api/projects/" +
                  p.id +
                  "/sources/" +
                  c.source +
                  "/content?version=" +
                  c.pinned
                }
              >
                查看原依据
              </a>{" "}
              ·{" "}
              <a
                href={
                  "/api/projects/" +
                  p.id +
                  "/sources/" +
                  c.source +
                  "/content?version=" +
                  c.observed
                }
              >
                查看现有版本
              </a>
            </p>
          )}
          {!c.revoked && (c.originalName && isPictureName(c.originalName) && isPictureName(c.name) ? (
            <div className="source-picture-versions">
              <section aria-label="方案所用图片">
                <small>方案原图 · v{c.pinned}</small>
                <FilePreview name={c.originalName} url={`/api/projects/${p.id}/sources/${c.source}/content?version=${c.pinned}`} />
              </section>
              <section aria-label="当前版本图片">
                <small>当前版本 · v{c.observed}</small>
                <FilePreview name={c.name} url={`/api/projects/${p.id}/sources/${c.source}/content?version=${c.observed}`} />
              </section>
            </div>
          ) : (
            <SourceComparison
              url={"projects/" + p.id + "/sources/" + c.source}
              from={c.pinned}
              to={c.observed}
            />
          ))}
          {c.decision ? (
            <p>
              {c.decision === "retain" ? "已核对沿用" : "已进入修改流程"} ·{" "}
              {c.actor} · {c.reason ?? "核对依据受限"}
            </p>
          ) : p.stage === "delivered" ? (
            <p>请核查已分发成果的适用性。交付清单保留确认时的记录。</p>
          ) : p.canReview && p.stage === "review" ? (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const f = new FormData(e.currentTarget);
                void perform(() =>
                  api(
                    "projects/" +
                      p.id +
                      "/artifacts/" +
                      a.id +
                      "/source-assessments/" +
                      c.source,
                    "POST",
                    {
                      version: a.version,
                      pinned: c.pinned,
                      observed: c.observed,
                      revoked: c.revoked,
                      reason: f.get("reason"),
                      decision: f.get("decision"),
                    },
                  ),
                );
              }}
            >
              <label>
                处理方式
                <select name="decision" required defaultValue="">
                  <option value="" disabled>
                    请选择
                  </option>
                  <option value="retain">当前成果仍可沿用</option>
                  <option value="change">要求修订这份成果</option>
                </select>
              </label>
              <label>
                {c.revoked ? "独立核实依据或所需修改" : "核对依据或所需修改"}
                <textarea name="reason" maxLength={4000} required />
              </label>
              <button disabled={busy}>记录资料影响</button>
            </form>
          ) : (
            <p>等待项目审核人核对。</p>
          )}
        </section>
      ))}
    </details>
  );
}

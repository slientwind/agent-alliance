import { SourceSearch } from "./source-search";
import { SourceHistory } from "./source-impact";
import { useEffect, useState } from "react";
import { FilePreview, canPreview } from "./file-preview";
import { api } from "./client";
export type Source = {
  id: string;
  name: string;
  version: number;
  revoked: boolean;
  readable: boolean;
  picture?: boolean;
  canMaintain: boolean;
  extraction?: string;
  recycledAt?: string;
  purgeDue?: string;
  purgedAt?: string;
  recoveredAs?: string;
  cleanupPending?: boolean;
  cleanupFailed?: boolean;
  sharedFiles?: number;
  size: number;
};
export function extractionNotice(state?: string) {
  switch (state) {
    case "empty":
      return "未读到正文。扫描件可补充文字版。";
    case "invalid":
      return "文件损坏或含不支持的内容，可上传简化版本。";
    case "limited":
      return "内容超出读取范围，可拆分文件后上传。";
    case "unavailable":
      return "正文读取服务暂不可用，可稍后重试。";
    default:
      return "";
  }
}
export type Preference = {
  id: string;
  body: string;
  revision: number;
  enabled: boolean;
  expiresAt: string;
  reviewAt: string;
  purgeDue: string;
  lifecycle: number;
  state: "active" | "disabled" | "expired" | "deleted" | "departed";
};
export async function upload(
  project: string,
  file: File,
  source?: Source,
  expectedProject?: number,
  uploadKey?: string,
) {
  const params = new URLSearchParams({ name: file.name });
  if (expectedProject !== undefined)
    params.set("expectedProject", String(expectedProject));
  if (source) params.set("version", String(source.version));
  const r = await fetch(
    "/api/projects/" +
      project +
      "/sources" +
      (source ? "/" + source.id : "") +
      "?" +
      params,
    {
      method: source ? "PUT" : "POST",
      body: file,
      headers: uploadKey ? { "Idempotency-Key": uploadKey } : undefined,
    },
  );
  if (!r.ok) {
    const err = await r.json().catch(() => ({}));
    throw new Error(err.error || "上传失败");
  }
  return (await r.json()) as {
    id: string;
    version: number;
    readable: boolean;
    picture?: boolean;
    extraction?: string;
  };
}
export function Sources({
  reviewScope,
  project,
  items,
  pins,
  pin,
  reload,
}: {
  project: string;
  items: Source[];
  pins?: Record<string, number>;
  pin?: (id: string, version: number) => void;
  reviewScope: string;
  reload: () => Promise<void>;
}) {
  const [err, setErr] = useState("");
  const [retrying, setRetrying] = useState("");
  async function retry(source: Source) {
    setRetrying(source.id);
    try {
      await api(
        "projects/" + project + "/sources/" + source.id + "/extract",
        "POST",
        { version: source.version },
      );
      await reload();
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setRetrying("");
    }
  }
  async function attach(file: File | undefined, s?: Source) {
    if (!file) return;
    try {
      await upload(project, file, s);
      await reload();
      setErr("");
    } catch (e) {
      setErr((e as Error).message);
    }
  }
  const recycled = items.filter((s) => s.recycledAt);
  const row = (s: Source) => (
    <div key={s.id}>
      <strong>
        {s.name} · v{s.version}
      </strong>
      <small>
        {s.purgedAt
          ? s.cleanupFailed
            ? "原件清理暂时失败，等待重试"
            : s.cleanupPending
              ? "原件清理排队中"
              : "资料正文已清除"
          : s.recycledAt
            ? "回收区 · 可恢复至 " + new Date(s.purgeDue!).toLocaleString()
            : s.revoked
              ? "已撤权"
              : s.readable
                ? "可读取和引用"
                : s.picture
                  ? "可供视觉理解 Agent 引用"
                  : canPreview(s.name)
                    ? "可预览和下载"
                    : "仅支持取回原件"}
      </small>
      {!s.revoked && (
        <>
          {s.extraction === "ready" && (
            <small>
              {/\.xlsx$/i.test(s.name)
                ? "读取可见单元格已保存的值，公式和复杂数字格式请核对原表。"
                : /\.pdf$/i.test(s.name)
                  ? "按页读取文字层，图像和多栏排版请核对原件。"
                  : /\.pptx$/i.test(s.name)
                    ? "按可见幻灯片读取文字和表格，图形请核对原件。"
                    : "按正文段落和表格行读取，图片请核对原件。"}
            </small>
          )}
          {extractionNotice(s.extraction) && (
            <small>{extractionNotice(s.extraction)}</small>
          )}
          {!s.readable &&
            s.canMaintain &&
            /\.(docx|xlsx|pptx|pdf)$/i.test(s.name) &&
            (!s.extraction || s.extraction === "unavailable") && (
              <button
                type="button"
                disabled={!!retrying}
                onClick={() => void retry(s)}
              >
                {retrying === s.id ? "正在读取…" : "重新读取正文"}
              </button>
            )}
          {canPreview(s.name, s.readable) && (
            <FilePreview
              key={s.id + ":" + s.version}
              url={
                "/api/projects/" +
                project +
                "/sources/" +
                s.id +
                "/content?version=" +
                s.version
              }
              name={s.name}
            />
          )}
          <a
            href={
              "/api/projects/" +
              project +
              "/sources/" +
              s.id +
              "/content?version=" +
              s.version
            }
          >
            取回原件
          </a>
        </>
      )}
      <SourceHistory
        key={reviewScope}
        project={project}
        source={s}
        reload={reload}
        onUpdate={async (file, version, revision) => {
          await upload(project, file, { ...s, version }, revision);
        }}
      />
    </div>
  );
  return (
    <details className="card">
      <summary>项目资料 · {items.filter((s) => !s.revoked).length}</summary>
      <label>
        添加资料（每份最多 8 MiB）
        <input
          type="file"
          onChange={(e) => {
            void attach(e.target.files?.[0]);
            e.target.value = "";
          }}
        />
      </label>
      {items.some((s) => s.readable && !s.revoked) && (
        <SourceSearch
          key={
            project +
            ":" +
            items
              .filter((s) => s.readable && !s.revoked)
              .map((s) => s.id + "/" + s.version)
              .join(",")
          }
          project={project}
          pins={pins}
          pin={pin}
        />
      )}
      {items.filter((s) => !s.recycledAt).map(row)}
      {recycled.length > 0 && (
        <details>
          <summary>回收区与处理记录 · {recycled.length}</summary>
          {recycled.map(row)}
        </details>
      )}
      {err && <p role="alert">{err}</p>}
    </details>
  );
}
const formatDate = (value: string) => new Date(value).toLocaleString();
const actionText = {
  review: "确认仍适用",
  renew: "续期",
  delete: "移入回收区",
  restore: "恢复偏好",
  purge: "立即清除正文",
};
type PreferenceAction = keyof typeof actionText;
export function Preferences() {
  const [items, setItems] = useState<Preference[]>([]);
  const [bin, setBin] = useState<Preference[]>([]);
  const [edit, setEdit] = useState<Preference | null>(null);
  const [choice, setChoice] = useState<{
    p: Preference;
    action: PreferenceAction;
  } | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(true);
  async function load() {
    const [available, recycled] = await Promise.all([
      api<Preference[]>("preferences"),
      api<Preference[]>("preferences?view=recycle"),
    ]);
    setItems(available);
    setBin(recycled);
  }
  useEffect(() => {
    void load()
      .catch((e) => setErr(e.message))
      .finally(() => setBusy(false));
  }, []);
  async function mutate(fn: () => Promise<unknown>) {
    setBusy(true);
    setErr("");
    try {
      await fn();
      setChoice(null);
      setEdit(null);
      await load();
    } catch (e) {
      setErr((e as Error).message);
      await load().catch(() => {});
    } finally {
      setBusy(false);
    }
  }
  function select(p: Preference, action: PreferenceAction) {
    setChoice({ p, action });
    setEdit(null);
    setErr("");
  }
  function confirmation(p: Preference) {
    if (!choice || choice.p.id !== p.id) return null;
    const { action } = choice;
    const newTerm =
      action === "renew" ||
      (action === "restore" && Date.parse(p.expiresAt) <= Date.now());
    return (
      <form
        key={action}
        onSubmit={(e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          const until = f.get("until");
          void mutate(() =>
            api("preferences/" + p.id + "/actions", "POST", {
              action,
              expected: choice.p.revision,
              lifecycle: choice.p.lifecycle,
              password: f.get("password") || "",
              until: until ? new Date(String(until)).toISOString() : undefined,
            }),
          );
        }}
      >
        {action === "review" && (
          <small>
            下次复查在 90 天后，有效期仍至 {formatDate(p.expiresAt)}。
          </small>
        )}
        {newTerm && (
          <label>
            新截止时间（留空则从本次确认起 365 天）
            <input type="datetime-local" name="until" />
          </label>
        )}
        {action === "delete" && (
          <small>
            移入后立即停止后续引用，最长可恢复 30
            天。已有聊天、成果和已发往外部的内容仍需分别处理。
          </small>
        )}
        {action === "purge" && (
          <small>
            将永久清除此条偏好在工作库中的正文，操作记录仍保留。备份按单独的清理周期处理。
          </small>
        )}
        {action === "restore" && !newTerm && (
          <small>
            恢复后有效至 {formatDate(p.expiresAt)}
            。已固定的旧方案需要重新选择偏好版本。
          </small>
        )}
        {action === "restore" && p.state === "departed" && (
          <label>
            验证本人密码
            <input
              name="password"
              type="password"
              autoComplete="current-password"
              required
            />
          </label>
        )}
        <div className="actions">
          <button className="primary" disabled={busy}>
            {action === "review" ? "保存复查确认" : "确认" + actionText[action]}
          </button>
          <button type="button" onClick={() => setChoice(null)}>
            取消
          </button>
        </div>
      </form>
    );
  }
  return (
    <fieldset
      aria-label="个人偏好设置"
      aria-busy={busy}
      disabled={busy}
      className="plan-fields"
    >
      {items.map((p) => (
        <article className="card" key={p.id}>
          <p>{p.body}</p>
          <small>
            v{p.revision} · {p.enabled ? "启用" : "停用"} · 有效至{" "}
            {formatDate(p.expiresAt)}
          </small>
          <small>
            {Date.parse(p.reviewAt) <= Date.now() ? (
              <strong>已到复查日</strong>
            ) : (
              "下次复查"
            )}{" "}
            · {formatDate(p.reviewAt)}
          </small>
          {Date.parse(p.expiresAt) - Date.now() <= 30 * 86400000 && (
            <small>
              <strong>将在 30 天内到期</strong>
            </small>
          )}
          <div className="actions">
            <button
              onClick={() => {
                setEdit(p);
                setChoice(null);
              }}
            >
              纠正
            </button>
            <button
              disabled={busy}
              onClick={() =>
                void mutate(() =>
                  api("preferences/" + p.id, "PATCH", {
                    body: p.body,
                    enabled: !p.enabled,
                    expected: p.revision,
                  }),
                )
              }
            >
              {p.enabled ? "停用" : "启用"}
            </button>
            {(["review", "renew", "delete"] as const).map((a) => (
              <button key={a} onClick={() => select(p, a)}>
                {actionText[a]}
              </button>
            ))}
          </div>
          {confirmation(p)}
        </article>
      ))}
      <form
        key={edit?.id || "new"}
        onSubmit={(e) => {
          e.preventDefault();
          const form = e.currentTarget;
          const f = new FormData(form);
          void mutate(async () => {
            const until = f.get("until");
            await api(
              "preferences" + (edit ? "/" + edit.id : ""),
              edit ? "PATCH" : "POST",
              {
                body: f.get("body"),
                enabled: edit?.enabled ?? true,
                expected: edit?.revision || 0,
                until: until
                  ? new Date(String(until)).toISOString()
                  : undefined,
              },
            );
            form.reset();
          });
        }}
      >
        <label>
          {edit ? "纠正偏好" : "新增显式偏好"}
          <textarea name="body" required defaultValue={edit?.body} />
        </label>
        {!edit && (
          <label>
            临时截止时间（可选）
            <input type="datetime-local" name="until" />
          </label>
        )}
        <div className="actions">
          <button disabled={busy}>保存</button>
          {edit && (
            <button type="button" onClick={() => setEdit(null)}>
              取消纠正
            </button>
          )}
        </div>
      </form>
      <details>
        <summary>偏好回收区 · {bin.length}</summary>
        {bin.map((p) => (
          <article className="card" key={p.id}>
            <strong>
              {p.state === "departed"
                ? "离职隔离"
                : p.state === "expired"
                  ? "已到期"
                  : "已删除"}
            </strong>
            <p>{p.state === "departed" ? "条目标识 " + p.id : p.body}</p>
            <small>
              原有效期至 {formatDate(p.expiresAt)} · 可清理时间{" "}
              {formatDate(p.purgeDue)}
            </small>
            <div className="actions">
              {(p.state !== "departed" ||
                Date.parse(p.expiresAt) > Date.now()) && (
                <button onClick={() => select(p, "restore")}>
                  {Date.parse(p.expiresAt) <= Date.now()
                    ? "重新确认并续期"
                    : "恢复偏好"}
                </button>
              )}
              <button onClick={() => select(p, "purge")}>立即清除正文</button>
            </div>
            {confirmation(p)}
          </article>
        ))}
      </details>
      {err && <p role="alert">{err}</p>}
    </fieldset>
  );
}

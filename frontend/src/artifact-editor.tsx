import { api, RequestError, type Artifact, type Project } from "./client";

export type RevisionDraft = {
  base: number;
  body: string;
  reason: string;
  file: File | null;
};

type Props = {
  project: Project;
  artifact: Artifact;
  draft?: RevisionDraft;
  update: (next?: RevisionDraft, expected?: RevisionDraft) => void;
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
  refresh: () => Promise<void>;
};

export function ArtifactEditor({
  project: p,
  artifact: a,
  draft,
  update,
  busy,
  perform,
  refresh,
}: Props) {
  if (!p.canReview) return null;
  if (!draft)
    return p.stage === "review" ? (
      <button
        type="button"
        disabled={busy}
        onClick={() =>
          update({
            base: a.version,
            body: a.file ? "" : a.body,
            reason: "",
            file: null,
          })
        }
      >
        {a.file ? "上传外部修订稿" : "修改当前稿"}
      </button>
    ) : null;

  const saved = draft;
  const stale = saved.base !== a.version;
  const locked = p.stage !== "review";
  const invalidFile =
    saved.file &&
    a.file &&
    (saved.file.size === 0 || saved.file.size > 8 * 1024 * 1024
      ? "请选择有内容且不超过 8 MiB 的文件。"
      : !saved.file.name
            .toLowerCase()
            .endsWith("." + a.file.split(".").pop()?.toLowerCase())
        ? "修订文件需要与当前成果格式一致。"
        : "");
  const endpoint = "projects/" + p.id + "/artifacts/" + a.id;
  const currentFile = "/api/" + endpoint + "/file?version=" + a.version;
  const affected = new Set<number>();
  for (const [i, node] of (p.plan?.nodes ?? []).entries()) {
    if (node.dependsOn.some((d) => d === a.ordinal || affected.has(d)))
      affected.add(i + 1);
  }

  function change(patch: Partial<RevisionDraft>) {
    update({ ...saved, ...patch });
  }
  async function save() {
    if (locked || stale || busy || invalidFile) return;
    try {
      if (a.file) {
        if (!saved.file) return;
        const form = new FormData();
        form.set("expected_version", String(saved.base));
        form.set("reason", saved.reason);
        form.set("summary", saved.body);
        form.set("file", saved.file);
        const response = await fetch("/api/" + endpoint + "/file-revisions", {
          method: "POST",
          body: form,
        });
        if (!response.ok)
          throw new RequestError(
            response.status,
            (await response.json().catch(() => ({}))).error ||
              "修订文件上传失败",
          );
      } else {
        await api(endpoint + "/revise", "POST", {
          expected_version: saved.base,
          body: saved.body,
          reason: saved.reason,
        });
      }
      update(undefined, saved);
    } catch (error) {
      if (
        error instanceof RequestError &&
        [401, 403, 404, 409].includes(error.status)
      )
        await refresh();
      throw error;
    }
  }
  return (
    <form
      className="card revision-editor"
      aria-label={"修订 " + a.name + " · 步骤 " + a.ordinal}
      onSubmit={(e) => {
        e.preventDefault();
        void perform(save);
      }}
    >
      <h3>
        修订 {a.name} · 步骤 {a.ordinal}
      </h3>
      <small>草稿仅在本页暂存 · 基于 v{saved.base}</small>
      {locked ? (
        <p role="status">项目已结束审核，草稿仅供查阅。</p>
      ) : (
        stale && (
          <section className="card">
            <strong>成果已有 v{a.version}，请核对后继续修订</strong>
            <div className="artifact-version-comparison">
              <section aria-label={"当前 v" + a.version}>
                <h4>当前 v{a.version}</h4>
                <p className="preserve">{a.body}</p>
                {a.file && <a href={currentFile} download>下载当前版本</a>}
              </section>
              <section aria-label={"基于 v" + saved.base + " 的草稿"}>
                <h4>我的修订 · 基于 v{saved.base}</h4>
                <p className="preserve">{saved.body || "暂无正文"}</p>
                {a.file && <a href={"/api/" + endpoint + "/file?version=" + saved.base} download>下载基准版本</a>}
                {saved.file && <small>草稿文件 · {saved.file.name}</small>}
              </section>
            </div>
            <button
              type="button"
              disabled={busy}
              onClick={() => change({ base: a.version, file: null })}
            >
              {a.file
                ? "已核对新版，重新选择修订文件"
                : "已核对新版，继续编辑草稿"}
            </button>
          </section>
        )
      )}
      <fieldset className="plan-fields" disabled={busy || locked}>
        {a.file && (
          <>
            <label>
              外部修订文件（最多 8 MiB）
              <input
                type="file"
                accept={"." + a.file.split(".").pop()}
                onChange={(e) => {
                  const input = e.currentTarget;
                  const selected = input.files?.[0];
                  if (selected) change({ file: selected });
                  input.value = "";
                }}
              />
            </label>
            {saved.file && <small>已选文件 · {saved.file.name}</small>}
            {invalidFile && <p role="alert">{invalidFile}</p>}
          </>
        )}
        <label>
          {a.file ? "新版内容摘要" : "修订正文"}
          <textarea
            name={a.file ? "summary" : "body"}
            required
            maxLength={a.file ? 16000 : undefined}
            value={saved.body}
            onChange={(e) => change({ body: e.target.value })}
          />
        </label>
        <label>
          修改原因
          <input
            name="reason"
            required
            maxLength={a.file ? 2000 : undefined}
            value={saved.reason}
            onChange={(e) => change({ reason: e.target.value })}
          />
        </label>
      </fieldset>
      {!locked && (
        <p>
          {affected.size
            ? "本稿和以下关联成果需重新审核：" +
              p.artifacts
                .filter((x) => affected.has(x.ordinal))
                .map((x) => x.name + "（步骤 " + x.ordinal + "）")
                .join("、")
            : "保存后仅需重新审核本稿。"}
        </p>
      )}
      <div className="actions">
        {!locked && (
          <button
            disabled={
              busy ||
              stale ||
              !!invalidFile ||
              !saved.reason.trim() ||
              !saved.body.trim() ||
              (!!a.file && !saved.file)
            }
          >
            保存新版本
          </button>
        )}
        <button
          type="button"
          disabled={busy}
          onClick={() => update(undefined, saved)}
        >
          放弃这份修订草稿
        </button>
      </div>
    </form>
  );
}

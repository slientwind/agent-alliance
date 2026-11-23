import { SlideRevisionFields } from "./slide-outline";
import { CallRecovery } from "./call-recovery";
import { RemoteReceipt } from "./remote-receipt";
import { FilePreview } from "./file-preview";
import { ImageRegionField } from "./image-region";
import {
  api,
  statusLabel,
  type Artifact,
  type Project,
  type FeedbackItem,
  type Member,
  type ChangeRun,
} from "./client";

type Props = {
  project: Project;
  artifact: Artifact;
  feedback: FeedbackItem;
  user: Member;
  busy: boolean;
  perform: (fn: () => Promise<unknown>) => Promise<void>;
};

export function ChangeReview({
  project: p,
  artifact: a,
  feedback: f,
  user,
  busy,
  perform,
}: Props) {
  const base = "projects/" + p.id;
  const canEdit = p.canReview || user.id === p.owner;
  const choice = a.sourceChanges.find(
    (s) => s.feedbackId === f.id && s.selectableAssessment,
  );
  const active = f.changes.some((c) =>
    ["conflict", "pending"].includes(c.disposition),
  );
  return (
    <>
      {f.changes.map((c) => (
        <section key={c.run}>
          <strong>
            {a.name} · 基于 v{c.base} 的改稿
            {c.slidePage && " · 第 " + c.slidePage + " 页"}
            {c.imageRegion && ` · 选区 (${c.imageRegion.x}, ${c.imageRegion.y}) · ${c.imageRegion.width} × ${c.imageRegion.height} 像素`}
          </strong>
          {c.sourceInput && (
            <small>
              本次改稿授权使用 {c.sourceInput.name || "资料"} v
              {c.sourceInput.selected}（方案固定 v{c.sourceInput.planPin}）
            </small>
          )}
          <CallRecovery
            project={p.id}
            key={c.controlRevision}
            record={c.callReview}
            run={c.run}
            state={c.status}
            revision={c.controlRevision}
            canReview={
              p.owner === user.id || (p.canReview && user.id === c.actor)
            }
            busy={busy}
            perform={perform}
          />
          {c.remote && (
            <RemoteReceipt
              remote={c.remote}
              run={c.run}
              revision={c.controlRevision}
              canCheck={
                p.owner === user.id || (p.canReview && c.actor === user.id)
              }
              perform={perform}
            />
          )}
          {c.disposition === "applied" && (
            <small>已生成 v{c.applied} · 审核记录见成果版本</small>
          )}
          {c.disposition === "discarded" && <small>已保留现稿</small>}
          {c.disposition === "pending" && (
            <>
              <small>{statusLabel[c.status] || c.status}</small>
              {(p.owner === user.id ||
                (p.canReview && user.id === c.actor)) && (
                <details>
                  <summary>管理本次修改</summary>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      const d = new FormData(e.currentTarget);
                      void perform(() =>
                        api(
                          "runs/" + c.run + "/control",
                          "POST",
                          {
                            expected: c.controlRevision,
                            reason: d.get("reason"),
                            action: d.get("action"),
                          },
                          { "Idempotency-Key": crypto.randomUUID() },
                        ),
                      );
                    }}
                  >
                    <label>
                      操作
                      <select name="action">
                        {["paused", "blocked"].includes(c.status) && (
                          <option value="resume">重新核验并恢复</option>
                        )}
                        {["queued", "running"].includes(c.status) && (
                          <option value="pause">暂停</option>
                        )}
                        <option value="cancel">取消此次修改</option>
                        {c.canConcludeFailure && (
                          <option value="confirm_failure">
                            按已核对回执确认执行失败
                          </option>
                        )}
                      </select>
                    </label>
                    <label>
                      原因
                      <input required name="reason" />
                    </label>
                    <button disabled={busy}>确认操作</button>
                  </form>
                </details>
              )}
            </>
          )}
          {c.disposition === "conflict" && (
            <Conflict
              key={a.version + ":" + f.revision}
              change={c}
              project={p}
              artifact={a}
              feedback={f}
              user={user}
              busy={busy}
              perform={perform}
            />
          )}
        </section>
      ))}
      {canEdit &&
        p.stage === "review" &&
        f.state === "required" &&
        !active &&
        (a.editMode === "manual" ? (
          <small>
            此能力按固定规则处理资料，可通过成果中的修改入口回传修订。
          </small>
        ) : (
          <details>
            <summary>交给 {a.name} 修改</summary>
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const data = new FormData(e.currentTarget);
                void perform(() =>
                  api(
                    base +
                      "/artifacts/" +
                      a.id +
                      "/feedback/" +
                      f.id +
                      "/change-runs",
                    "POST",
                    {
                      version:
                        a.editMode === "outline"
                          ? Number(data.get("slideBase"))
                          : a.version,
                      slide:
                        a.editMode === "outline"
                          ? {
                              page: Number(data.get("slideNumber")),
                              title: data.get("slideTitle"),
                              body: data.get("slideBody"),
                            }
                          : undefined,
                      expected: f.revision,
                      imageRegion: data.has("imageLocal") ? {
                        x: Number(data.get("x")), y: Number(data.get("y")),
                        width: Number(data.get("width")), height: Number(data.get("height")),
                      } : undefined,
                      authorizeOneCall: data.get("authorize") === "on",
                      sourceAssessment: data.get("sourceAssessment") || "",
                      replacement: data.get("replacement") || "",
                      excerpt: data.get("excerpt") || "",
                    },
                    { "Idempotency-Key": crypto.randomUUID() },
                  ),
                );
              }}
            >
              {choice ? (
                <label key={choice.selectableAssessment}>
                  <input
                    type="checkbox"
                    name="sourceAssessment"
                    value={choice.selectableAssessment!}
                  />{" "}
                  仅本次改稿使用《{choice.name}》v{choice.observed}
                  。留空沿用方案资料 v{choice.pinned}。
                </label>
              ) : p.plan?.sources.length && a.editMode !== "image_edit" ? (
                <small>本次改稿沿用协作方案固定的资料版本。</small>
              ) : null}
              {a.editMode === "regenerate" && (
                <p>
                  本次修改会按文字反馈重新生成整张图。局部精修可在外部完成后上传
                  PNG。
                </p>
              )}
              {a.editMode === "image_edit" && (
                <>
                  <small>编辑以当前成果 v{a.version} 为基准。可修改整张图，也可只保留选区内的修改。</small>
                  <ImageRegionField key={a.version} version={a.version}
                    url={`/api/${base}/artifacts/${a.id}/file?version=${a.version}&preview=1`} />
                </>
              )}
              {p.plan?.nodes[a.ordinal-1]?.lookupPhase && <small>本次按反馈重新核对引文，生成整份修订稿。</small>}
              {a.editMode === "feedback" && !p.plan?.nodes[a.ordinal-1]?.lookupPhase && (
                <label>
                  仅修改此段原文（选填）
                  <textarea
                    name="excerpt"
                    placeholder="粘贴在当前稿中唯一出现的原文；留空时修订整稿"
                  />
                </label>
              )}
              {a.editMode === "outline" && (
                <SlideRevisionFields
                  body={a.body}
                  version={a.version}
                  position={f.base === a.version ? f.position || "" : ""}
                  quotePath={
                    f.quoteAvailable
                      ? base +
                        "/artifacts/" +
                        a.id +
                        "/feedback/" +
                        f.id +
                        "/source-quote?version=" +
                        a.version
                      : undefined
                  }
                />
              )}
              {a.editMode === "externalOutline" && (
                <p>
                  当前文件与原提纲的排版无法核对。可上传外部修订稿保留其版式，也可按完整提纲重新制作。
                </p>
              )}
              {(a.editMode === "replace" ||
                a.editMode === "externalOutline") && (
                <label>
                  用于重新生成文件的完整正文
                  <textarea
                    required
                    name="replacement"
                    defaultValue={a.body}
                    maxLength={64000}
                  />
                </label>
              )}
              <label>
                <input type="checkbox" name="authorize" required />
                授权负责此成果的 Agent
                调用一次，费用由团队承担，实际金额待核对。
              </label>
              <button disabled={busy}>
                {a.editMode === "externalOutline"
                  ? "按完整提纲重新制作"
                  : "启动局部修改"}
              </button>
            </form>
          </details>
        ))}
    </>
  );
}

function Conflict({
  change: c,
  project: p,
  artifact: a,
  feedback: f,
  user,
  busy,
  perform,
}: Props & { change: ChangeRun }) {
  const canAdopt =
    p.stage === "review" && ["required", "verification"].includes(f.state);
  return (
    <details open>
      <summary>版本或反馈已变化 · 比较两稿</summary>
      <h4>当前稿 v{a.version}</h4>
      <p className="preserve">{a.body}</p>
      <h4>Agent 候选稿</h4>
      <p className="preserve">{c.body}</p>
      {c.file && (
        <FilePreview
          url={"/api/projects/" + p.id + "/change-runs/" + c.run + "/file"}
          name={c.file}
        />
      )}
      {c.file && (
        <a
          href={"/api/projects/" + p.id + "/change-runs/" + c.run + "/file"}
          download
        >
          下载候选文件 {c.file}
        </a>
      )}
      {(p.canReview || p.owner === user.id) && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const d = new FormData(e.currentTarget);
            void perform(() =>
              api(
                "projects/" + p.id + "/change-runs/" + c.run + "/decision",
                "POST",
                {
                  version: a.version,
                  expected: f.revision,
                  action: d.get("action"),
                  reason: d.get("reason"),
                  mergedBody: d.get("merge") || "",
                },
              ),
            );
          }}
        >
          <label>
            处理方式
            <select name="action">
              <option value="discard">保留当前稿</option>
              {canAdopt && (
                <option value="apply">采用 Agent 稿并生成新版本</option>
              )}
              {canAdopt && !c.file && (
                <option value="merge">合并两稿并生成新版本</option>
              )}
            </select>
          </label>
          {canAdopt && !c.file && (
            <label>
              合并后的完整正文（选择合并时填写）
              <textarea name="merge" />
            </label>
          )}
          <label>
            处理依据
            <input required name="reason" />
          </label>
          <button disabled={busy}>确认候选稿处理</button>
        </form>
      )}
    </details>
  );
}

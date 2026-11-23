import { FilePreview } from "./file-preview";
import { PictureTrialField } from "./picture-trial-field";
import { useEffect, useState } from "react";
import { api } from "./client";
import { authorizeTrial } from "./trial-request";

type Definition = {
  name: string;
  sample: string;
  expected: string;
  match: string;
  reviewer: string;
};
type Check = Omit<Definition, "reviewer"> & {
  reviewer: string | null;
  id: string;
  passed: boolean | null;
  latestTrial: string | null;
  effectiveTrial: string | null;
  status: string | null;
  verdict: boolean | null;
  reason: string | null;
  canReview: boolean;
  withdrawn: string | null;
  file: string | null;
  picture?: string | null;
  output: string | null;
};
type Suite = { version: number; editable: boolean; items: Check[] };
const blank = (): Definition => ({
  name: "",
  sample: "",
  expected: "",
  match: "contains",
  reviewer: "",
});

export function AgentChecks({
  agent,
  revision,
  refresh,
  pinned = false,
  project,
  version,
  imageSetup,
  pictureInput = false,
}: {
  imageSetup?: { reviewer: string; payer: string; estimate?: string | null };
  pictureInput?: boolean;
  pinned?: boolean;
  project?: string;
  version?: number;
  agent: string;
  revision: number;
  refresh: () => Promise<void>;
}) {
  const [open, setOpen] = useState(!!imageSetup),
    [suite, setSuite] = useState<Suite | null>(null);
  const [draft, setDraft] = useState<Definition[] | null>(null),
    [busy, setBusy] = useState(false),
    [message, setMessage] = useState("");
  const [epoch, reload] = useState(0);
  const path = "agents/" + agent + "/checks/" + revision;
  useEffect(() => {
    if (!open) return;
    let live = true;
    api<Suite>(path)
      .then((value) => {
        if (live) setSuite(value);
      })
      .catch((e) => {
        if (live) setMessage(e.message);
      });
    return () => {
      live = false;
    };
  }, [path, open, epoch, version]);
  async function apply(fn: () => Promise<string>) {
    if (busy) return;
    setBusy(true);
    try {
      const text = await fn();
      setMessage(text);
      if (version === undefined) reload((x) => x + 1);
      await refresh();
    } catch (e) {
      setMessage((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  function edit(index: number, field: keyof Definition, value: string) {
    setDraft(
      (rows) =>
        rows?.map((r, i) => (i === index ? { ...r, [field]: value } : r)) ??
        null,
    );
  }
  return (
    <details open={open} onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>
        {imageSetup ? "样图与验收" : "必要检查"} · v{revision}
      </summary>
      {suite && (
        <>
          {suite.items.length === 0 && (
            <small>
              {imageSetup
                ? "用一个实际任务生成样图，由指定成员看图验收后即可提交发布。"
                : "可配置多项必要检查。未配置时，发布需要一次通过的试运行。"}
            </small>
          )}
          {imageSetup && suite.editable && suite.items.length === 0 && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const fields = new FormData(e.currentTarget);
                void apply(async () => {
                  await api(path, "PUT", {
                    version: suite.version,
                    items: [
                      {
                        name: String(fields.get("criterion")).trim(),
                        sample: String(fields.get("sample")).trim(),
                        reviewer: String(fields.get("reviewer")).trim(),
                        expected: "PNG 图像",
                        match: "contains",
                      },
                    ],
                  });
                  return "样例已保存，可授权生成样图。";
                });
              }}
            >
              <label>
                样图要求
                <textarea name="sample" required />
              </label>
              <label>
                看图时要确认什么？
                <input
                  name="criterion"
                  required
                  placeholder="例如，标题清晰，画面只出现一种产品"
                />
              </label>
              <label>
                看图验收人
                <input
                  type="email"
                  name="reviewer"
                  defaultValue={imageSetup.reviewer}
                  required
                />
              </label>
              <small>样例和验收要求对团队成员可见。</small>
              <button disabled={busy}>保存样图要求</button>
            </form>
          )}
          {pinned && suite.items.length === 0 && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                const f = new FormData(e.currentTarget);
                void apply(() =>
                  authorizeTrial(
                    agent,
                    revision,
                    String(f.get("sample")),
                    String(f.get("expected")),
                    undefined,
                    project,
                    f.get("picture") as File | null,
                  ),
                );
              }}
            >
              <label>
                试运行输入
                <textarea name="sample" required />
              </label>
              {pictureInput && <PictureTrialField />}
              <label>
                预期文本
                <input name="expected" required />
              </label>
              <label>
                <input type="checkbox" required />{" "}
                授权验证此修订一次，费用由团队承担
                {project && "，计入本项目预算"}。
              </label>
              <button disabled={busy}>复验绑定修订</button>
            </form>
          )}
          {suite.items.map((c) => (
            <section key={c.id}>
              <strong>
                {c.name}
                {!imageSetup &&
                  " · " +
                    (c.passed === true
                      ? "已通过"
                      : c.passed === false
                        ? "未通过"
                        : "待验证")}
              </strong>
              <p>共享样例 · {c.sample}</p>
              {(!imageSetup ||
                c.match !== "contains" ||
                c.expected !== "PNG 图像") && (
                <p>
                  {c.match === "exact" ? "应完全等于" : "应包含"} · {c.expected}
                </p>
              )}
              {c.reviewer && <small>业务验收人 · {c.reviewer}</small>}
              {c.latestTrial && (
                <small>
                  最近试运行 ·{" "}
                  {(
                    {
                      passed: "技术检查通过",
                      failed: "技术检查未通过",
                      submitted: "已受理",
                      remote: "等待远端回执",
                      unknown: "结果待核对",
                    } as Record<string, string>
                  )[c.status || ""] || c.status}
                  {c.status === "passed" &&
                    c.reviewer &&
                    (c.verdict === null
                      ? " · 待业务验收"
                      : c.verdict
                        ? " · 业务验收通过"
                        : " · 业务验收未通过")}
                </small>
              )}
              {c.file && (
                <>
                  <FilePreview
                    name={c.file}
                    url={
                      "/api/agents/" +
                      agent +
                      "/evidence/" +
                      c.latestTrial +
                      "/file"
                    }
                  />
                  <a
                    href={
                      "/api/agents/" +
                      agent +
                      "/evidence/" +
                      c.latestTrial +
                      "/file"
                    }
                  >
                    下载试运行原件
                  </a>
                </>
              )}
              {c.picture && <FilePreview name={c.picture} url={"/api/agents/" + agent + "/evidence/" + c.latestTrial + "/picture"} />}
              {c.output && <p className="preserve">{c.output}</p>}
              {c.withdrawn && <p>证据已失效 · {c.withdrawn}</p>}
              {c.reason && <p>验收依据 · {c.reason}</p>}
              {c.canReview && (
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    const f = new FormData(e.currentTarget);
                    void apply(async () => {
                      await api(
                        "agents/" +
                          agent +
                          "/evidence/" +
                          c.latestTrial +
                          "/review",
                        "POST",
                        {
                          passed: f.get("verdict") === "pass",
                          reason: f.get("reason"),
                        },
                      );
                      return "已记录该次试运行的业务验收结论";
                    });
                  }}
                >
                  <label>
                    业务结论
                    <select name="verdict" required defaultValue="">
                      <option value="" disabled>
                        选择结论
                      </option>
                      <option value="pass">通过</option>
                      <option value="fail">未通过</option>
                    </select>
                  </label>
                  <label>
                    验收依据
                    <textarea name="reason" required />
                  </label>
                  <button disabled={busy}>提交业务验收</button>
                </form>
              )}
              <form
                onSubmit={(e) => {
                  e.preventDefault();
                  const form = e.currentTarget;
                  const file = new FormData(form).get("picture") as File | null;
                  void apply(async () => {
                    const msg = await authorizeTrial(
                      agent,
                      revision,
                      c.sample,
                      c.expected,
                      c.id,
                      project,
                      file,
                    );
                    form.reset();
                    return msg;
                  });
                }}
              >
                {pictureInput && <PictureTrialField />}
                <label>
                  <input type="checkbox" required />{" "}
                  授权以共享样例试运行一次，费用由{imageSetup?.payer || "团队"}
                  承担
                  {project && "，计入本项目预算"}，指定验收人可查看结果。
                  {imageSetup &&
                    (imageSetup.estimate == null
                      ? "金额未知，无法预先封顶。"
                      : "预计 ¥" + imageSetup.estimate + "。")}
                </label>
                <button disabled={busy}>
                  {imageSetup ? "生成样图" : "运行此检查"}
                </button>
              </form>
            </section>
          ))}
          {!draft && (
            <div className="actions">
              <button
                type="button"
                disabled={busy}
                onClick={() => {
                  reload((x) => x + 1);
                  if (imageSetup)
                    void refresh().catch((error) => setMessage(error.message));
                }}
              >
                刷新检查结果
              </button>
              {suite.editable && (!imageSetup || suite.items.length > 0) && (
                <button
                  type="button"
                  onClick={() =>
                    setDraft(
                      suite.items.length
                        ? suite.items.map((c) => ({
                            name: c.name,
                            sample: c.sample,
                            expected: c.expected,
                            match: c.match,
                            reviewer: c.reviewer ?? "",
                          }))
                        : [blank()],
                    )
                  }
                >
                  编辑验收清单
                </button>
              )}
            </div>
          )}
          {draft && (
            <form
              onSubmit={(e) => {
                e.preventDefault();
                void apply(async () => {
                  await api(path, "PUT", {
                    version: suite.version,
                    items: draft,
                  });
                  setDraft(null);
                  return "已保存此修订的验收清单";
                });
              }}
            >
              <small>
                样例与预期对团队可见。首次试运行后清单冻结，新修订沿用清单并重新验证。
              </small>
              {draft.map((item, i) => (
                <fieldset key={i}>
                  <legend>检查 {i + 1}</legend>
                  <label>
                    验收要求
                    <input
                      required
                      value={item.name}
                      onChange={(e) => edit(i, "name", e.target.value)}
                    />
                  </label>
                  <label>
                    团队共享测试输入
                    <textarea
                      required
                      value={item.sample}
                      onChange={(e) => edit(i, "sample", e.target.value)}
                    />
                  </label>
                  <label>
                    预期文本
                    <textarea
                      required
                      value={item.expected}
                      onChange={(e) => edit(i, "expected", e.target.value)}
                    />
                  </label>
                  <label>
                    文本判断方式
                    <select
                      value={item.match}
                      onChange={(e) => edit(i, "match", e.target.value)}
                    >
                      <option value="contains">包含预期文本</option>
                      <option value="exact">完全相同</option>
                    </select>
                  </label>
                  <label>
                    业务验收人邮箱（可选）
                    <input
                      type="email"
                      value={item.reviewer}
                      onChange={(e) => edit(i, "reviewer", e.target.value)}
                    />
                  </label>
                  <button
                    type="button"
                    onClick={() => setDraft(draft.filter((_, n) => n !== i))}
                  >
                    移除此检查
                  </button>
                </fieldset>
              ))}
              <div className="actions">
                <button
                  type="button"
                  onClick={() => setDraft([...draft, blank()])}
                >
                  添加检查
                </button>
                <button disabled={busy}>保存验收清单</button>
                <button type="button" onClick={() => setDraft(null)}>
                  放弃编辑
                </button>
              </div>
            </form>
          )}
        </>
      )}
      {message && <p role="status">{message}</p>}
    </details>
  );
}

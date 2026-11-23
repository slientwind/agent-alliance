import { checkReleaseBoundaries, releaseInBrowser } from "./agent-release.mjs";
import { checkImageRevision } from "./draft-image-revision.mjs";
import { checkProjectDraftRevision } from "./project-draft-revision.mjs";
import {
  checkConcurrentEdit,
  checkDraftBoundaries,
} from "./draft-revision-boundaries.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  QUICK_ENDPOINT: endpoint,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: out,
} = process.env;
const real = process.env.DRAFT_REAL_MODEL === "1";
const mode = real ? "local" : "fixture";
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const user = await browser.newPage({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  async function post(page, path, data, expected = 201) {
    const res = await page.request.post("/api/" + path, { data });
    assert.equal(res.status(), expected, await res.text());
    return res.status() === 204 ? null : res.json();
  }
  async function get(page, path) {
    const res = await page.request.get("/api/" + path);
    assert.equal(res.status(), 200, await res.text());
    return res.json();
  }
  await post(admin, "login", { email: "review@sample.invalid", password }, 200);
  const invitation = await post(admin, "admin/invitations", {});
  const email = "draft-" + mode + "@sample.invalid";
  const registered = await user.request.post("/api/register", {
    data: { name: "业务经办人", email, password, token: invitation.token },
  });
  assert.ok(registered.ok(), await registered.text());
  const team = (await get(user, "teams")).find((t) => t.joined && t.default).id;
  const model = await post(admin, "resources", {
    team,
    name: "工单整理模型 " + mode,
    endpoint,
    model: real ? "local-course-qwen" : "revision-fixture",
    key: "synthetic",
  });
  const sample = "维修单：饮水机漏水，工单编号 SRV-184，已通知物业。";
  const expected = "SRV-184";
  const name = "报修单整理 " + mode;
  const instructions =
    "你是工单提取助手。对用户收到的维修记录，只输出工单编号（以 SRV- 开头）。不要输出其他字符。";
  const launch = user.getByRole("button", { name: "选择或创建 Agent" });
  const panel = user.getByRole("dialog", { name: "Agent", exact: true });
  await user.goto("/");
  await launch.click();
  await panel.getByRole("button", { name: "创建 Agent", exact: true }).click();
  await panel
    .getByLabel("希望 Agent 完成什么任务？")
    .fill("所有工单只回复“待分类”三个字。");
  const options = panel.getByText("名称与模型", { exact: true });
  if (!(await options.evaluate((s) => s.parentElement.open)))
    await options.click();
  await panel.getByLabel("文本模型").selectOption(model.id + "/1");
  await panel.getByLabel("名称（可选）").fill(name);
  const createResponse = user.waitForResponse(
    (r) => r.url().endsWith("/api/agents") && r.request().method() === "POST",
  );
  await panel.getByRole("button", { name: "创建并准备试运行" }).click();
  const id = (await (await createResponse).json()).id;
  const route = "agents/" + id;
  const input = panel.getByLabel("试运行输入");
  const criterion = panel.getByLabel("结果应包含", { exact: true });
  const consent = panel.getByRole("checkbox", { name: /授权本次试运行/ });
  async function runTrial() {
    await consent.check();
    const reply = user.waitForResponse((r) =>
      r.url().endsWith("/" + route + "/validate"),
    );
    await panel.getByRole("button", { name: "运行示例", exact: true }).click();
    const response = await reply;
    assert.equal(response.status(), 200, await response.text());
    return response.json();
  }
  await input.fill(sample);
  await criterion.fill(expected);
  const first = await runTrial();
  assert.equal(first.status, "failed");
  assert.ok(!first.output.includes(expected));
  const samplePath = route + "/evidence/" + first.id + "/sample";
  assert.deepEqual(await get(user, samplePath), { input: sample, expected });
  assert.equal((await admin.request.get("/api/" + samplePath)).status(), 404);
  await user.reload();
  await launch.click();
  await panel.getByText(/我的文本草稿/).click();
  await panel.getByRole("button", { name: new RegExp(name) }).click();
  await panel.getByText("调整 Agent 职责", { exact: true }).click();
  await panel.getByLabel("调整后的职责").fill(instructions);
  const saved = user.waitForResponse((r) =>
    r.url().endsWith("/" + route + "/instructions"),
  );
  await panel.getByRole("button", { name: "保存职责为新草稿" }).click();
  const updated = await saved;
  assert.equal(updated.status(), 201, await updated.text());
  assert.equal((await updated.json()).revision, 2);
  const denied = await user.request.post("/api/" + route + "/instructions", {
    data: { expected: 1, instructions: "过期并发保存" },
  });
  assert.equal(denied.status(), 409);
  const state = (await get(user, "agents")).find((a) => a.id === id);
  assert.equal(state.resource, model.id);
  assert.equal(state.resourceRevision, 1);
  assert.equal(state.validated, false);
  assert.equal(state.published, 0);
  assert.equal(
    (
      await admin.request.post("/api/" + route + "/publish", {
        data: { revision: 2 },
      })
    ).status(),
    409,
  );
  await panel.getByText("试运行记录与回执", { exact: true }).click();
  await consent.check();
  await panel.getByRole("button", { name: "沿用这条样例" }).click();
  await user.waitForFunction(
    (s) => document.querySelector('textarea[name="input"]')?.value === s,
    sample,
  );
  assert.equal(await criterion.inputValue(), expected);
  assert.equal(await consent.isChecked(), false);
  assert.equal((await get(user, route + "/evidence")).length, 1);
  await panel.screenshot({ path: out + "/draft-revision-" + mode + ".png" });
  const second = await runTrial();
  assert.equal(second.status, "passed", JSON.stringify(second));
  assert.ok(second.output.includes(expected));
  const publication = await releaseInBrowser({
    admin, user, view: panel, id, name, revision: 2, get, returnFirst: !real,
    screenshot: out + "/agent-release-" + mode + ".png",
  });
  await panel.getByRole("button", { name: "查看发布状态" }).click();
  await panel.getByRole("button", { name: "在此对话使用已发布版本" }).click();
  await user
    .getByRole("textbox", { name: "消息", exact: true })
    .fill("请处理刚到的报修单：编号 SRV-627，空调停机。");
  await user.getByRole("button", { name: "发送消息" }).click();
  await user.getByLabel(/授权本次 1/).check();
  await user.getByRole("button", { name: "确认并执行" }).click();
  const review = user.getByRole("button", { name: "审核此版本" });
  await review.waitFor();
  const pending = await get(
    user,
    "projects/" + new URL(user.url()).hash.slice(1),
  );
  assert.equal(pending.artifacts[0].body.trim(), "SRV-627");
  await review.click();
  await user.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = user.getByRole("link", { name: "下载交付包（ZIP）" });
  await download.waitFor();
  const archive = await user.request.get(await download.getAttribute("href"));
  assert.ok(archive.ok(), await archive.statusText());
  await writeFile(
    out + "/draft-revision-" + mode + ".zip",
    await archive.body(),
  );
  const project = await get(
    user,
    "projects/" + new URL(user.url()).hash.slice(1),
  );
  assert.equal(project.plan.nodes[0].revision, 2);
  assert.equal(project.stage, "delivered");
  assert.equal(project.costSummary.calls, 1);
  assert.ok(
    project.artifacts[0].body.includes("SRV-627"),
    project.artifacts[0].body,
  );
  const trials = await get(user, route + "/evidence");
  assert.deepEqual(
    trials.map((t) => [t.revision, t.status]),
    [
      [2, "passed"],
      [1, "failed"],
    ],
  );
  const projectGap = await checkProjectDraftRevision({
    admin,
    user,
    model,
    post,
    get,
    out,
    mode,
  });
  if (!real) await checkConcurrentEdit({ admin, user, model, post, get });
  const releaseBoundaries = real
    ? undefined
    : await checkReleaseBoundaries({ admin, user, endpoint, post, get });
  const imageRevision = real
    ? undefined
    : await checkImageRevision({ admin, user, endpoint, team, post, get });
  const boundaries = real
    ? undefined
    : await checkDraftBoundaries({
        admin,
        user,
        endpoint,
        team,
        post,
        get,
        samplePath,
      });
  await writeFile(
    out + "/draft-revision-" + mode + ".json",
    JSON.stringify(
      {
        projectGap,
        imageRevision,
        boundaries,
        releaseBoundaries,
        data: "synthetic",
        provider: real ? "local Qwen" : "HTTPS fixture",
        sample,
        expected,
        trials: trials.map((t) => ({
          revision: t.revision,
          status: t.status,
          output: t.output,
        })),
        staleSave: denied.status(),
        publishedRevision: 2,
        publication,
        delivery: project.artifacts[0].body,
        projectCalls: project.costSummary.calls,
        stage: project.stage,
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

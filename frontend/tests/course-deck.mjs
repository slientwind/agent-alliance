import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: dir,
} = process.env;
const browser = await chromium.launch();
const admin = await browser.newPage({ baseURL });
const page = await browser.newPage({
  baseURL,
  viewport: { width: 390, height: 844 },
});
const rows = [
  ["领取设备", "员工填写设备用途和预计归还日。\n领取前由部门负责人确认。"],
  ["归还设备", "保管员核对设备编号并登记。"],
  ["遗失报告", "遗失时立即报告保管员。"],
];
const replacement = "保管员核对设备编号并登记归还日期。";
const goal = "按培训提纲制作可编辑设备借用课件";
const panel = page.getByRole("dialog", { name: "Agent", exact: true });
const launch = page.getByRole("button", { name: "选择或创建 Agent" });
async function call(context, path, method, data, status, key) {
  const res = await context.request.fetch("/api/" + path, {
    method,
    data,
    headers: key ? { "Idempotency-Key": key } : {},
  });
  assert.equal(
    res.status(),
    status,
    path + " " + (res.ok() ? "" : await res.text()),
  );
  if (res.headers()["content-type"]?.includes("json")) return res.json();
}
async function capture(url, file) {
  const rsp = await page.request.get(url);
  assert.equal(rsp.status(), 200, url);
  await writeFile(dir + "/" + file, await rsp.body());
}
try {
  await call(
    admin,
    "login",
    "POST",
    { email: "review@sample.invalid", password },
    200,
  );
  const invite = await call(admin, "admin/invitations", "POST", {}, 201);
  await call(
    page,
    "register",
    "POST",
    {
      email: "course-author@sample.invalid",
      name: "培训制作人",
      token: invite.token,
      password,
    },
    201,
  );
  const { id: team } = await call(
    admin,
    "admin/teams",
    "POST",
    { name: "培训课件工作组" },
    201,
  );
  const who = await call(page, "me", "GET", undefined, 200);
  const joined = await call(page, "teams", "GET", undefined, 200);
  await call(
    admin,
    "admin/teams/" + team + "/members/" + who.id,
    "PUT",
    { joined: true, reason: "参与课件验证" },
    204,
  );
  await call(
    admin,
    "admin/teams/" + joined.find((t) => t.default).id + "/members/" + who.id,
    "PUT",
    { joined: false, reason: "验证单一业务组快捷入口" },
    204,
  );
  await page.goto("/");
  const message = page.getByRole("textbox", { name: "消息", exact: true });
  await message.fill(goal);
  await launch.click();
  await panel.getByRole("button", { name: "创建 Agent" }).click();
  assert.equal(await panel.getByLabel("成果类型").inputValue(), "pptx");
  await panel.getByRole("button", { name: "从资料编稿助手开始" }).click();
  assert.equal(await panel.getByLabel("成果类型").inputValue(), "text");
  assert.match(
    await panel.getByLabel("希望 Agent 完成什么任务？").inputValue(),
    /正文只摘录资料里的短句/,
  );
  await panel.getByRole("button", { name: "返回 Agent 列表" }).click();
  await panel.getByRole("button", { name: "创建 Agent" }).click();
  await panel.getByLabel("希望 Agent 完成什么任务？").fill(goal);
  if (!(await panel.getByLabel("名称（可选）").isVisible()))
    await panel.getByText("名称与归属", { exact: true }).click();
  await panel.getByLabel("名称（可选）").fill("");
  assert.equal(
    await panel.getByLabel("希望 Agent 完成什么任务？").inputValue(),
    goal,
  );
  assert.equal(await panel.getByLabel("文本模型").count(), 0);
  const made = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/agents/tools") && r.request().method() === "POST",
  );
  await panel.getByRole("button", { name: "创建并准备试运行" }).click();
  const agent = await (await made).json();
  assert.ok(agent.id, JSON.stringify(agent));
  const path = "agents/" + agent.id;
  await call(admin, path + "/publish", "POST", { revision: 1 }, 409);
  const bad = await call(
    page,
    path + "/validate",
    "POST",
    {
      revision: 1,
      input: "没有任何页标题的段落。",
      expected: "段落",
      authorizeOneCall: true,
    },
    200,
    "course-negative",
  );
  assert.equal(bad.status, "failed");
  assert.match(bad.output, /页标题/);
  await panel.getByLabel("第 1 页正文").fill("员".repeat(38));
  await panel.getByLabel("结果应包含").fill("部门负责人");
  await panel.getByRole("checkbox").check();
  await panel.getByRole("button", { name: "运行示例" }).click();
  assert.equal(
    await panel.getByLabel("第 1 页正文").evaluate((el) => el.validity.valid),
    false,
  );
  await panel.getByLabel("第 1 页正文").fill(rows[0][1]);
  await panel.getByRole("button", { name: "运行示例" }).click();
  await panel.getByRole("button", { name: "申请发布此版本", exact: true }).waitFor();
  await panel.getByText("试运行记录与回执", { exact: true }).click();
  await panel.getByText("预览 deliverable.pptx", { exact: true }).click();
  await panel.getByText("第 1 / 2 页", { exact: true }).waitFor();
  assert.equal(
    (await call(page, path + "/evidence", "GET", undefined, 200)).length,
    2,
  );
  await call(page, path + "/publish", "POST", { revision: 1 }, 403);
  await call(admin, path + "/publish", "POST", { revision: 1 }, 204);
  await panel.getByRole("button", { name: "关闭", exact: true }).click();
  await launch.click();
  await panel.getByLabel("搜索 Agent").fill(goal);
  await panel.getByRole("button", { name: new RegExp(goal) }).click();
  await page.getByRole("button", { name: "编写分页提纲" }).click();
  const editor = page.getByRole("dialog", { name: "分页提纲" });
  for (const [i, [title, body]] of rows.entries()) {
    if (i) await editor.getByRole("button", { name: "添加一页" }).click();
    await editor.getByLabel("第 " + (i + 1) + " 页标题").fill(title);
    await editor.getByLabel("第 " + (i + 1) + " 页正文").fill(body);
  }
  await editor.getByRole("button", { name: "附到当前对话" }).click();
  await page.getByRole("button", { name: "编辑分页提纲" }).click();
  assert.equal(await editor.getByLabel("第 3 页正文").inputValue(), rows[2][1]);
  await editor.getByRole("button", { name: "附到当前对话" }).click();
  await page.getByRole("button", { name: "发送消息" }).click();
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const id = new URL(page.url()).hash.slice(1),
    project = "projects/" + id;
  const read = () => call(page, project, "GET", undefined, 200);
  const planned = await read();
  assert.equal(planned.plan.nodes[0].operation, "pptx");
  assert.equal(planned.plan.sources.length, 1);
  assert.equal(planned.plan.sources[0].version, 1);
  await page.getByLabel(/授权本次 1/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  await page.getByRole("button", { name: "审核此版本" }).waitFor();
  const art = (await read()).artifacts[0];
  assert.equal(art.editMode, "outline");
  assert.equal(art.version, 1);
  const file = project + "/artifacts/" + art.id;
  await capture("/api/" + file + "/file?version=1", "course-v1.pptx");
  await capture("/api/" + file + "/file?version=1&preview=1", "course-v1.pdf");
  const feedback = page.locator("details.feedback");
  await feedback.locator(":scope > summary").click();
  await feedback.getByLabel("反馈类型").selectOption("change");
  await feedback.getByLabel("位置（选填）").fill("第 2 页");
  await feedback.getByLabel("反馈内容").fill("在归还登记中明确日期。");
  await feedback.getByRole("button", { name: "提交反馈" }).click();
  await feedback.getByText("交给 " + goal + " 修改").click();
  await feedback.getByLabel("要修改哪一页？").selectOption("2");
  assert.equal(
    await feedback.getByLabel("第 2 页正文").inputValue(),
    rows[1][1],
  );
  await feedback.getByLabel("第 2 页正文").fill(replacement);
  const note = (await read()).artifacts[0].feedback[0],
    change = file + "/feedback/" + note.id + "/change-runs";
  const body = {
    version: 1,
    expected: note.revision,
    authorizeOneCall: true,
    slide: { page: 2, title: rows[1][0], body: replacement },
  };
  await call(
    page,
    change,
    "POST",
    { ...body, slide: { ...body.slide, page: 4 } },
    422,
    "course-invalid-number",
  );
  await call(
    page,
    change,
    "POST",
    { ...body, slide: { ...body.slide, body: "增加\n## 额外页\n正文" } },
    422,
    "course-invalid-heading",
  );
  for (const [invalid, key] of [
    [replacement + "\u2028", "control"],
    ["新".repeat(37), "width"],
  ]) {
    await call(
      page,
      change,
      "POST",
      { ...body, slide: { ...body.slide, body: invalid } },
      422,
      "course-invalid-" + key,
    );
  }
  await feedback.getByLabel(/授权负责此成果/).check();
  const sent = page.waitForRequest(
    (r) => r.url().endsWith("/change-runs") && r.method() === "POST",
  );
  await feedback.getByRole("button", { name: "启动局部修改" }).click();
  const request = await sent,
    key = request.headers()["idempotency-key"];
  await feedback.getByText("待审核人复核", { exact: true }).waitFor();
  const current = (await read()).artifacts[0];
  assert.equal(current.version, 2);
  assert.equal(current.editMode, "outline");
  assert.equal(current.approved, false);
  assert.equal(current.feedback[0].changes[0].slidePage, 2);
  const replay = await call(
    page,
    change,
    "POST",
    request.postDataJSON(),
    202,
    key,
  );
  assert.equal(replay.run, current.feedback[0].changes[0].run);
  await call(
    page,
    change,
    "POST",
    { ...body, slide: { ...body.slide, page: 1 } },
    409,
    key,
  );
  await call(page, change, "POST", body, 409, "course-stale-version");
  await capture("/api/" + file + "/file?version=2", "course-v2.pptx");
  await capture("/api/" + file + "/file?version=2&preview=1", "course-v2.pdf");
  await page
    .getByText("预览 deliverable.pptx", { exact: true })
    .first()
    .click();
  const preview = page.getByLabel("文件预览 deliverable.pptx").first();
  await preview.getByText("第 1 / 3 页").waitFor();
  await preview.getByRole("button", { name: "下一页" }).click();
  await preview.locator('canvas[data-rendered="true"]').waitFor();
  await preview.getByText("本页可读取文字").click();
  await preview.getByText(replacement, { exact: false }).waitFor();
  await preview.screenshot({ path: dir + "/course-review-mobile.png" });
  await feedback.getByLabel("处理方式").selectOption("resolve");
  await feedback.getByLabel("处理依据").fill("核对了第二页原件和日期字段。");
  await feedback.getByRole("button", { name: "记录决定" }).click();
  await page.getByRole("button", { name: "审核此版本" }).click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = page.waitForEvent("download");
  await page.getByRole("link", { name: "下载交付包（ZIP）" }).click();
  await (await download).saveAs(dir + "/course-delivery.zip");
  assert.equal((await read()).stage, "delivered");
  const { id: model } = await call(
    admin,
    "resources",
    "POST",
    {
      team,
      name: "合成课件编稿模型",
      endpoint: process.env.QUICK_ENDPOINT,
      model: "synthetic-course",
      key: "synthetic",
    },
    201,
  );
  const writerName = "设备制度编稿人";
  const writer = await call(
    page,
    "agents",
    "POST",
    {
      name: writerName,
      instructions: "根据制度原件分配页标题，保留每条规定的责任人和动作。",
      resource: model,
      resourceRevision: 1,
    },
    201,
  );
  await call(
    page,
    "agents/" + writer.id + "/validate",
    "POST",
    {
      revision: 1,
      input: "试连通",
      expected: "RECEIPT",
      authorizeOneCall: true,
    },
    200,
    "outline-writer-fit",
  );
  await call(
    admin,
    "agents/" + writer.id + "/publish",
    "POST",
    { revision: 1 },
    204,
  );
  await page.getByRole("button", { name: "新对话", exact: true }).click();
  await launch.click();
  await panel.getByLabel("搜索 Agent").fill(goal);
  await panel.locator("button.agent-choice").click();
  await page.getByRole("button", { name: "让 Agent 编稿" }).click();
  await page.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/course-writer-picker.png",
  });
  await page
    .getByRole("dialog", { name: "选择编稿 Agent" })
    .getByRole("button", { name: new RegExp(writerName) })
    .click();
  await message.fill("把附件中的两条制度做成两页课件。");
  const appendix = [
    "归还时由保管员登记？",
    "归还时由保管员登记。",
    ...Array.from({ length: 13 }, (_, i) => "📝 附加说明 " + (i + 1) + "。"),
  ].join("\n");
  await page
    .locator("input[type=file]")
    .last()
    .setInputFiles([
      {
        name: "制度原件.txt",
        mimeType: "text/plain",
        buffer: Buffer.from(
          "仓库编号为 QK-94。仓库 QK-94 的设备经部门负责人确认后领取。归还时由保管员核对设备编号并登记。",
        ),
      },
      {
        name: "附加说明.txt",
        mimeType: "text/plain",
        buffer: Buffer.from(appendix),
      },
    ]);
  await page.getByRole("button", { name: "发送消息" }).click();
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const combined = "projects/" + new URL(page.url()).hash.slice(1);
  const combinedRead = () => call(page, combined, "GET", undefined, 200);
  const selected = await combinedRead();
  assert.deepEqual(
    selected.plan.nodes.map((n) => n.agent),
    [writer.id, agent.id],
  );
  await page.getByLabel(/授权本次 2/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  await page.getByRole("button", { name: "审核此版本" }).nth(1).waitFor();
  const generated = (await combinedRead()).artifacts;
  assert.equal(generated[0].body, generated[1].body);
  assert.match(generated[1].body, /## 设备领取[\s\S]*QK-94[\s\S]*## 设备归还/);
  const combinedFile = combined + "/artifacts/" + generated[1].id;
  await capture(
    "/api/" + combinedFile + "/file?version=1",
    "course-composed.pptx",
  );
  const inputs = await call(page, combined + "/sources", "GET", undefined, 200);
  const pin = inputs.find((s) => s.name === "制度原件.txt"),
    other = inputs.find((s) => s.name === "附加说明.txt");
  assert.equal(selected.plan.sources.length, 2);
  const supplement =
    combinedFile + "/source-comparison/" + other.id + "?version=1";
  const firstPage = await call(page, supplement, "GET", undefined, 200);
  assert.equal(firstPage.total, 15);
  assert.deepEqual(firstPage.rows[0].matches, []);
  assert.deepEqual(firstPage.rows[1].matches, [2]);
  const nextPage = await call(
    page,
    supplement + "&page=2",
    "GET",
    undefined,
    200,
  );
  assert.equal(nextPage.rows.length, 3);
  for (const row of [...firstPage.rows, ...nextPage.rows]) {
    assert.equal(
      Array.from(appendix)
        .slice(row.start - 1, row.end)
        .join(""),
      row.text,
    );
  }

  const compares = combinedFile + "/source-comparison/" + pin.id;
  const readCompare = (version, query = "") =>
    call(page, compares + "?version=" + version + query, "GET", undefined, 200);
  const beforeCompare = await readCompare(1);
  assert.equal(beforeCompare.unmatched, 2);
  assert.equal(beforeCompare.all, 3);
  assert.deepEqual(beforeCompare.rows[1].matches, [1]);
  const missing = "归还时由保管员核对设备编号并登记。";
  assert.equal(beforeCompare.rows[2].text, missing);
  assert.equal(beforeCompare.rows[2].suggested, 2);
  const chars = Array.from(
    "仓库编号为 QK-94。仓库 QK-94 的设备经部门负责人确认后领取。" + missing,
  );
  assert.equal(
    chars
      .slice(beforeCompare.rows[2].start - 1, beforeCompare.rows[2].end)
      .join(""),
    missing,
  );
  await call(admin, compares + "?version=1", "GET", undefined, 404);
  await call(page, compares + "?version=2", "GET", undefined, 404);
  await call(page, compares + "?version=1&page=2", "GET", undefined, 422);
  await call(
    page,
    combinedFile +
      "/source-comparison/" +
      planned.plan.sources[0].id +
      "?version=1",
    "GET",
    undefined,
    404,
  );
  const deckCard = page.locator("article.card").filter({
    has: page.getByRole("heading", { name: goal + " · v1", exact: true }),
  });
  await deckCard.getByText("与方案原文对照", { exact: true }).click();
  const comparison = deckCard.getByRole("region", { name: "原句对照" });
  await comparison.getByLabel("方案引用的资料").selectOption(other.id);
  await comparison.getByRole("button", { name: "下一组原句" }).click();
  await comparison.getByText("📝 附加说明 13。", { exact: true }).waitFor();
  await comparison.getByLabel("方案引用的资料").selectOption(pin.id);
  await comparison.getByLabel(/只看未逐字出现/).check();
  const flagged = comparison
    .locator("article")
    .filter({ has: page.getByText(missing, { exact: true }) });
  await flagged.getByText("未找到相同原句", { exact: true }).waitFor();
  assert.equal(await flagged.getByLabel("反馈到哪一页？").inputValue(), "2");
  await flagged.scrollIntoViewIfNeeded();
  await page.screenshot({ path: dir + "/source-comparison-mobile.png" });
  await flagged.getByRole("button", { name: "起草核对意见" }).click();
  const directed = deckCard.locator("details.feedback");
  assert.equal(
    await directed.getByLabel("位置（选填）").inputValue(),
    "第 2 页",
  );
  assert.match(
    await directed.getByLabel("反馈内容").inputValue(),
    /制度原件.txt.*第 .* 字/,
  );
  await directed.getByLabel("反馈类型").selectOption("change");
  const anchor = {
    source: pin.id,
    version: pin.version,
    start: beforeCompare.rows[2].start,
    end: beforeCompare.rows[2].end,
    hash: beforeCompare.rows[2].hash,
  };
  await call(
    page,
    combinedFile + "/feedback",
    "POST",
    {
      version: 1,
      kind: "change",
      body: "错误位置",
      sourceQuote: { ...anchor, start: anchor.start + 1 },
    },
    409,
  );
  await call(
    page,
    combinedFile + "/feedback",
    "POST",
    {
      version: 1,
      kind: "change",
      body: "错误摘要",
      sourceQuote: { ...anchor, hash: "invalid" },
    },
    409,
  );
  assert.equal((await combinedRead()).artifacts[1].feedback.length, 0);
  await directed.getByRole("button", { name: "提交反馈" }).click();
  await directed.getByText("交给 " + goal + " 修改", { exact: true }).click();
  assert.equal(await directed.getByLabel("要修改哪一页？").inputValue(), "2");
  const anchored = (await combinedRead()).artifacts[1].feedback[0];
  assert.equal(anchored.quoteAvailable, true);
  await directed.getByText("查看反馈引用的原句", { exact: true }).click();
  await directed.getByText(missing, { exact: true }).waitFor();
  assert.equal(
    await directed
      .getByRole("link", { name: "打开引用的资料版本" })
      .getAttribute("href"),
    "/api/" + combined + "/sources/" + pin.id + "/content?version=1",
  );
  const quoteURL =
    combinedFile + "/feedback/" + anchored.id + "/source-quote?version=";
  assert.equal(
    (await call(page, quoteURL + "1", "GET", undefined, 200)).text,
    missing,
  );
  await call(admin, quoteURL + "1", "GET", undefined, 404);
  const bodyField = directed.getByLabel("第 2 页正文");
  await bodyField.evaluate((el) => {
    el.focus();
    el.select();
  });
  await directed.getByRole("button", { name: "从反馈引用插入原句" }).click();
  await page.waitForFunction(
    (expected) =>
      document.querySelector('textarea[name="slideBody"]')?.value === expected,
    missing,
  );
  assert.equal(await bodyField.inputValue(), missing);
  await directed.getByLabel(/授权负责此成果/).check();
  await directed.getByRole("button", { name: "启动局部修改" }).click();
  await page.getByText("待审核人复核", { exact: true }).waitFor();
  const afterCompare = await readCompare(2);
  assert.equal(afterCompare.unmatched, 1);
  assert.deepEqual(afterCompare.rows[2].matches, [2]);
  const filtered = await readCompare(2, "&filter=unmatched");
  assert.deepEqual(
    filtered.rows.map((r) => r.text),
    ["仓库编号为 QK-94。"],
  );
  await call(page, compares + "?version=1", "GET", undefined, 404);
  await capture(
    "/api/" + combinedFile + "/file?version=2",
    "source-comparison-corrected.pptx",
  );
  const originalText = await call(
    page,
    combined +
      "/artifacts/" +
      generated[0].id +
      "/source-comparison/" +
      pin.id +
      "?version=1",
    "GET",
    undefined,
    200,
  );
  assert.equal(originalText.unmatched, 2);
  await writeFile(
    dir + "/source-comparison.json",
    JSON.stringify(
      {
        beforeCompare,
        afterCompare,
        filtered,
        originalText,
        appendixPage1: firstPage,
        appendixPage2: nextPage,
      },
      null,
      2,
    ),
  );
  // An external package with a deliberately identical text summary is a different file.
  const trials = await call(page, path + "/evidence", "GET", undefined, 200);
  const original = await page.request.get(
    "/api/" + path + "/evidence/" + trials.find((t) => t.file).id + "/file",
  );
  const external = await page.request.post(
    "/api/" + combinedFile + "/file-revisions",
    {
      multipart: {
        version: "2",
        reason: "外部重新制作",
        summary: generated[1].body,
        file: {
          name: "外部版本.pptx",
          mimeType:
            "application/vnd.openxmlformats-officedocument.presentationml.presentation",
          buffer: await original.body(),
        },
      },
    },
  );
  assert.equal(external.status(), 201, await external.text());
  const imported = (await combinedRead()).artifacts[1];
  assert.equal(imported.version, 3);
  assert.equal(imported.editMode, "externalOutline");
  const newNote = await call(
    page,
    combinedFile + "/feedback",
    "POST",
    { version: 3, kind: "change", body: "请核对外部文件" },
    200,
  );
  await call(
    page,
    combinedFile + "/feedback/" + newNote.id + "/change-runs",
    "POST",
    {
      version: 3,
      expected: 1,
      slide: { page: 1, title: "外部第1页", body: "不能按旧提纲重排" },
      authorizeOneCall: true,
    },
    409,
    "course-external-protected",
  );
  await call(page, compares + "?version=3", "GET", undefined, 404);
  const checkText =
    combined +
    "/artifacts/" +
    generated[0].id +
    "/source-comparison/" +
    pin.id +
    "?version=1";
  const changedSource = await page.request.put(
    "/api/" +
      combined +
      "/sources/" +
      pin.id +
      "?version=1&name=" +
      encodeURIComponent("制度新版.txt"),
    {
      data: Buffer.from("新版资料缺少已归档的条款。"),
      headers: { "content-type": "application/octet-stream" },
    },
  );
  assert.equal(changedSource.status(), 201, await changedSource.text());
  const pinned = await call(page, checkText, "GET", undefined, 200);
  assert.equal(pinned.pin, 1);
  assert.deepEqual(pinned.rows, originalText.rows);
  const textCard = page
    .locator("article.card")
    .filter({
      has: page.getByRole("heading", {
        name: writerName + " · v1",
        exact: true,
      }),
    });
  await textCard.getByText("与方案原文对照", { exact: true }).click();
  const liveComparison = textCard.getByRole("region", { name: "原句对照" });
  await liveComparison.getByLabel("方案引用的资料").selectOption(pin.id);
  await liveComparison.getByText(missing, { exact: true }).waitFor();
  await call(
    page,
    combined + "/sources/" + pin.id + "?version=2",
    "DELETE",
    undefined,
    204,
  );
  await call(page, checkText, "GET", undefined, 404);
  await liveComparison
    .getByText(missing, { exact: true })
    .waitFor({ state: "hidden", timeout: 22000 });
  await call(page, quoteURL + "3", "GET", undefined, 404);
  assert.equal(
    (await combinedRead()).artifacts[1].feedback.find(
      (f) => f.id === anchored.id,
    ).quoteAvailable,
    false,
  );
  // A single plain sentence contains no slide boundaries. Keep the negative receipt.
  const refused = await call(
    page,
    "projects",
    "POST",
    { goal: "一段没有分页的正文", team, agent: agent.id, agentRevision: 1 },
    201,
  );
  const before = await call(
    page,
    "projects/" + refused.id,
    "GET",
    undefined,
    200,
  );
  await call(
    page,
    "projects/" + refused.id + "/runs",
    "POST",
    { expected: before.revision, authorizeCalls: 1 },
    202,
    "course-known-refusal",
  );
  await page.goto("/#" + refused.id);
  await page.getByText(/课件未生成成果。请用/).waitFor();
  const rejected = await call(
    page,
    "projects/" + refused.id,
    "GET",
    undefined,
    200,
  );
  assert.equal(rejected.run.attempts[0].status, "input_rejected");
  assert.equal(rejected.artifacts.length, 0);
  await writeFile(
    dir + "/course-deck.json",
    JSON.stringify(
      {
        project: id,
        composedProject: selected.id,
        rejectedProject: refused.id,
        externalSlideEdit: 409,
        agent: agent.id,
        trialFailed: bad.status,
        sourceVersion: 1,
        slideEdited: 2,
        reviewedVersion: 2,
        editRun: replay.run,
        rows,
        replacement,
      },
      null,
      2,
    ),
  );
} catch (error) {
  await page.screenshot({ path: dir + "/course-diagnostic.png" });
  console.error((await page.locator("body").innerText()).slice(-2200));
  throw error;
} finally {
  await browser.close();
}

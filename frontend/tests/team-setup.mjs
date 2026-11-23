import { verifyWordSourceComparison } from "./word-source-comparison.mjs";
import { checkServiceEntry } from "./service-entry.mjs";
import { checkModelMaintenance } from "./model-maintenance.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";
const browser = await chromium.launch();
try {
  const baseURL = process.env.SMOKE_ORIGIN,
    out = process.env.VISUAL_EVIDENCE;
  const admin = await browser.newContext({ baseURL });
  const page = await admin.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto("/");
  for (const [label, value] of [
    ["企业名称", "合成配置企业"],
    ["安装凭证", process.env.BOOTSTRAP_TOKEN],
    ["姓名", "配置负责人"],
    ["邮箱", "setup@sample.invalid"],
    ["密码", process.env.BROWSER_PASSWORD],
  ])
    await page.getByLabel(label, { exact: true }).fill(value);
  await page.getByRole("button", { name: "创建首位管理员" }).click();
  const panel = page.getByRole("dialog", { name: "团队首次配置" });
  await panel.getByText("0 / 3 个助手可用").waitFor();
  assert.equal(await panel.getByLabel("配置团队").count(), 0);
  const joined = (await (await admin.request.get("/api/teams")).json()).filter(
    (t) => t.joined,
  );
  assert.equal(joined.length, 1);
  const team = joined[0].id;
  await panel.getByText(joined[0].name + "承担本次配置的资源费用。").waitFor();
  const root = "/api/teams/" + team + "/setup";
  const info = async () => (await (await admin.request.get(root)).json()).state;
  await panel.getByText(/接入团队模型 ·/).click();
  await panel.getByLabel("资源名称", { exact: true }).fill("试点文本模型");
  await panel.getByLabel(/模型服务根地址/).fill(process.env.SETUP_ENDPOINT);
  await panel.getByLabel(/模型 ID/).fill("setup-text");
  await panel.getByLabel("供应商密钥").fill("synthetic-setup-key");
  await panel.getByRole("button", { name: "保存模型资源" }).click();
  await panel.getByText(/接入团队模型 · 1 项/).waitFor();
  const model = (await (await admin.request.get("/api/resources")).json())[0];
  assert.equal(model.team, team);
  await panel.getByText("试点文本模型 v1 · 未做独立连通验证").click();
  await panel.getByLabel(/授权发送一次合成连通请求/).check();
  const probeRequest = page.waitForRequest(
    (req) =>
      req.url().endsWith(root + "/connections") && req.method() === "POST",
  );
  await panel.getByRole("button", { name: "验证模型连通" }).click();
  await panel.getByText("试点文本模型 v1 · 连通通过").waitFor();
  const request = await probeRequest;
  const replayProbe = await admin.request.post(root + "/connections", {
    data: request.postDataJSON(),
    headers: { "Idempotency-Key": request.headers()["idempotency-key"] },
  });
  assert.equal((await replayProbe.json()).status, "passed");
  assert.equal((await info()).connections.length, 1);
  assert.deepEqual((await info()).slots, {});
  await panel.getByText(/规划助手默认模型 ·/).click();
  await panel.getByLabel("新项目的规划模型").selectOption(model.id);
  await panel.getByRole("button", { name: "设为新项目默认模型" }).click();
  await panel.getByText("规划助手默认模型 · 试点文本模型 v1").waitFor();
  const other = (
    await (
      await admin.request.post("/api/admin/teams", {
        data: { name: "另一团队" },
      })
    ).json()
  ).id;
  assert.equal(
    (
      await admin.request.put("/api/teams/" + other + "/setup/writer", {
        data: { resource: model.id, revision: 1 },
      })
    ).status(),
    422,
  );
  const names = ["简报整理助手", "简报核对助手", "Word 成稿助手"];
  for (const [i, name] of names.entries()) {
    const row = panel
      .locator("article")
      .filter({ has: page.getByRole("heading", { name, exact: true }) });
    if (i < 2) await row.getByLabel("绑定文本模型").selectOption(model.id);
    await row.getByRole("button", { name: "按模板创建草稿" }).click();
    await row.getByText(/必要检查 · v1/).click();
    const run = async () => {
      await row.getByRole("checkbox", { name: /授权以共享样例/ }).check();
      await row.getByRole("button", { name: "运行此检查" }).click();
    };
    await run();
    if (i === 0) {
      await row.getByText("保留资料编号 · 未通过", { exact: true }).waitFor();
      await row.getByText(/此修订已有连通回执/).waitFor();
      assert.equal((await info()).slots.writer.ready, false);
      await run();
    }
    await row.getByRole("button", { name: "发布 v1 供团队使用" }).click();
    await row.getByText(/可使用已发布 v1/).waitFor();
  }
  const state = await info(),
    ordered = ["writer", "checker", "document"];
  const config = {
    agents: ordered.map((k) => state.slots[k].agent),
    revisions: [1, 1, 1],
  };
  const repeat = await admin.request.put(root + "/writer", {
    data: { resource: model.id, revision: 1 },
  });
  assert.equal((await repeat.json()).agent, config.agents[0]);
  assert.equal(state.passed, false);
  const creation = page.waitForRequest(
    (req) => req.method() === "POST" && req.url().endsWith(root + "/samples"),
  );
  await panel.getByRole("button", { name: "创建协作验证项目" }).click();
  const key = (await creation).headers()["idempotency-key"];
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const project = new URL(page.url()).hash.slice(1);
  const replay = await admin.request.post(root + "/samples", {
    data: config,
    headers: { "Idempotency-Key": key },
  });
  assert.equal((await replay.json()).id, project);
  assert.equal(
    (
      await admin.request.post(root + "/samples", {
        data: { ...config, agents: config.agents.toReversed() },
        headers: { "Idempotency-Key": key },
      })
    ).status(),
    409,
  );
  const p = await (await admin.request.get("/api/projects/" + project)).json();
  assert.equal(p.plan.sources.length, 1);
  assert.deepEqual(
    p.plan.nodes.map((n) => n.dependsOn),
    [[], [1], [2]],
  );
  await page.getByText("根据对话推荐方案或直接回答").click();
  assert.equal(await page.getByLabel("规划助手模型").inputValue(), model.id);
  await page.getByLabel(/授权一次规划调用/).check();
  await page.getByRole("button", { name: "请助手规划" }).click();
  await page.getByRole("button", { name: "采用为待确认方案" }).click();
  await page.getByRole("heading", { name: "协作方案 v2" }).waitFor();
  const adopted = await (
    await admin.request.get("/api/projects/" + project)
  ).json();
  assert.deepEqual(adopted.plan.sources, p.plan.sources);
  await page.getByLabel(/授权本次 3/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  for (let i = 0; i < 3; i++)
    await page.getByRole("button", { name: "审核此版本" }).first().click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).waitFor();
  assert.equal((await info()).passed, false);
  const wordCard = page
    .locator("article.card")
    .filter({ has: page.getByRole("heading", { name: /Word 成稿助手/ }) });
  await wordCard.getByText("与方案原文对照", { exact: true }).click();
  const comparison = wordCard.getByRole("region", { name: "原句对照" });
  await comparison.getByText(/对照成果 v1/).waitFor();
  assert.ok((await comparison.locator("article").count()) > 0);
  await comparison.screenshot({ path: out + "/word-source-comparison.png" });
  const feedback = wordCard.locator("details.feedback");
  await feedback.locator(":scope > summary").click();
  await feedback.getByLabel("反馈类型").selectOption("change");
  await feedback.getByLabel("反馈内容").fill("在简报末尾增加核对日期。");
  await feedback.getByRole("button", { name: "提交反馈" }).click();
  await feedback.getByText("已要求修改", { exact: true }).waitFor();
  await feedback.getByText(/交给 .* 修改/).click();
  await feedback
    .getByLabel("用于重新生成文件的完整正文")
    .fill(
      "培训安排 PX-17。运营组负责设备登记，截止周三；培训组整理使用说明，截止周五。核对日期：11 月 21 日。",
    );
  await feedback
    .getByRole("checkbox", { name: /授权负责此成果的 Agent/ })
    .check();
  await feedback.getByRole("button", { name: "启动局部修改" }).click();
  await feedback.getByText("待审核人复核", { exact: true }).waitFor();
  await feedback.getByLabel("处理方式").selectOption("resolve");
  await feedback.getByLabel("处理依据").fill("已核对修订正文及日期");
  await feedback.getByRole("button", { name: "记录决定" }).click();
  await wordCard.getByRole("button", { name: "审核此版本" }).click();
  await comparison.getByText(/对照成果 v2/).waitFor();
  const reviewed = await (
    await admin.request.get("/api/projects/" + project)
  ).json();
  assert.deepEqual(
    reviewed.artifacts.map((a) => a.version),
    [1, 1, 2],
  );
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const link = page.getByRole("link", { name: "下载交付包（ZIP）" });
  await link.waitFor();
  const zip = await admin.request.get(await link.getAttribute("href"));
  assert.equal(zip.status(), 200);
  await writeFile(out + "/setup-delivery.zip", await zip.body());
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByRole("button", { name: "企业管理", exact: true }).click();
  await page.getByRole("button", { name: "团队首次配置" }).click();
  await panel.getByText(/当前发布组合已完成审核交付/).waitFor();
  await panel.getByText("默认规划助手的建议已被采纳并完成交付").waitFor();
  await panel.screenshot({ path: out + "/team-setup.png" });
  const invite = (
    await (
      await admin.request.post("/api/admin/invitations", { data: {} })
    ).json()
  ).token;
  const member = await browser.newContext({ baseURL });
  const password = crypto.randomUUID();
  const registered = await member.request.post("/api/register", {
    data: {
      token: invite,
      email: "staff@sample.invalid",
      name: "参与者",
      password,
    },
  });
  assert.equal(registered.status(), 201);
  const memberState = (await (await member.request.get(root)).json()).state;
  assert.ok(memberState.passed);
  assert.equal(memberState.projects.length, 0);
  const staff = await member.newPage();
  await staff.setViewportSize({ width: 390, height: 844 });
  await staff.goto("/");
  const goal = "用简报整理助手整理采购计划，保留负责人和期限";
  const message = staff.getByRole("textbox", { name: "消息", exact: true });
  await message.fill(goal);
  await staff.getByRole("group", { name: "适合这项任务的 Agent" })
    .getByRole("button", { name: /简报整理助手/ }).waitFor();
  await staff.getByRole("button", { name: "我的工作区" }).click();
  await staff.getByText("协作与授权", { exact: true }).click();
  assert.equal(await staff.getByRole("button", { name: "团队首次配置" }).count(), 0);
  await staff.getByRole("button", { name: "关闭", exact: true }).click();
  await staff.getByRole("button", { name: "选择或创建 Agent" }).click();
  await staff.getByRole("button", { name: "内置 Agent 模板" }).click();
  const templates = staff.getByRole("dialog", { name: "内置 Agent 模板" });
  const writerTemplate = templates.getByRole("article").filter({ hasText: "简报整理助手" });
  await writerTemplate.getByRole("button", { name: "用于当前对话" }).click();
  assert.equal(await message.inputValue(), goal);
  assert.deepEqual((await (await member.request.get("/api/projects")).json()) ?? [], []);
  const [submitted] = await Promise.all([
    staff.waitForResponse((r) => r.url().endsWith("/api/projects") && r.request().method() === "POST"),
    staff.getByRole("button", { name: "发送消息" }).click(),
  ]);
  assert.equal(submitted.status(), 201);
  const memberProject = (await submitted.json()).id;
  const selected = await (await member.request.get("/api/projects/" + memberProject)).json();
  assert.equal(selected.plan.nodes[0].agent, memberState.slots.writer.agent);
  assert.equal(selected.plan.nodes[0].revision, memberState.slots.writer.published);
  assert.equal(selected.stage, "plan");
  await staff.close();
  assert.equal(
    (
      await member.request.post(
        "/api/agents/" + config.agents[0] + "/publish",
        { data: { revision: 1 } },
      )
    ).status(),
    403,
  );
  assert.equal(
    (
      await member.request.put(root + "/planner", {
        data: { resource: model.id, revision: 1, expected: 1 },
      })
    ).status(),
    403,
  );
  await checkModelMaintenance({
    admin,
    member,
    panel,
    root,
    model,
    project,
    out,
    writer: state.slots.writer.agent,
  });
  await checkServiceEntry({
    browser,
    baseURL,
    admin,
    member,
    team,
    project,
    documentAgent: state.slots.document.agent,
    password,
    out,
  });
  const newer = await admin.request.post("/api/resources", {
    data: {
      id: model.id,
      name: model.name,
      endpoint: process.env.SETUP_ENDPOINT,
      model: "setup-text",
      key: "synthetic-new-key",
      expected: 1,
    },
  });
  assert.equal(newer.status(), 201);
  const replace = await admin.request.put(root + "/planner", {
    data: { resource: model.id, revision: 2, expected: 1 },
  });
  assert.equal(replace.status(), 204);
  assert.equal(
    (
      await admin.request.put(root + "/planner", {
        data: { resource: model.id, revision: 1, expected: 1 },
      })
    ).status(),
    409,
  );
  const changed = await info();
  assert.equal(changed.planned, false);
  assert.equal(changed.passed, true);
  assert.equal(
    (await (await admin.request.get("/api/projects/" + project)).json())
      .planningModel.revision,
    1,
  );
  const next = (
    await (
      await admin.request.post("/api/projects", {
        data: { goal: "验证新项目默认模型" },
      })
    ).json()
  ).id;
  assert.equal(
    (await (await admin.request.get("/api/projects/" + next)).json())
      .planningModel.revision,
    2,
  );
  const lifecycle = "/api/admin/catalog/models/" + model.id;
  assert.equal(
    (
      await admin.request.post(lifecycle, {
        data: { action: "disable", reason: "检查当前配置可用状态" },
      })
    ).status(),
    204,
  );
  const after = await info();
  assert.equal(after.passed, false);
  assert.equal(after.slots.document.ready, true);
  await verifyWordSourceComparison(
    admin,
    team,
    after.slots.document.agent,
    after.slots.document.published,
    "/api/projects/" +
      project +
      "/artifacts/" +
      reviewed.artifacts[2].id +
      "/file?version=2",
    out,
  );
  assert.deepEqual(errors, []);
  await writeFile(
    out + "/team-setup.json",
    JSON.stringify(
      {
        provider: "controlled HTTPS, synthetic data",
        firstAdmin: "opened setup",
        independentConnectivityProbe: true,
        probeReplayRows: 1,
        failedTrialConnected: true,
        plannerAdopted: true,
        preservedSourceAfterPlanning: true,
        readyBeforePublication: false,
        reviewedBeforePass: true,
        feedbackRevisions: [1, 1, 2],
        replayedProject: project,
        privateProjectHidden: true,
        memberTemplate: { selected: selected.plan.nodes[0].agent, revision: selected.plan.nodes[0].revision },
        disabledModelReadyCount: Object.values(after.slots).filter(
          (s) => s.ready,
        ).length,
      },
      null,
      2,
    ),
  );
  console.log(
    "Browser: fresh enterprise, scoped resource, failed and successful checks, three-step reviewed Word delivery and partial availability",
  );
} finally {
  await browser.close();
}

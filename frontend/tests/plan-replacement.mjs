import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const { SMOKE_ORIGIN: baseURL, BROWSER_PASSWORD: password, VISUAL_EVIDENCE: evidence } = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const member = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  const request = async (page, path, data, status = 200, headers) => {
    const response = data === undefined
      ? await page.request.get("/api/" + path)
      : await page.request.post("/api/" + path, { data, headers });
    const body = await response.text();
    assert.equal(response.status(), status, path + " " + body);
    return body ? JSON.parse(body) : undefined;
  };
  await request(admin, "login", { email: "review@sample.invalid", password });
  const { token } = await request(admin, "admin/invitations", {}, 201);
  const user = await request(member, "register", {
    token, email: "plan-replacement@sample.invalid", password, name: "编排测试成员",
  }, 201);

  const docx = await request(admin, "agents/tools", {
    name: "借用说明排版",
    description: "将上一步文本整理为正式 Word 说明。",
    tool: { operation: "docx" },
  }, 201);
  const trial = await request(admin, "agents/" + docx.id + "/validate", {
    revision: 1, input: "设备借用说明", expected: "设备借用说明", authorizeOneCall: true,
  });
  assert.equal(trial.status, "passed");
  await request(admin, "agents/" + docx.id + "/publish", { revision: 1 }, 204);
  const catalog = await request(member, "agents");
  const writer = catalog.find(agent => agent.publishedName === "Writer");
  const checker = catalog.find(agent => agent.publishedName === "Checker");
  assert.ok(writer?.publishedReady && checker?.publishedReady && writer.publishedLookup && !checker.publishedLookup);

  const goal = "完成设备借用说明并核对格式";
  const { id } = await request(member, "projects", { goal }, 201);
  const base = "projects/" + id;
  const load = () => request(member, base);
  const save = (body, status = 204) => request(member, base + "/plan", body, status);
  const source = await request(member, base + "/sources?name=borrowing.txt",
    Buffer.from("借用设备前需提交主管审批。"), 201);
  const pref = await request(member, "preferences", { body: "保留审批要求", enabled: true });
  const inputs = {
    brief: goal,
    agents: [writer.id, writer.id, docx.id, writer.id],
    agentRevisions: [writer.published, writer.published, 1, writer.published],
    dependencies: [[], [], [2], [3]],
    sources: [{ id: source.id, version: 1 }],
    preferences: [{ id: pref.id, revision: 1 }],
  };
  await save({ ...inputs, expected: (await load()).revision });
  const original = await load();
  const wrong = {
    ...inputs,
    expected: original.revision,
    agents: [writer.id, docx.id, docx.id, writer.id],
    agentRevisions: [writer.published, 1, 1, writer.published],
    replacementBase: original.planVersion,
    replacements: [{ from: 2, step: 2 }],
  };
  const incompatible = await save(wrong, 422);
  assert.match(incompatible.error, /步骤 2.*不兼容/);
  await save({ ...wrong, replacementBase: original.planVersion + 1 }, 409);
  assert.equal((await load()).planVersion, original.planVersion);
  assert.equal((await load()).run, null);

  await member.goto("/#" + id);
  await member.getByText("拟定协作方案", { exact: true }).click();
  await member.getByLabel(/授权本次 4/).check();
  await member.getByRole("button", { name: "更换步骤 2 的 Agent" }).click();
  const options = member.getByLabel("步骤 2 的兼容 Agent");
  await options.waitFor();
  assert.equal(await options.getByText("借用说明排版", { exact: false }).count(), 0);
  await member.getByText("步骤 3、4 将使用替换后的结果。", { exact: true }).waitFor();
  await options.evaluate(el => el.scrollIntoView({ block: "center" }));
  await member.screenshot({ path: join(evidence, "plan-replacement-mobile.png") });
  await options.selectOption(checker.id + "/" + checker.published);
  await member.locator(".plan-steps > li").nth(1).getByText("Checker", { exact: false }).waitFor();
  assert.equal((await load()).planVersion, original.planVersion);
  const submit = member.getByRole("button", { name: "保存待确认方案" });
  await save({ ...inputs, brief: goal + "，保留最新审核要求", expected: original.revision });
  const [superseded] = await Promise.all([
    member.waitForResponse(response => response.url().endsWith("/api/" + base + "/plan") &&
      response.request().method() === "POST"),
    submit.click(),
  ]);
  assert.equal(superseded.status(), 409);
  await member.getByText(/原方案已另存新版/).waitFor();
  assert.ok(await submit.isDisabled());
  assert.equal(await member.getByRole("button", { name: "已核对，继续使用我的草稿" }).count(), 0);
  assert.equal((await load()).plan.nodes[1].agent, writer.id);
  await member.getByRole("button", { name: "丢弃草稿，使用当前内容" }).click();
  await member.getByText("拟定协作方案", { exact: true }).click();
  await member.getByRole("button", { name: "更换步骤 2 的 Agent" }).click();
  await options.selectOption(checker.id + "/" + checker.published);
  await submit.click();
  await member.getByRole("heading", { name: "协作方案 v3" }).waitFor();
  const changed = await load();
  assert.deepEqual(changed.plan.nodes.map(node => node.agent), [writer.id, checker.id, docx.id, writer.id]);
  assert.deepEqual(changed.plan.nodes.map(node => node.dependsOn), inputs.dependencies);
  assert.deepEqual(changed.plan.sources, inputs.sources);
  assert.deepEqual(changed.plan.preferences, inputs.preferences);
  assert.equal(changed.run, null);
  assert.equal(await member.getByLabel(/授权本次 4/).isChecked(), false);

  await member.getByLabel(/授权本次 4/).check();
  await member.getByRole("button", { name: "确认并执行", exact: true }).click();
  const approve = member.getByRole("button", { name: "审核此版本", exact: true });
  for (let i = 0; i < 4; i++) await approve.first().click();
  await member.getByRole("button", { name: "确认并固定以上版本" }).click();
  const zip = member.getByRole("link", { name: "下载交付包（ZIP）" });
  await zip.waitFor();
  const packet = await member.request.get(await zip.getAttribute("href"));
  assert.equal(packet.status(), 200);
  const delivered = await load();
  assert.equal(delivered.costSummary.calls, 4);
  assert.equal(delivered.artifacts.filter(item => item.approved).length, 4);
  assert.equal(delivered.stage, "delivered");
  await writeFile(join(evidence, "plan-replacement-delivery.zip"), await packet.body());
  await save({ ...wrong, expected: delivered.revision, replacementBase: changed.planVersion }, 409);

  const { id: lookup } = await request(member, "projects", { goal: "先核对原文再排版" }, 201);
  const lookupBase = "projects/" + lookup;
  const version = () => request(member, lookupBase);
  const originalText = await request(member, lookupBase + "/sources?name=reference.txt",
    Buffer.from("设备借用前需要主管审批。"), 201);
  const lookupPlan = {
    brief: "查阅审批规范然后排版",
    agents: [writer.id, docx.id],
    agentRevisions: [writer.published, 1],
    sources: [{ id: originalText.id, version: 1 }],
    lookupSteps: [1],
  };
  await request(member, lookupBase + "/plan", { ...lookupPlan, expected: (await version()).revision }, 204);
  const lookupOriginal = await version();
  assert.equal(lookupOriginal.plan.nodes.length, 3);
  await request(member, lookupBase + "/plan", {
    ...lookupPlan, expected: lookupOriginal.revision, replacementBase: lookupOriginal.planVersion,
    agents: [writer.id, checker.id], agentRevisions: [writer.published, checker.published],
    replacements: [{ from: 2, step: 2 }],
  }, 422);
  await member.goto("/#" + lookup);
  await member.getByText("拟定协作方案", { exact: true }).click();
  await member.getByRole("button", { name: "更换步骤 1 的 Agent" }).click();
  assert.equal(await member.getByLabel("步骤 1 的兼容 Agent").getByText(/Checker/).count(), 0);

  const { id: otherTeam } = await request(admin, "admin/teams", { name: "其他分类团队" }, 201);
  const joined = await admin.request.put("/api/admin/teams/" + otherTeam + "/members/" + user.id, {
    data: { joined: true, reason: "核对工具选择的团队边界" },
  });
  assert.equal(joined.status(), 204);
  const otherModel = await request(admin, "resources", {
    team: otherTeam, name: "其他团队的文本模型", model: "fixture",
    endpoint: process.env.QUICK_ENDPOINT, key: "local-fixture",
  }, 201);
  const otherWriter = await request(admin, "agents", {
    name: "其他团队的文本 Agent", instructions: "提供核对回执",
    resource: otherModel.id, resourceRevision: 1,
  }, 201);
  const verify = async (agent, input, expected) => {
    const response = await request(admin, "agents/" + agent + "/validate", { revision: 1, input, expected, authorizeOneCall: true });
    assert.equal(response.status, "passed");
    await request(admin, "agents/" + agent + "/publish", { revision: 1 }, 204);
  };
  await verify(otherWriter.id, "verify", "RECEIPT");
  assert.ok((await request(member, "agents")).some(agent => agent.id === otherWriter.id && agent.publishedLookup && agent.publishedReady));
  const data = "area,department,amount\nEast,service,12.30";
  const aggregates = [];
  for (const [group, name] of [["area", "地区汇总"], ["department", "部门汇总"]]) {
    const tool = await request(admin, "agents/tools", {
      name: name + "供编排走查", description: "按已批准的金额口径汇总",
      tool: { operation: "aggregate", config: { group, amount: "amount" } },
    }, 201);
    await verify(tool.id, data, "12.30");
    aggregates.push(tool.id);
  }
  const { id: routed } = await request(member, "projects", { goal: "在两个汇总口径中选择" }, 201);
  const routeBase = "projects/" + routed;
  const csv = await request(member, routeBase + "/sources?name=amounts.csv", Buffer.from(data), 201);
  await request(member, routeBase + "/plan", {
    brief: "核对地区或部门汇总口径",
    expected: (await request(member, routeBase)).revision,
    agents: [writer.id, aggregates[0]], agentRevisions: [writer.published, 1],
    dependencies: [[], [1]], sources: [{ id: csv.id, version: 1 }],
    toolChoices: [{ step: 2, selector: 1, alternative: aggregates[1], revision: 1 }],
  }, 204);
  await member.goto("/#" + routed);
  await member.getByText("拟定协作方案", { exact: true }).click();
  await member.getByRole("button", { name: "更换步骤 1 的 Agent" }).click();
  await member.getByText("工具选择使用项目所属团队的能力。", { exact: true }).waitFor();
  assert.equal(await member.getByLabel("步骤 1 的兼容 Agent").getByText(/其他团队/).count(), 0);
  await writeFile(join(evidence, "plan-replacement.json"), JSON.stringify({
    evidence: "Chromium with an ordinary member and isolated PostgreSQL, Temporal, real tools and TLS fixtures",
    project: id,
    previousPlan: original.planVersion,
    replacementPlan: changed.planVersion,
    dependencies: changed.plan.nodes.map(node => node.dependsOn),
    linkedSource: changed.plan.sources[0].version,
    preferenceRevision: changed.plan.preferences[0].revision,
    incompatibleFormat: 422,
    stalePlan: 409,
    concurrentEdit: superseded.status(),
    hiddenLookupNodeExcludedFromBusinessSteps: true,
    externalTextExcludedFromLookupChoices: true,
    otherTeamExcludedFromToolChoice: true,
    billedCalls: delivered.costSummary.calls,
    approvedArtifacts: delivered.artifacts.length,
  }, null, 2) + "\n");
} finally {
  await browser.close();
}

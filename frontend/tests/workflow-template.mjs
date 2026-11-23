import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const { SMOKE_ORIGIN: baseURL, BROWSER_PASSWORD: password, QUICK_ENDPOINT: endpoint, VISUAL_EVIDENCE: evidence } = process.env;
const browser = await chromium.launch();
try {
  const mobile = () => browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  const admin = await browser.newPage({ baseURL });
  const creator = await mobile(), member = await mobile(), outsider = await mobile();
  async function api(page, route, { method = "GET", body, status, headers } = {}) {
    const response = await page.request.fetch("/api/" + route, { method, data: body, headers });
    const text = await response.text();
    if (status !== undefined) assert.equal(response.status(), status, `${method} ${route}: ${text}`);
    else assert.ok(response.ok(), `${method} ${route}: ${text}`);
    return text ? JSON.parse(text) : undefined;
  }
  const post = (page, route, body, status, headers) => api(page, route, { method: "POST", body, status, headers });
  const key = () => ({ "Idempotency-Key": randomUUID() });
  await post(admin, "login", { email: "review@sample.invalid", password });
  const { id: team } = await post(admin, "admin/teams", { name: "材料核对组" });
  async function register(page, label, joinTeam) {
    const invite = await post(admin, "admin/invitations", {});
    const { id } = await post(page, "register", { name: label, email: `${page === creator ? "template-creator" : page === member ? "template-user" : "template-outsider"}@sample.invalid`, password, token: invite.token });
    if (joinTeam) await api(admin, `admin/teams/${team}/members/${id}`, {
      method: "PUT", body: { joined: true, reason: "本次模板验收" }, status: 204,
    });
    return id;
  }
  const originalOwner = await register(creator, "原执行人", true);
  const nextReviewer = await register(member, "新使用人", true);
  await register(outsider, "其他团队人员", false);
  const resource = await post(admin, "resources", { team, name: "协作验证模型", model: "fixture", endpoint, key: "synthetic", estimate: "0.07" });
  const writer = await post(admin, "agents", { name: "提炼进度摘要", instructions: "提供简短回执", resource: resource.id, resourceRevision: 1 });
  const former = await post(admin, "agents/tools", {
    team, name: "旧金额汇总", description: "按区域汇总当期凭证",
    tool: { operation: "aggregate", config: { group: "region", amount: "amount" } },
  });
  const replacement = await post(admin, "agents/tools", {
    team, name: "在用金额汇总", description: "按区域汇总当期凭证",
    tool: { operation: "aggregate", config: { group: "region", amount: "amount" } },
  });
  const docx = await post(admin, "agents/tools", { team, name: "汇编 Word 供审核", description: "将前置结果排成 Word", tool: { operation: "docx" } });
  for (const [agent, input, expected] of [
    [writer, "verify", "RECEIPT"], [former, "region,amount\nEast,7.00", "7.00"],
    [replacement, "region,amount\nEast,7.00", "7.00"], [docx, "交付原件", "交付原件"],
  ]) {
    const trial = await post(admin, `agents/${agent.id}/validate`, { revision: 1, input, expected, authorizeOneCall: true });
    assert.equal(trial.status, "passed");
    await post(admin, `agents/${agent.id}/publish`, { revision: 1 }, 204);
  }

  const secret = "PRIVATE-TEMPLATE-718";
  const { id: original } = await post(creator, "projects", { team, goal: secret + " 当期原始业务" });
  const originalPath = "projects/" + original;
  const initial = await post(creator, originalPath + "/sources?name=" + secret + ".csv",
    Buffer.from("region,amount\nEast,12.40"), 201, { "Content-Type": "application/octet-stream" });
  const shape = { agents: [writer.id, former.id, docx.id], agentRevisions: [1, 1, 1], dependencies: [[], [], [1, 2]] };
  await post(creator, originalPath + "/plan", {
    ...shape, brief: "根据 " + secret + " 私有说明核对实际金额", expected: (await api(creator, originalPath)).revision,
    sources: [{ id: initial.id, version: 1 }],
  }, 204);
  await creator.goto("/#" + original);
  async function execute(page, project, label, calls = 3) {
    await page.getByLabel(new RegExp("授权本次 " + calls)).check();
    await page.getByRole("button", { name: "确认并执行", exact: true }).click();
    const approve = page.getByRole("button", { name: "审核此版本", exact: true });
    for (let i = 0; i < calls; i += 1) await approve.first().click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    const zip = page.getByRole("link", { name: "下载交付包（ZIP）" });
    await zip.waitFor();
    const packet = await page.request.get(await zip.getAttribute("href"));
    assert.equal(packet.status(), 200);
    await writeFile(join(evidence, `workflow-template-${label}.zip`), await packet.body());
    const detail = await api(page, "projects/" + project);
    assert.equal(detail.costSummary.calls, calls);
    assert.equal(detail.artifacts.filter(item => item.approved).length, calls);
    return detail;
  }
  const sealed = await execute(creator, original, "original");

  const name = "月度材料归档";
  const fields = { name, instructions: "核对新一期材料后编制交付文件。", inputs: "包含地区及金额的本期 CSV。", outcomes: "摘要、汇总工作簿及 Word，审核后交付。" };
  await creator.getByText("保存为协作模板", { exact: true }).click();
  const form = creator.getByRole("form", { name: "保存协作模板" });
  assert.equal(await form.getByLabel("可复用的执行要求").inputValue(), "");
  for (const [label, field] of [["模板名称", "name"], ["可复用的执行要求", "instructions"], ["需要准备的资料", "inputs"], ["交付和审核约定", "outcomes"]]) {
    await form.getByLabel(label).fill(fields[field]);
  }
  await form.getByRole("button", { name: "保存模板草稿" }).click();
  await creator.getByText("已保存草稿 v1", { exact: true }).waitFor();
  const [draft] = await api(creator, "workflow-templates");
  assert.equal(draft.version, 1);
  assert.deepEqual(await api(member, "workflow-templates"), []);
  const resourcePath = "workflow-templates/" + draft.id;
  await api(member, resourcePath + "?version=1", { status: 404 });
  await post(member, resourcePath + "/projects", { goal: "尚未共享的模板", version: 1 }, 404, key());
  const personal = await post(creator, resourcePath + "/projects", { version: 1, goal: "个人核对准备" }, 201, key());
  const personalPath = "projects/" + personal.id;
  const personalDraft = await api(creator, personalPath);
  assert.equal(personalDraft.plan, null);
  assert.equal(personalDraft.workflowTemplate.version, 1);
  await post(creator, personalPath + "/plan", { ...shape, brief: "等待本次资料", expected: personalDraft.revision }, 204);
  const missingSources = await post(creator, personalPath + "/runs", {
    expected: (await api(creator, personalPath)).revision, authorizeCalls: 3,
  }, 409, key());
  assert.match(missingSources.error, /新的可读资料/);
  assert.equal((await api(creator, personalPath)).costSummary.calls, 0);
  await writeFile(join(evidence, "workflow-template-draft-admission.json"), JSON.stringify({
    personalTemplateUsedBeforePublication: true, missingSourcesStatus: 409, calls: 0,
  }, null, 2) + "\n");
  const expose = () => post(creator, resourcePath + "/publication", { action: "request", version: 1 }, 204);
  await creator.getByRole("button", { name: "打开协作模板" }).click();
  await creator.getByRole("button", { name: new RegExp(name + " · v1") }).click();
  await creator.getByRole("button", { name: "申请发布给团队" }).click();
  await creator.getByText("发布申请已提交", { exact: true }).waitFor();
  await post(creator, resourcePath + "/publication", { action: "publish", version: 1 }, 403);
  await post(admin, resourcePath + "/publication", { action: "return", version: 1, reason: "核对待审核资料类型" }, 204);
  assert.equal((await api(creator, resourcePath + "?version=1")).decision, "核对待审核资料类型");
  await expose();
  async function browseTemplates(page) {
    await page.goto("/");
    await page.getByRole("button", { name: "选择或创建 Agent", exact: true }).click();
    await page.getByRole("dialog", { name: "Agent", exact: true }).getByRole("button", { name: "协作模板", exact: true }).click();
  }
  await browseTemplates(admin);
  await admin.getByRole("button", { name: new RegExp(name + " · v1") }).click();
  await admin.getByRole("button", { name: "发布该版本" }).click();
  await admin.getByText("该版本已发布给团队").waitFor();
  const published = await api(member, resourcePath + "?version=1");
  assert.equal(published.requiresSources, true);
  for (const marker of [secret, initial.id, originalOwner, original, sealed.run.id]) assert.ok(!JSON.stringify(published).includes(marker));
  assert.deepEqual(published.nodes.map(node => node.dependsOn), shape.dependencies);
  await api(outsider, resourcePath + "?version=1", { status: 404 });
  assert.deepEqual(await api(outsider, "workflow-templates"), []);

  await post(admin, "admin/catalog/agents/" + former.id, { action: "disable", reason: "验证模板重新核对当前能力" }, 204);
  await browseTemplates(member);
  await member.getByRole("button", { name: new RegExp(name + " · v1") }).click();
  await member.getByText("需在新方案核对可用的 Agent。", { exact: true }).waitFor();
  const goal = "五月材料汇编，以新表制作交付";
  await member.getByLabel("本次任务目标").fill(goal);
  await member.getByRole("region", { name: "模板详情" }).screenshot({ path: join(evidence, "workflow-template-mobile.png") });
  const start = member.waitForRequest(r => r.method() === "POST" && r.url().endsWith(resourcePath + "/projects"));
  await member.getByRole("button", { name: "使用模板创建对话" }).click();
  const request = await start;
  const receipt = await (await request.response()).json();
  const reused = receipt.id;
  const path = "projects/" + reused;
  const reuseHeaders = { "Idempotency-Key": request.headers()["idempotency-key"] };
  assert.deepEqual(await post(member, resourcePath + "/projects", { version: 1, goal }, 201, reuseHeaders), receipt);
  await post(member, resourcePath + "/projects", { version: 1, goal: "其他任务" }, 409, reuseHeaders);
  await member.waitForURL("**/#" + reused);
  const empty = await api(member, path);
  assert.equal(empty.workflowTemplate.version, 1);
  assert.equal(empty.owner, nextReviewer);
  assert.equal(empty.reviewer, nextReviewer);
  assert.equal(empty.plan, null);
  assert.equal(empty.run, null);
  assert.equal(empty.costSummary.calls, 0);
  assert.deepEqual(empty.participants.map(item => item.id), [nextReviewer]);
  assert.deepEqual(await api(member, path + "/sources"), []);
  assert.deepEqual(await api(member, path + "/agent-grants"), []);
  assert.equal((await member.request.get(`/api/${originalPath}/sources/${initial.id}/content?version=1`)).status(), 404);

  const { id: upgradeProject } = await post(member, resourcePath + "/projects", { goal: "本项目六月入账核对", version: 1 }, 201, key());
  const upgradePath = "projects/" + upgradeProject;
  const sameProjectSource = await post(member, upgradePath + "/sources?name=" + encodeURIComponent("六月资料.csv"),
    Buffer.from("region,amount\nEast,41.15"), 201, { "Content-Type": "application/octet-stream" });
  const pref = await post(member, "preferences", { body: "归档资料按月份标识", enabled: true });
  const retained = [{ id: pref.id, revision: 1 }];
  const sameProjectPins = [{ id: sameProjectSource.id, version: 1 }];
  await post(member, upgradePath + "/plan", {
    ...shape, agents: [writer.id, replacement.id, docx.id], brief: "本项目六月入账核对，保留已选资料",
    expected: (await api(member, upgradePath)).revision, sources: sameProjectPins, preferences: retained,
  }, 204);
  const beforeUpgrade = await api(member, upgradePath);
  assert.equal(beforeUpgrade.workflowTemplate.version, 1);
  assert.equal(beforeUpgrade.planVersion, 1);
  assert.equal(beforeUpgrade.workflowTemplate.updateVersion, null);

  const { id: racingProject } = await post(member, resourcePath + "/projects", { goal: "测试执行与采用的并发确认", version: 1 }, 201, key());
  const racingPath = "projects/" + racingProject;
  const racingSource = await post(member, racingPath + "/sources?name=race.csv", Buffer.from("region,amount\nEast,51.10"),
    201, { "Content-Type": "application/octet-stream" });
  await post(member, racingPath + "/plan", {
    ...shape, agents: [writer.id, replacement.id, docx.id], brief: "本次独立核对", expected: (await api(member, racingPath)).revision,
    sources: [{ id: racingSource.id, version: 1 }],
  }, 204);

  // Publish a genuinely different two-step workflow while both v1 drafts exist.
  const { id: reference } = await post(creator, "projects", { team, goal: "备案新一期两步协作" });
  const referencePath = "projects/" + reference;
  const referenceSource = await post(creator, referencePath + "/sources?name=reference.csv",
    Buffer.from("region,amount\nWest,22.80"), 201, { "Content-Type": "application/octet-stream" });
  const newShape = { agents: [replacement.id, docx.id], agentRevisions: [1, 1], dependencies: [[], [1]] };
  await post(creator, referencePath + "/plan", {
    ...newShape, brief: "只制作实收工作簿和 Word", expected: (await api(creator, referencePath)).revision,
    sources: [{ id: referenceSource.id, version: 1 }],
  }, 204);
  await creator.goto("/#" + reference);
  const reviewedReference = await execute(creator, reference, "v2-reference", 2);
  const savedV2 = await post(creator, referencePath + "/workflow-templates", {
    ...fields, instructions: "新版模板的明确要求。", outcomes: "实收工作簿及 Word，逐项审核。",
    expected: reviewedReference.revision, replace: draft.id, expectedVersion: 1,
  }, 201, key());
  assert.equal(savedV2.version, 2);
  assert.equal((await api(member, "workflow-templates"))[0].version, 1);
  const updatePath = upgradePath + "/workflow-template-update";
  await api(member, updatePath, { status: 404 });
  await post(creator, resourcePath + "/publication", { action: "request", version: 2 }, 204);
  await post(admin, resourcePath + "/publication", { action: "publish", version: 2 }, 204);
  await post(member, resourcePath + "/projects", { version: 1, goal: "另一个旧版请求" }, 409, key());
  assert.equal((await api(member, "workflow-templates"))[0].version, 2);
  await member.reload();
  await member.getByText(/采用协作模板/).waitFor();
  assert.equal((await api(member, path)).workflowTemplate.version, 1);
  assert.ok(!(await member.getByLabel("交付目标与确认要求").inputValue()).includes("新版模板"));
  assert.equal(await member.getByRole("button", { name: "保存待确认方案" }).isEnabled(), false);
  await member.getByLabel("步骤 2 执行 Agent").selectOption(replacement.id + "/1");
  assert.equal(await member.getByRole("button", { name: "保存待确认方案" }).isEnabled(), false);
  await member.locator('.composer input[type="file"]').setInputFiles({
    name: "五月实收.csv", mimeType: "text/csv", buffer: Buffer.from("region,amount\nEast,35.20"),
  });
  await member.getByRole("button", { name: "发送消息" }).click();
  await member.getByLabel("引用资料 · 五月实收.csv").selectOption("1");
  const replacementSnapshot = await api(member, path);
  const planInput = { ...shape, agents: [writer.id, replacement.id, docx.id], brief: goal, expected: replacementSnapshot.revision };
  await post(member, path + "/plan", { ...planInput, sources: [{ id: initial.id, version: 1 }] }, 409);
  assert.ok(await member.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  await member.getByRole("button", { name: "保存待确认方案" }).click();
  await member.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const reviewPlan = await api(member, path);
  assert.deepEqual(reviewPlan.plan.nodes.map(step => step.dependsOn), shape.dependencies);
  assert.equal(reviewPlan.plan.nodes[1].agent, replacement.id);
  assert.equal(reviewPlan.plan.sources.length, 1);
  assert.notEqual(reviewPlan.plan.sources[0].id, initial.id);
  assert.deepEqual(reviewPlan.plan.preferences, []);
  assert.ok(!JSON.stringify(reviewPlan).includes(secret));
  assert.equal(reviewPlan.run, null);
  const execution = await execute(member, reused, "reused");
  assert.notEqual(execution.run.id, sealed.run.id);
  assert.equal(execution.workflowTemplate.version, 1);
  const old = await api(creator, originalPath);
  assert.equal(old.deliveries[0].id, sealed.deliveries[0].id);
  assert.equal(old.run.id, sealed.run.id);
  assert.equal(old.plan.sources[0].id, initial.id);

  const preview = await api(member, updatePath);
  assert.equal(preview.target.version, 2);
  assert.equal(preview.canAdopt, true);
  assert.deepEqual(preview.changedSteps, [1, 2, 3]);
  assert.deepEqual(preview.target.nodes.map(step => step.dependsOn), newShape.dependencies);
  await api(outsider, updatePath, { status: 404 });
  const adopt = (plan, expected, headers = key()) => post(member, updatePath, { fromVersion: 1, toVersion: 2, plan }, expected, headers);
  const newPlan = { ...newShape, brief: "本项目六月入账核对", expected: preview.projectRevision, sources: sameProjectPins, preferences: retained };
  await adopt({ ...newPlan, sources: [{ id: initial.id, version: 1 }] }, 409);
  assert.equal((await api(member, upgradePath)).planVersion, 1);
  assert.equal((await api(member, upgradePath)).workflowTemplate.version, 1);
  await post(member, upgradePath + "/messages", { body: "业务补充，按实际入账金额。" }, 204);
  await adopt(newPlan, 409);
  await member.goto("/#" + upgradeProject);
  await member.getByLabel(/授权本次 3/).check();
  await member.getByRole("button", { name: "查看模板新版 v2" }).click();
  const compare = member.getByRole("region", { name: "模板版本对比" });
  await compare.getByText("能力调用上限 · 当前 3 次，新版 2 次。保存时重新核对能力，执行前查看费用。", { exact: true }).waitFor();
  await compare.getByText(/实收工作簿及 Word/).waitFor();
  await compare.screenshot({ path: join(evidence, "workflow-template-update-mobile.png") });
  await compare.getByRole("button", { name: "按新版编辑方案" }).click();
  assert.equal(await member.getByLabel("引用资料 · 六月资料.csv").inputValue(), "1");
  assert.equal(await member.getByLabel("个人偏好 · 归档资料按月份标识").inputValue(), "1");
  const revisedBrief = await member.getByLabel("交付目标与确认要求").inputValue();
  assert.match(revisedBrief, /新版模板的明确要求/);
  assert.match(revisedBrief, /业务补充，按实际入账金额/);
  assert.equal((await api(member, upgradePath)).planVersion, 1);
  assert.equal((await api(member, upgradePath)).costSummary.calls, 0);
  const submitUpdate = member.waitForRequest(r => r.method() === "POST" && r.url().endsWith(updatePath));
  await member.getByRole("button", { name: "采用新版并保存待确认方案" }).click();
  const updateRequest = await submitUpdate;
  const updateResponse = await updateRequest.response();
  assert.equal(updateResponse.status(), 200, await updateResponse.text());
  const acceptedUpdate = await updateResponse.json();
  const updateKey = { "Idempotency-Key": updateRequest.headers()["idempotency-key"] };
  assert.deepEqual(await post(member, updatePath, updateRequest.postDataJSON(), 200, updateKey), acceptedUpdate);
  await adopt({ ...newPlan, brief: "不同的模板变更" }, 409, updateKey);
  await member.getByRole("heading", { name: "协作方案 v2", exact: true }).waitFor();
  assert.equal(await member.getByLabel(/授权本次 2/).isChecked(), false);
  const adopted = await api(member, upgradePath);
  assert.equal(adopted.id, upgradeProject);
  assert.equal(adopted.workflowTemplate.version, 2);
  assert.equal(adopted.planVersion, 2);
  assert.deepEqual(adopted.plan.sources, sameProjectPins);
  assert.deepEqual(adopted.plan.preferences, retained);
  assert.deepEqual(adopted.plan.nodes.map(node => node.dependsOn), newShape.dependencies);
  assert.equal(adopted.run, null);
  assert.equal(adopted.costSummary.calls, 0);
  assert.equal(adopted.reviewer, beforeUpgrade.reviewer);
  const upgradedExecution = await execute(member, upgradeProject, "adopted", 2);

  const oldUpdatePath = path + "/workflow-template-update";
  const acceptedPreview = await api(member, oldUpdatePath);
  assert.equal(acceptedPreview.canAdopt, false);
  await post(member, oldUpdatePath, { fromVersion: 1, toVersion: 2, plan: { ...newPlan, expected: execution.revision } }, 409, key());
  await member.goto("/#" + reused);
  await member.getByRole("button", { name: "查看模板新版 v2" }).click();
  await member.getByRole("button", { name: "用新版开启新对话" }).click();
  const destination = member.getByRole("dialog", { name: "协作模板", exact: true });
  await destination.getByRole("heading", { name: name, exact: true }).waitFor();
  await destination.getByLabel("本次任务目标").fill("按新版开启七月材料核对");
  await destination.getByRole("button", { name: "使用模板创建对话" }).click();
  await member.waitForURL(url => url.hash !== "#" + reused);
  const followup = await api(member, "projects/" + new URL(member.url()).hash.slice(1));
  assert.equal(followup.workflowTemplate.version, 2);
  assert.equal(followup.plan, null);
  assert.equal(followup.run, null);
  const retainedDelivery = await api(member, path);
  assert.equal(retainedDelivery.workflowTemplate.version, 1);
  assert.equal(retainedDelivery.run.id, execution.run.id);
  assert.equal(retainedDelivery.deliveries[0].id, execution.deliveries[0].id);

  const racingPlan = await api(member, racingPath);
  const [updated, launched] = await Promise.all([
    member.request.post(`/api/${racingPath}/workflow-template-update`, { headers: key(), data: { fromVersion: 1, toVersion: 2, plan: {
      ...newShape, brief: "按两步核对", expected: racingPlan.revision, sources: [{ id: racingSource.id, version: 1 }],
    } } }),
    member.request.post(`/api/${racingPath}/runs`, { headers: key(), data: { expected: racingPlan.revision, authorizeCalls: 3 } }),
  ]);
  const raced = await api(member, racingPath);
  if (updated.status() === 200) {
    assert.equal(launched.status(), 409, await launched.text());
    assert.equal(raced.planVersion, 2);
    assert.equal(raced.workflowTemplate.version, 2);
    assert.equal(raced.run, null);
    assert.equal(raced.costSummary.calls, 0);
  } else {
    assert.equal(updated.status(), 409, await updated.text());
    assert.equal(launched.status(), 202, await launched.text());
    assert.equal(raced.planVersion, 1);
    assert.equal(raced.workflowTemplate.version, 1);
    assert.equal(raced.run.id, (await launched.json()).id);
    await member.goto("/#" + racingProject);
    await member.getByRole("button", { name: "审核此版本", exact: true }).first().waitFor();
    assert.equal((await api(member, racingPath)).costSummary.calls, 3);
  }
  await writeFile(join(evidence, "workflow-template-update-race.json"), JSON.stringify({
    project: racingProject, updateStatus: updated.status(), launchStatus: launched.status(),
    planVersion: raced.planVersion, templateVersion: raced.workflowTemplate.version,
    acceptedRun: raced.run?.id ?? null,
  }, null, 2) + "\n");

  await writeFile(join(evidence, "workflow-template-update.json"), JSON.stringify({
    project: upgradeProject, oldPlan: 1, newPlan: adopted.planVersion, fromVersion: 1, toVersion: 2,
    changedSteps: preview.changedSteps, sourceRetained: sameProjectSource.id, preferredRevisionRetained: true,
    changedFromCalls: 3, finalCalls: upgradedExecution.costSummary.calls, freshExecutionConsent: true,
    transactionRollbackOnInvalidSource: true, staleProjectRejected: true, replaySameReceipt: true,
    pinnedDelivery: retainedDelivery.deliveries[0].id, newerFollowup: followup.id,
  }, null, 2) + "\n");
  assert.equal((await post(creator, originalPath + "/workflow-templates", {
    ...fields, instructions: "仍待共享的草稿。", expected: sealed.revision, replace: draft.id, expectedVersion: 2,
  }, 201, key())).version, 3);
  await post(admin, resourcePath + "/publication", { action: "archive", version: 2 }, 204);
  assert.deepEqual(await api(member, "workflow-templates"), []);
  assert.equal((await api(member, path)).workflowTemplate.version, 1);

  await writeFile(join(evidence, "workflow-template.json"), JSON.stringify({
    template: draft.id, versionUsed: 1, versionThenPublished: 2, original, reused,
    originalReviewer: originalOwner, newReviewer: nextReviewer, dependencies: shape.dependencies,
    originalSource: initial.id, newSource: reviewPlan.plan.sources[0].id,
    originalRun: sealed.run.id, newRun: execution.run.id, callsPerRun: 3,
    passed: { sameKeyReplay: true, previousVersionPinned: true, scopedPublication: true, mandatoryNewSources: true, replacement: true, archivedAfterDelivery: true },
  }, null, 2) + "\n");
} finally {
  await browser.close();
}

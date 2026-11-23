import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const { SMOKE_ORIGIN: baseURL, BROWSER_PASSWORD: password, VISUAL_EVIDENCE: out, QUICK_ENDPOINT: fixture } = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const manager = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  const member = await browser.newPage({ baseURL });
  const call = async (page, path, { data, headers, status = 200, method = data === undefined ? "GET" : "POST" } = {}) => {
    const response = await page.request.fetch("/api/" + path, { method, data, headers });
    const body = await response.text();
    assert.equal(response.status(), status, path + " " + body);
    return body ? JSON.parse(body) : null;
  };
  const key = () => ({ "Idempotency-Key": randomUUID() });
  const post = (page, path, data, status = 200, headers) => call(page, path, { data, status, headers });
  async function until(read, accept) {
    const deadline = Date.now() + 20000;
    while (Date.now() < deadline) {
      const value = await read();
      if (accept(value)) return value;
      await new Promise(resolve => setTimeout(resolve, 150));
    }
    throw new Error("Rollback test state did not become ready");
  }

  await post(admin, "login", { email: "review@sample.invalid", password });
  const originalReviewer = await call(admin, "me");
  async function register(page, suffix, name) {
    const { token } = await post(admin, "admin/invitations", {}, 201);
    return post(page, "register", { token, email: "rollback-" + suffix + "@sample.invalid", name, password }, 201);
  }
  const supervisor = await register(manager, "manager", "换版管理员");
  await register(member, "member", "服务使用者");
  const people = await call(admin, "admin/members");
  const roleVersion = people.find(person => person.id === supervisor.id).accountRevision;
  await call(admin, "admin/members/" + supervisor.id, {
    method: "PATCH", status: 204, data: { role: "admin", enabled: true, password, expected: roleVersion, reason: "验收服务换版权限" },
  });
  const team = (await call(manager, "teams")).find(item => item.default).id;
  const catalog = await call(admin, "agents");
  const writer = catalog.find(a => a.publishedName === "Writer");
  const checker = catalog.find(a => a.publishedName === "Checker");
  assert.ok(writer?.publishedReady && checker?.publishedReady);
  const firstInput = {
    name: "退单说明核对", agent: checker.id,
    instructions: "核对旧版退单说明，只需文本回执", example: "请核对退单单号 R-14",
  };
  const first = await post(admin, "services", firstInput, 201);
  const path = "services/" + first.id;
  const describe = "v1/" + path;
  const history = () => call(manager, path + "/versions");
  const preview = (target) => call(manager, path + "/versions/" + target + "/preflight");
  const action = (data, status, headers = key()) => post(manager, path + "/rollback", data, status, headers);

  const docx = await post(manager, "agents/tools", {
    name: "退单说明排版", description: "将核对结果排成可编辑原件", tool: { operation: "docx" },
  }, 201);
  const testWord = await post(manager, "agents/" + docx.id + "/validate", {
    revision: 1, input: "退单说明", expected: "退单说明", authorizeOneCall: true,
  });
  assert.equal(testWord.status, "passed");
  await post(manager, "agents/" + docx.id + "/publish", { revision: 1 }, 204);
  const source = await post(manager, "projects", { goal: "试用新的退单说明" }, 201);
  const sourcePath = "projects/" + source.id;
  await post(manager, sourcePath + "/plan", {
    expected: 1, brief: "写好说明后排版", agents: [writer.id, docx.id],
    agentRevisions: [writer.published, 1], dependencies: [[], [1]],
  }, 204);
  await post(manager, sourcePath + "/runs", {
    expected: (await call(manager, sourcePath)).revision, authorizeCalls: 2,
  }, 202, key());
  await until(() => call(manager, sourcePath), result => result.stage === "review");
  const secondInput = {
    name: "退单说明成稿", instructions: "核对退单说明并制作 Word 原件", project: source.id,
  };
  const second = await post(manager, "services", {
    ...secondInput, id: first.id, expected: first.version, expectedRoutingRevision: first.routingRevision,
  }, 201);
  assert.equal(second.version, 2);
  assert.equal(second.routingRevision, first.routingRevision + 1);

  await call(member, path + "/versions", { status: 403 });
  await call(member, path + "/versions/1/preflight", { status: 403 });
  const rollbackInput = {
    expected: 2, expectedRoutingRevision: second.routingRevision, target: 1, reason: "临时恢复已核对的文本回执流程",
  };
  await post(member, path + "/rollback", rollbackInput, 403, key());
  const setMembership = (joined) => call(manager, "admin/teams/" + team + "/members/" + originalReviewer.id, {
    method: "PUT", status: 204, data: { joined, reason: "核验旧版确认人的有效授权" },
  });
  await setMembership(false);
  await call(admin, path + "/versions", { status: 404 });
  const missingReviewer = await preview(1);
  assert.ok(missingReviewer.blockers.some(item => item.code === "reviewer_unavailable"));
  await action(rollbackInput, 409);
  await setMembership(true);
  assert.deepEqual((await preview(1)).blockers, []);

  const app = await post(manager, path + "/applications", { name: "退单签名调用", days: 1 }, 201);
  const bearer = { Authorization: "Bearer " + app.token };
  const signed = (version, input, calls, idempotency) => post(manager, describe + "/runs", {
    version, input, authorizeCalls: calls,
  }, 202, { ...bearer, ...idempotency });
  const progress = (run) => call(manager, "v1/runs/" + run, { headers: bearer });
  assert.equal((await call(manager, describe, { headers: bearer })).version, 2);

  await member.goto("/?service=" + first.id);
  await member.getByRole("heading", { name: secondInput.name, exact: true }).waitFor();
  await member.getByLabel("向服务提出任务").fill("保留已输入的退单需求");
  await member.getByRole("checkbox", { name: /2 次调用/ }).check();
  await manager.goto("/");
  await manager.getByRole("button", { name: "我的工作区" }).click();
  await manager.getByRole("button", { name: "服务与渠道" }).click();
  await manager.getByRole("button", { name: new RegExp(secondInput.name + " · v2") }).click();
  await manager.getByText("版本与回退", { exact: true }).click();
  const changeAgent = (action) => post(manager, "admin/catalog/agents/" + checker.id,
    { action, reason: "核对历史依赖的停用和恢复" }, 204);
  await changeAgent("disable");
  await manager.getByLabel("回退目标版本").selectOption("1");
  await manager.getByRole("list", { name: "回退阻塞原因" }).getByText(/步骤 1 的 Agent 当前已停用/).waitFor();
  assert.ok(await manager.getByRole("button", { name: "确认回退至 v1" }).isDisabled());
  await action(rollbackInput, 409);
  assert.equal((await history()).currentVersion, 2);
  await changeAgent("enable");
  const [rechecked] = await Promise.all([
    manager.waitForResponse(response => response.url().endsWith("/api/" + path + "/versions/1/preflight")),
    manager.getByRole("button", { name: "刷新版本" }).click(),
  ]);
  assert.deepEqual((await rechecked.json()).blockers, []);
  const oldView = manager.getByRole("region", { name: "回退目标版本", exact: true });
  const newView = manager.getByRole("region", { name: "当前默认版本", exact: true });
  await oldView.getByText("最多 1 次调用", { exact: false }).waitFor();
  await newView.getByText(/DOCX 文档/).waitFor();
  await manager.getByRole("list", { name: "回退阻塞原因" }).waitFor({ state: "detached" });

  const originalKey = key();
  const held = await signed(2, "SERVICE_ROLLBACK_HOLD_937", 2, originalKey);
  await until(async () => manager.request.get(fixture.replace(/\/v1$/, "") + "/fixture/service-rollback-pending",
    { ignoreHTTPSErrors: true }), response => response.status() === 204);
  const during = await progress(held.id);
  assert.equal(during.version, 2);
  assert.notEqual(during.stage, "review");

  await manager.getByLabel("回退原因").fill(rollbackInput.reason);
  await oldView.evaluate(node => node.scrollIntoView({ block: "nearest" }));
  await manager.screenshot({ path: join(out, "service-rollback-mobile.png") });
  const [submitted] = await Promise.all([
    manager.waitForResponse(response => response.url().endsWith("/api/" + path + "/rollback")),
    manager.getByRole("button", { name: "确认回退至 v1" }).click(),
  ]);
  assert.equal(submitted.status(), 200, await submitted.text());
  const receipt = await submitted.json();
  const acceptedKey = { "Idempotency-Key": submitted.request().headers()["idempotency-key"] };
  assert.equal(receipt.version, 1);
  assert.equal(receipt.routingRevision, second.routingRevision + 1);
  await manager.getByText("当前默认 v1", { exact: true }).waitFor();
  assert.equal((await progress(held.id)).version, 2);
  assert.deepEqual(await signed(2, "SERVICE_ROLLBACK_HOLD_937", 2, originalKey), held);
  const released = await manager.request.get(fixture.replace(/\/v1$/, "") + "/fixture/service-rollback-release",
    { ignoreHTTPSErrors: true });
  assert.equal(released.status(), 204);
  const discovery = await call(manager, describe, { headers: bearer });
  assert.equal(discovery.name, firstInput.name);
  assert.equal(discovery.version, 1);
  assert.equal(discovery.authorizeCalls, 1);

  await member.getByRole("button", { name: "确认并启动对话" }).click();
  await member.getByText(/服务已换版/).waitFor();
  await member.getByRole("heading", { name: firstInput.name, exact: true }).waitFor();
  const consent = member.getByRole("checkbox", { name: /1 次调用/ });
  await consent.waitFor();
  assert.ok(!(await consent.isChecked()));
  assert.equal(await member.getByLabel("向服务提出任务").inputValue(), "保留已输入的退单需求");
  await consent.check();
  await member.getByRole("button", { name: "确认并启动对话" }).click();
  await member.waitForURL(/\/#.+/);
  const latest = await call(member, path + "/entry");
  assert.equal(latest.recent[0].version, 1);

  const legacy = await signed(2, "按已知二版合同发起", 2, key());
  assert.equal((await progress(legacy.id)).version, 2);
  const pending = await until(() => call(manager, "projects/" + held.project), result => result.stage === "review");
  assert.equal(pending.costSummary.calls, 2);
  for (const item of pending.artifacts) await post(manager,
    "projects/" + held.project + "/artifacts/" + item.id + "/review", { version: item.version }, 204);
  await post(manager, "projects/" + held.project + "/deliver",
    { expected: (await call(manager, "projects/" + held.project)).revision }, 201);
  const archive = await manager.request.get("/api/v1/runs/" + held.id + "/delivery/package", { headers: bearer });
  assert.equal(archive.status(), 200);
  const deliveredBytes = await archive.body();
  await writeFile(join(out, "service-rollback-held-delivery.zip"), deliveredBytes);

  const restoredSecond = await action({
    expected: 1, expectedRoutingRevision: receipt.routingRevision, target: 2, reason: "复核后恢复 Word 交付",
  }, 200);
  await action(rollbackInput, 409);
  const routedFirst = await action({ ...rollbackInput, expectedRoutingRevision: restoredSecond.routingRevision }, 200);
  const third = await post(manager, "services", {
    ...firstInput, name: "退单说明第三版", id: first.id, expected: 1, expectedRoutingRevision: routedFirst.routingRevision,
  }, 201);
  assert.equal(third.version, 3);
  assert.deepEqual((await history()).versions.map(item => item.version), [3, 2, 1]);
  assert.deepEqual(await action(rollbackInput, 200, acceptedKey), receipt);
  await action({ ...rollbackInput, reason: "更改理由不得复用旧请求键" }, 409, acceptedKey);
  assert.equal((await call(manager, describe, { headers: bearer })).version, 3);
  const priorDelivery = await manager.request.get("/api/v1/runs/" + held.id + "/delivery/package", { headers: bearer });
  assert.equal(priorDelivery.status(), 200);
  assert.ok((await priorDelivery.body()).equals(deliveredBytes), "The previously approved ZIP changed");

  const other = await post(manager, "admin/teams", { name: "旧服务支持组" }, 201);
  const model = await post(manager, "resources", {
    team: other.id, name: "支持组模型", model: "fixture", endpoint: fixture, key: "local-fixture",
  }, 201);
  const borrowed = await post(manager, "agents", {
    name: "支持组核对助手", instructions: "输出核对回执", resource: model.id, resourceRevision: 1,
  }, 201);
  assert.equal((await post(manager, "agents/" + borrowed.id + "/validate", {
    revision: 1, input: "verify", expected: "RECEIPT", authorizeOneCall: true,
  })).status, "passed");
  await post(manager, "agents/" + borrowed.id + "/publish", { revision: 1 }, 204);
  const scopedProject = await post(manager, "projects", { team, goal: "校验支持组的独立授权" }, 201);
  const scopedPath = "projects/" + scopedProject.id;
  await post(manager, scopedPath + "/plan", {
    expected: 1, brief: "由已获权的支持组能力核对", agents: [borrowed.id], agentRevisions: [1],
  }, 204);
  await post(manager, scopedPath + "/runs", {
    expected: (await call(manager, scopedPath)).revision, authorizeCalls: 1,
  }, 202, key());
  const scopedV1 = await post(manager, "services", {
    name: "有历史授权范围的服务", project: scopedProject.id, instructions: "由支持组核对",
  }, 201);
  const scoped = "services/" + scopedV1.id;
  const scopedV2 = await post(manager, "services", {
    id: scopedV1.id, expected: 1, name: "团队内核对", agent: checker.id, instructions: "按本团队资料核对",
  }, 201);
  await post(manager, scoped + "/applications", { name: "团队内调用应用", days: 1 }, 201);
  const scopePreview = await call(manager, scoped + "/versions/1/preflight");
  assert.deepEqual(scopePreview.blockers.map(item => item.code), ["audience_scope"]);
  await post(manager, scoped + "/rollback", {
    expected: 2, expectedRoutingRevision: scopedV2.routingRevision, target: 1, reason: "校验原授权范围",
  }, 409, key());
  assert.equal((await call(member, scoped + "/entry")).version, 2);

  await writeFile(join(out, "service-rollback.json"), JSON.stringify({
    service: first.id, first, second, receipt, third, acceptedV2: held.id, webNewVersion: latest.recent[0].version,
    activeV2DuringRollback: during.stage, oldReplay: true, explicitV2AfterRollback: legacy.id,
    reviewerUnavailableBlocked: true, inactiveDependencyBlocked: true, staleAdminGenerationBlocked: true,
    staleWebConsentCleared: true, externalPackageCalls: pending.costSummary.calls, narrowedAudienceBlocked: true,
    priorDeliveryBytesPreserved: true,
  }, null, 2) + "\n");
} finally {
  await browser.close();
}

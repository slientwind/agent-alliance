import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: evidence,
  COLUMN_FIXTURE_ENDPOINT: endpoint,
} = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  const member = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  async function call(page, path, { method = "GET", data, headers, status } = {}) {
    const response = await page.request.fetch("/api/" + path, { method, data, headers });
    const body = await response.text();
    if (status !== undefined) assert.equal(response.status(), status, body);
    else assert.ok(response.ok(), path + ": " + body);
    return body ? JSON.parse(body) : undefined;
  }
  const post = (page, path, data, options) => call(page, path, { method: "POST", data, ...options });
  await post(admin, "login", { email: "review@sample.invalid", password });
  const { id: team } = await post(admin, "admin/teams", { name: "已发布表头服务验证" });
  const model = await post(admin, "resources", {
    team, name: "服务字段模型", model: "column-suggestion-fixture", endpoint,
    key: "synthetic-columns", estimate: "0.03",
  });
  await call(admin, `teams/${team}/setup/planner`, { method: "PUT", data: {
    resource: model.id, revision: 1, expected: 0,
  } });
  const invite = await post(admin, "admin/invitations", {});
  await post(member, "register", {
    email: "service-columns@sample.invalid", name: "报表调用成员", token: invite.token, password,
  });
  const person = await call(member, "me");
  await call(admin, `admin/teams/${team}/members/${person.id}`, {
    method: "PUT", data: { joined: true, reason: "验证本团队授权报表服务" },
  });
  const agent = await post(admin, "agents/tools", {
    team, name: "发布的报表助手", description: "按地区汇总已核准行",
    tool: { operation: "aggregate", config: {
      group: "region", amount: "amount", filterColumn: "status", filterValue: "ok",
    } },
  });
  await post(admin, `agents/${agent.id}/validate`, {
    revision: 1, input: "region,amount,status\nEast,7.00,ok", expected: "7.00", authorizeOneCall: true,
  });
  await post(admin, `agents/${agent.id}/publish`, { revision: 1 });
  const serviceName = "跨渠道表头核对";
  const service = await post(admin, "services", { name: serviceName, agent: agent.id, instructions: "汇总已核准金额" });
  const app = await post(admin, `services/${service.id}/applications`, { name: "报表应用", days: 1 });
  const delegation = await post(member, `services/${service.id}/delegate`, { app: app.id, hours: 1 });
  const otherDelegation = await post(member, `services/${service.id}/delegate`, { app: app.id, hours: 1 });
  const applicationHeaders = { Authorization: "Bearer " + app.token };
  const delegateHeaders = { ...applicationHeaders, "X-Alliance-Delegation": delegation.token };
  const alternativeHeaders = { ...applicationHeaders, "X-Alliance-Delegation": otherDelegation.token };
  const serviceRoute = `v1/services/${service.id}/runs`;
  const csv = (name, text) => ({ name, data: Buffer.from(text).toString("base64") });
  const east = ["敏感东区.csv", "region,Gross sales,Net settled amount,status\nPRIVATE-ROW-367,100,30.10,ok"];
  const west = ["西区.csv", "region,标价合计,折后实收金额,status\nWest,80,60.20,ok"];
  const original = {
    version: 1, input: "内部工作目标", authorizeCalls: 1, attachments: [csv(...east), csv(...west)],
  };
  async function submit(headers, data, key = randomUUID(), status = 202) {
    return post(member, serviceRoute, data, { headers: { ...headers, "Idempotency-Key": key }, status });
  }
  const specification = await call(member, "v1/openapi.json");
  assert.ok(specification.paths["/runs/{id}/calls/{step}/column-suggestion"]);
  assert.equal(specification.components.schemas.RunRequest.properties.attachments.maxItems, 20);
  await submit(delegateHeaders, { ...original, attachment: original.attachments[0] }, randomUUID(), 422);
  await submit(delegateHeaders, { ...original, attachments: [
    csv("large-1.csv", "a".repeat(50 * 1024)), csv("large-2.csv", "b".repeat(50 * 1024)),
  ] }, randomUUID(), 422);

  async function status(run, headers = delegateHeaders) {
    return call(member, `v1/runs/${run}`, { headers });
  }
  async function waitStatus(run, headers, stageOrDecision) {
    for (let attempt = 0; attempt < 110; attempt += 1) {
      const progress = await status(run, headers);
      if ([progress.stage, progress.nextAction].includes(stageOrDecision)) return progress;
      await new Promise(resolve => setTimeout(resolve, 180));
    }
    assert.fail(`waiting for ${stageOrDecision}: ${JSON.stringify(await status(run, headers))}`);
  }
  async function approveDelivery(project, run, headers, label) {
    await admin.goto("/#" + project);
    await admin.getByRole("button", { name: "审核此版本" }).waitFor();
    await admin.getByRole("button", { name: "审核此版本" }).click();
    await admin.getByRole("button", { name: "确认并固定以上版本" }).click();
    await waitStatus(run, headers, "delivered");
    const response = await member.request.get(`/api/v1/runs/${run}/delivery/package`, { headers });
    assert.ok(response.ok(), await response.text());
    await writeFile(join(evidence, `service-columns-${label}.zip`), await response.body());
  }
  const firstKey = randomUUID();
  const delegated = await submit(delegateHeaders, original, firstKey);
  assert.deepEqual(await submit(delegateHeaders, original, firstKey), delegated);
  await submit(delegateHeaders, { ...original, attachments: [...original.attachments].reverse() }, firstKey, 409);
  await call(member, `v1/runs/${delegated.id}`, { headers: applicationHeaders, status: 404 });
  await call(member, `v1/runs/${delegated.id}`, { headers: alternativeHeaders, status: 404 });
  const pending = await waitStatus(delegated.id, delegateHeaders, "correct_columns");
  const [correction] = pending.columnCorrections;
  assert.equal(correction.attempt, 1);
  assert.equal(correction.files.length, 2);
  assert.ok(correction.suggestionUrl);
  const adviceRequest = { attempt: correction.attempt, expected: pending.controlRevision, authorizeOneCall: true };
  const adviceRoute = correction.suggestionUrl.replace("/api/", "");
  const advised = await call(member, adviceRoute, {
    method: "POST", headers: delegateHeaders, data: adviceRequest, status: 200,
  });
  assert.equal(advised.status, "ready");
  assert.equal(advised.entries.length, 2);
  assert.deepEqual(await post(member, adviceRoute, adviceRequest, { headers: delegateHeaders }), advised);
  assert.deepEqual((await status(delegated.id)).columnCorrections[0].suggestion, advised);
  const mappings = advised.entries.map(({ file, field, column }) => {
    const issue = correction.files[file - 1];
    return { source: issue.source, version: issue.version, columns: { [issue.missing[field - 1]]: issue.choices[column - 1] } };
  });
  const decision = {
    action: "correct_columns", expected: pending.controlRevision, step: correction.step, attempt: correction.attempt,
    reason: "已核对两份固定版本的实收金额列", authorizeOneCall: true, inputMappings: mappings,
  };
  const actions = `v1/runs/${delegated.id}/actions`;
  const apply = (value, expected, key = randomUUID(), headers = delegateHeaders) =>
    post(member, actions, value, { headers: { ...headers, "Idempotency-Key": key }, status: expected });
  await apply({ ...decision, inputMappings: [mappings[0]] }, 409);
  await apply({ ...decision, authorizeOneCall: false }, 422);
  await apply({ ...decision, attempt: 2 }, 409);
  await apply(decision, 404, randomUUID(), alternativeHeaders);
  const approvedKey = randomUUID();
  const receipt = await apply(decision, 200, approvedKey);
  assert.equal(receipt.controlRevision, pending.controlRevision + 1);
  assert.deepEqual(await apply({ ...decision, inputMappings: [...mappings].reverse() }, 200, approvedKey), receipt);
  await apply({ ...decision, reason: "其他确认" }, 409, approvedKey);
  const beforeDelivery = await waitStatus(delegated.id, delegateHeaders, "review");
  assert.equal(beforeDelivery.id, delegated.id);
  assert.deepEqual(beforeDelivery.columnCorrections, []);
  const events = await call(member, `v1/runs/${delegated.id}/events`, { headers: delegateHeaders });
  assert.equal(events.events.filter(event => event.state === "columns_confirmed").length, 1);
  await call(member, `v1/runs/${delegated.id}/delivery/package`, { headers: delegateHeaders, status: 409 });
  await approveDelivery(delegated.project, delegated.id, delegateHeaders, "delegated");

  const autonomous = await submit(applicationHeaders, original);
  const waiting = await waitStatus(autonomous.id, applicationHeaders, "review_columns");
  assert.deepEqual(waiting.columnCorrections, []);
  await post(member, `v1/runs/${autonomous.id}/actions`, {
    ...decision, expected: waiting.controlRevision,
  }, { headers: { ...applicationHeaders, "Idempotency-Key": randomUUID() }, status: 403 });
  await post(member, `v1/runs/${autonomous.id}/calls/1/column-suggestion`, {
    ...adviceRequest, expected: waiting.controlRevision,
  }, { headers: applicationHeaders, status: 403 });
  await post(admin, `runs/${autonomous.id}/calls/1/review`, {
    decision: "redo", expected: waiting.controlRevision, attempt: 1,
    evidence: "仅有字段补正职责", authorizeOneCall: true,
  }, { headers: { "Idempotency-Key": randomUUID() }, status: 404 });
  await admin.goto(waiting.humanActionUrl);
  await admin.getByRole("button", { name: "授权一次模型调用并推荐字段" }).click();
  const fill = admin.getByRole("button", { name: "将建议填入空白字段" });
  await fill.click();
  await admin.getByLabel(/我已核对字段含义/).check();
  await fill.locator("xpath=ancestor::form").screenshot({ path: join(evidence, "service-columns-reviewer.png") });
  await admin.getByRole("button", { name: "按对应字段重新计算" }).click();
  await waitStatus(autonomous.id, applicationHeaders, "review");
  await approveDelivery(autonomous.project, autonomous.id, applicationHeaders, "reviewer");

  await member.goto("/?service=" + service.id);
  await member.getByRole("heading", { name: serviceName }).waitFor();
  await member.getByLabel("向服务提出任务").fill("核对两份原始报表");
  await member.getByLabel("附资料").setInputFiles(
    [east, west].map(([name, text]) => ({ name, mimeType: "text/csv", buffer: Buffer.from(text) })),
  );
  await member.getByRole("button", { name: "移除资料 西区.csv" }).waitFor();
  assert.ok(await member.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  await member.getByRole("checkbox", { name: /授权本次最多/ }).check();
  await member.getByRole("button", { name: "确认并启动对话" }).click();
  await member.getByRole("button", { name: "按对应字段重新计算" }).waitFor();
  const webProject = (await new URL(member.url())).hash.slice(1);
  const webSources = await call(member, `projects/${webProject}/sources`);
  assert.deepEqual(webSources.map(source => source.name).sort(), [east[0], west[0]].sort());

  const revoked = await submit(delegateHeaders, {
    ...original, attachments: [csv("待撤销.csv", "region,revoked_net,status\nEast,5.00,ok")],
  });
  const revokedState = await waitStatus(revoked.id, delegateHeaders, "correct_columns");
  const submittedAdvice = member.request.post("/api/" + revokedState.columnCorrections[0].suggestionUrl.replace("/api/", ""), {
    headers: delegateHeaders, data: { ...adviceRequest, expected: revokedState.controlRevision },
  });
  await member.waitForFunction(async ({ run, headers }) => {
    const result = await fetch("/api/v1/runs/" + run, { headers });
    return (await result.json()).columnCorrections?.[0]?.suggestion?.status === "submitted";
  }, { run: revoked.id, headers: delegateHeaders });
  await call(member, "delegations/" + delegation.id, { method: "DELETE", status: 204 });
  assert.deepEqual(await (await submittedAdvice).json(), { status: "stale" });
  await call(member, `v1/runs/${revoked.id}`, { headers: delegateHeaders, status: 403 });

  const delegatedRecord = await call(member, "projects/" + delegated.project);
  const autonomousRecord = await call(admin, "projects/" + autonomous.project);
  for (const state of [delegatedRecord, autonomousRecord]) {
    assert.equal(state.costSummary.calls, 3);
    assert.match(state.artifacts[0].body, /合计 · 90\.30/);
  }
  await writeFile(join(evidence, "service-column-recovery.json"), JSON.stringify({
    delegatedRun: delegated.id, ownedByApplication: autonomous.id, webProject, withdrawnDelegation: revoked.id,
    expectedTotal: "90.30", callsPerCompletedRun: 3, confirmedEvents: 1, originalFiles: 2,
    staleDelegationResponse: "stale",
  }, null, 2) + "\n");
} finally {
  await browser.close();
}

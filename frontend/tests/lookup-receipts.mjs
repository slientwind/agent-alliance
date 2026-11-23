import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const { SMOKE_ORIGIN: baseURL, QUICK_ENDPOINT: endpoint, BROWSER_PASSWORD: password, VISUAL_EVIDENCE: out } = process.env;
const real = process.env.LOOKUP_RECEIPT_REAL === "1";
const mode = real ? "local" : "fixture";
const goal = "依据设备借用规程，设备借出前由哪个岗位核对出库单并签字？";
const other = "培训周三十四点，讲义在签到台领取。";
const quote = "设备借出前，由库房主管核对出库单并签字。";
const original = [
  "早期草案，仅用于历史对照。",
  quote,
  "PRIVATE-LOOKUP-ROW 未授权的内部编号。",
  "当前有效规程。",
  quote,
].join("\n");
const position = Array.from(original.slice(0, original.lastIndexOf(quote))).length + 1;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const member = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  async function call(page, path, method = "GET", data, expected = 200, headers) {
    const response = await page.request.fetch("/api/" + path, { method, data, headers });
    assert.equal(response.status(), expected, await response.text());
    const body = await response.text();
    return body ? JSON.parse(body) : null;
  }
  const get = (page, path, headers) => call(page, path, "GET", undefined, 200, headers);
  const post = (page, path, data, status = 201, headers) => call(page, path, "POST", data, status, headers);
  async function until(page, path, passed, headers) {
    const deadline = Date.now() + 22000;
    let state;
    do {
      state = await get(page, path, headers);
      if (passed(state)) return state;
      await new Promise(resolve => setTimeout(resolve, 140));
    } while (Date.now() < deadline);
    assert.fail(JSON.stringify(state));
  }
  const key = (name) => ({ "Idempotency-Key": "lookup-" + mode + "-" + name });
  await post(admin, "login", { email: "review@sample.invalid", password }, 200);
  const team = (await post(admin, "admin/teams", { name: "制度补件 " + mode })).id;
  const defaultTeam = (await get(admin, "teams")).find(t => t.default).id;
  const invite = await post(admin, "admin/invitations", {});
  await post(member, "register", { name: "库房经办人", email: "lookup-receipt-" + mode + "@sample.invalid", token: invite.token, password });
  const actor = (await get(member, "me")).id;
  for (const [id, joined] of [[team, true], [defaultTeam, false]]) {
    await call(admin, "admin/teams/" + id + "/members/" + actor, "PUT", { joined, reason: "规范核对权限测试" }, 204);
  }
  const resource = await post(admin, "resources", {
    name: "规程问答模型 " + mode, endpoint, model: real ? "local-course-qwen" : "lookup-receipt-fixture",
    team, key: "synthetic", estimate: "0.20",
  });
  const agent = await post(member, "agents", {
    name: "设备借用规则核对 " + mode, instructions: "收到“连接验证”时只回复 READY。正常工作时根据所给原文核对制度并准确引用。",
    resource: resource.id, resourceRevision: 1,
  });
  const trial = await post(member, "agents/" + agent.id + "/validate", {
    revision: 1, input: "连接验证", expected: "READY", authorizeOneCall: true,
  }, 200);
  assert.equal(trial.status, "passed", trial.output);
  await post(admin, "agents/" + agent.id + "/publish", { revision: 1 }, 204);

  async function createPlan(page) {
    const project = await post(page, "projects", { goal, team });
    const path = "projects/" + project.id;
    const source = await post(page, path + "/sources?name=" + encodeURIComponent("培训通知.txt"), Buffer.from(other), 201, { "Content-Type": "application/octet-stream" });
    const before = await get(page, path);
    await post(page, path + "/plan", {
      expected: before.revision, brief: goal, agents: [agent.id], agentRevisions: [1],
      sources: [{ id: source.id, version: 1 }], sourceLookup: true,
    }, 204);
    return { id: project.id, path, source };
  }

  async function confirm(page, path, filename) {
    const list = page.getByRole("button", { name: "审核此版本" });
    await list.first().click();
    await list.first().click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    const link = page.getByRole("link", { name: "下载交付包（ZIP）" });
    await link.waitFor();
    const zip = await page.request.get(await link.getAttribute("href"));
    assert.equal(zip.status(), 200);
    await writeFile(out + "/" + filename, await zip.body());
  }

  const web = await createPlan(member);
  await member.goto("/#" + web.id);
  await member.getByLabel("找不到原文时等待我补件", { exact: true }).check();
  await member.getByLabel(/授权本次 2/).check();
  await member.getByRole("button", { name: "确认并执行" }).click();
  const held = await until(member, web.path, value => value.run?.receiptRequest?.kind === "lookup");
  assert.equal(held.costSummary.calls, 1);
  assert.equal(held.run.receiptRequest.step, 2);
  assert.equal(held.artifacts.length, 1);
  const run = "runs/" + held.run.id;
  await post(member, run + "/control", { action: "resume", expected: held.run.controlRevision, reason: "确认等补件时不会绕过所需原件" }, 409, key("blocked-resume"));
  const form = member.getByRole("heading", { name: "补充引用原件" }).locator("..");
  if (!real) {
    const office = await readFile(new URL("./fixtures/receipt-pages.docx", import.meta.url));
    const word = await post(member, web.path + "/sources?name=historical.docx", office, 201);
    assert.ok(word.readable);
    await member.reload();
    await form.getByLabel("补件来源").selectOption(word.id);
    const viewed = member.waitForResponse(r => r.url().includes("receipt-passages?"));
    await form.getByRole("button", { name: "查看原句", exact: true }).click();
    const first = await (await viewed).json();
    assert.equal(first.rows.length, 20);
    assert.equal(first.rows[0].location, "段落 1");
    assert.equal(first.rows[19].location, "段落 20");
    assert.equal(first.more, true);
    const next = member.waitForResponse(r => r.url().includes("receipt-passages?"));
    await form.getByRole("button", { name: "下一页原句" }).click();
    const rest = await (await next).json();
    assert.ok(rest.rows.some(row => row.location === "段落 22"));
    const removed = await member.request.delete("/api/" + web.path + "/sources/" + word.id + "?version=1");
    assert.ok(removed.ok(), await removed.text());
    await call(member, run + "/receipt-passages?step=2&source=" + word.id + "&version=1", "GET", undefined, 404);
    await post(member, run + "/lookup-receipts", {
      expected: held.run.controlRevision, step: 2, source: word.id, version: 1,
      excerpt: first.rows[0].text, excerptStart: first.rows[0].start,
    }, 409, key("withdrawn-word"));
    await member.reload();
  }

  await form.getByLabel("补充原件").setInputFiles({ name: "现行设备借用规程.txt", buffer: Buffer.from(original), mimeType: "text/plain" });
  await form.getByRole("button", { name: "上传并查看原句" }).click();
  const target = form.getByRole("button", { name: new RegExp("当前有效规程.*" + quote.slice(0, 8)) });
  await target.click();
  const select = form.getByLabel("补件来源");
  const source = await select.inputValue();
  const receipt = { expected: held.run.controlRevision, step: 2, source, version: 1, excerpt: quote };
  await post(member, run + "/lookup-receipts", receipt, 422, key("ambiguous-source"));
  await post(member, run + "/lookup-receipts", { ...receipt, source: web.source.id, excerptStart: position }, 422, key("another-passage"));
  await post(member, run + "/lookup-receipts", { ...receipt, version: 2, excerptStart: position }, 409, key("wrong-source-version"));
  const preview = run + "/receipt-passages?step=2&source=" + source + "&version=1";
  await call(admin, preview, "GET", undefined, 404);
  await post(admin, run + "/lookup-receipts", { ...receipt, excerptStart: position }, 404, key("outside-scope"));
  assert.equal((await get(member, web.path)).costSummary.calls, 1);
  const continueAnswer = form.getByRole("button", { name: "确认原句并继续回答" });
  await continueAnswer.scrollIntoViewIfNeeded();
  await member.screenshot({ path: out + "/lookup-receipt-" + mode + ".png" });
  await continueAnswer.click();
  const ready = await until(member, web.path, value => value.stage === "review");
  assert.equal(ready.run.id, held.run.id);
  assert.equal(ready.costSummary.calls, 2);
  assert.equal(ready.planVersion, 2);
  assert.ok(ready.artifacts[1].body.includes("库房主管") && ready.artifacts[1].body.includes(quote));
  assert.ok(!ready.artifacts[1].body.includes("PRIVATE-LOOKUP-ROW"));
  const trace = await get(member, web.path + "/runs/" + ready.run.id + "/trace");
  assert.ok(trace.trace.events.some(e => e.code === "lookup_receipt_requested"));
  assert.equal(trace.trace.spans[0].attempts.length, 1);
  assert.equal(trace.trace.spans[1].attempts.length, 1);
  await call(member, preview, "GET", undefined, 404);
  const webFile = "lookup-receipt-" + mode + "-web.zip";
  await confirm(member, web.path, webFile);

  let service, browserEntry;
  if (!real) {
    const seed = await createPlan(admin);
    await post(admin, seed.path + "/runs", {
      expected: (await get(admin, seed.path)).revision, authorizeCalls: 2,
    }, 202, key("service-seed"));
    const definition = await post(admin, "services", {
      project: seed.id, name: "库房制度核对服务", instructions: "根据本次提供的原文核对设备借用制度。",
    });
    const app = await post(admin, "services/" + definition.id + "/applications", { name: "制度查询应用", days: 1 });
    const auth = { Authorization: "Bearer " + app.token };
    const launch = await post(admin, "v1/services/" + definition.id + "/runs", {
      version: 1, input: goal, authorizeCalls: 2, awaitLookupReceipt: true,
      attachment: { name: "培训通知.txt", data: Buffer.from(other).toString("base64") },
    }, 202, { ...auth, ...key("app-invoke") });
    const status = "v1/runs/" + launch.id;
    const pending = await until(admin, status, value => value.nextAction === "submit_lookup_receipt", auth);
    assert.equal(pending.receiptRequest.kind, "lookup");
    const actions = status + "/actions";
    const body = {
      action: "submit_lookup_receipt", expected: pending.controlRevision, step: pending.receiptRequest.step,
      attachment: { name: "现行设备借用规程.txt", data: Buffer.from(original).toString("base64") },
      excerpt: quote, excerptStart: position,
    };
    await post(admin, actions, { ...body, action: "submit_receipt" }, 404, { ...auth, ...key("wrong-receipt-kind") });
    const [ack, replay] = await Promise.all([
      post(admin, actions, body, 200, { ...auth, ...key("app-receipt") }),
      post(admin, actions, body, 200, { ...auth, ...key("app-receipt") }),
    ]);
    assert.deepEqual(replay, ack);
    await post(admin, actions, { ...body, excerptStart: position - 1 }, 409, { ...auth, ...key("app-receipt") });
    const complete = await until(admin, status, value => value.stage === "review", auth);
    assert.equal(complete.id, pending.id);
    await admin.goto(complete.humanActionUrl);
    const serviceFile = "lookup-receipt-fixture-api.zip";
    await confirm(admin, "projects/" + complete.project, serviceFile);
    const pack = await admin.request.get(complete.packageUrl, { headers: auth });
    assert.equal(pack.status(), 200);
    assert.ok((await pack.body()).equals(await (await admin.request.get("/api/projects/" + complete.project + "/delivery/package")).body()));
    service = { run: complete.id, idempotent: true, concurrentReplay: true, archive: serviceFile };

    await admin.goto("/?service=" + definition.id);
    await admin.getByLabel("向服务提出任务").fill(goal);
    await admin.getByLabel("附资料").setInputFiles({ name: "培训通知.txt", buffer: Buffer.from(other), mimeType: "text/plain" });
    const hold = admin.getByLabel("找不到原文时在原任务等待我补件");
    assert.equal(await hold.isChecked(), false);
    await hold.check();
    await admin.getByLabel(/授权本次最多 2 次调用/).check();
    await admin.getByRole("button", { name: "确认并启动对话" }).click();
    await admin.waitForURL("**/#*");
    const entryPath = "projects/" + new URL(admin.url()).hash.slice(1);
    const entryPending = await until(admin, entryPath, value => value.run?.receiptRequest?.kind === "lookup");
    const entryForm = admin.getByRole("heading", { name: "补充引用原件" }).locator("..");
    await entryForm.getByLabel("补充原件").setInputFiles({ name: "现行设备借用规程.txt", buffer: Buffer.from(original), mimeType: "text/plain" });
    await entryForm.getByRole("button", { name: "上传并查看原句" }).click();
    await entryForm.getByRole("button", { name: new RegExp("当前有效规程.*" + quote.slice(0, 8)) }).click();
    await entryForm.getByRole("button", { name: "确认原句并继续回答" }).click();
    const entryReady = await until(admin, entryPath, value => value.stage === "review");
    assert.equal(entryReady.run.id, entryPending.run.id);
    assert.deepEqual([entryPending.costSummary.calls, entryReady.costSummary.calls], [1, 2]);
    const entryFile = "lookup-receipt-fixture-chat.zip";
    await confirm(admin, entryPath, entryFile);
    browserEntry = { run: entryReady.run.id, archive: entryFile };

    // Hold the second invocation on budget so withdrawal happens after the
    // original is authorized, but before any content is sent to the model.
    const reviewer = (await get(admin, "me")).id;
    await call(admin, "teams/" + team + "/budget-owner", "PUT",
      { member: reviewer, previous: "", reason: "指派预算核对样例负责人" }, 204);
    const protectedPlan = await createPlan(admin);
    const budget = (expected, limit) => call(admin, protectedPlan.path + "/budget", "PUT",
      { expected, limit, warningPercent: 80, reason: "核对资料撤权后的实际派发" }, 204);
    await budget(0, "0.20");
    await post(admin, protectedPlan.path + "/runs", {
      expected: (await get(admin, protectedPlan.path)).revision, authorizeCalls: 2, awaitLookupReceipt: true,
    }, 202, key("budget-start"));
    const waiting = await until(admin, protectedPlan.path, value => value.run?.receiptRequest?.kind === "lookup");
    const supplement = await post(admin, protectedPlan.path + "/sources?name=another-original.txt",
      Buffer.from(quote), 201, { "Content-Type": "application/octet-stream" });
    const guardedRun = "runs/" + waiting.run.id;
    const guardedTrace = protectedPlan.path + "/" + guardedRun + "/trace";
    await post(admin, guardedRun + "/lookup-receipts", {
      step: 2, expected: waiting.run.controlRevision, source: supplement.id, version: 1, excerpt: quote,
    }, 202, key("budget-receipt"));
    const limited = await until(admin, protectedPlan.path, value => value.stage === "blocked" && !value.run?.receiptRequest);
    assert.equal(limited.costSummary.calls, 1);
    assert.ok((await get(admin, guardedTrace)).trace.events.some(e => e.code === "budget_denied"));
    const revoke = await admin.request.delete("/api/" + protectedPlan.path + "/sources/" + supplement.id + "?version=1");
    assert.ok(revoke.ok(), await revoke.text());
    await budget(1, "0.40");
    await post(admin, guardedRun + "/control", { action: "resume", expected: limited.run.controlRevision, reason: "重新核对预算与引用资料" }, 202, key("budget-resume"));
    const afterRevocation = await until(admin, guardedTrace, value => value.trace.events.some(e => e.code === "input_changed"));
    const withdrawn = await until(admin, protectedPlan.path, value => value.stage === "blocked");
    assert.equal(afterRevocation.trace.spans[1].attempts.length, 0);
    assert.equal(withdrawn.costSummary.calls, 1);
    assert.equal(withdrawn.artifacts.length, 1);
    await post(admin, guardedRun + "/control", { action: "cancel", expected: withdrawn.run.controlRevision, reason: "结束资料撤权样例" }, 202, key("budget-stop"));
  }
  await writeFile(out + "/lookup-receipt-" + mode + ".json", JSON.stringify({
    provider: real ? "local Qwen" : "HTTPS fixture",
    project: web.id, run: ready.run.id, chargeCalls: [held.costSummary.calls, ready.costSummary.calls],
    quote, position, archive: webFile, service, browserEntry,
    ...(real ? {} : { wordPreviewVerified: true, revokedSourceRejected: true, withdrawalBeforeSecondDispatch: true }),
  }, null, 2) + "\n");
} finally {
  await browser.close();
}

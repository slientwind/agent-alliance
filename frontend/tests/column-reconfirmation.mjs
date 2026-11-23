import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const { SMOKE_ORIGIN: baseURL, BROWSER_PASSWORD: password, VISUAL_EVIDENCE: evidence } = process.env;
const browser = await chromium.launch();
try {
  const page = () => browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  const admin = await page();
  const reviewer = await page();
  const successor = await page();
  async function call(actor, path, { method = "GET", data, headers, status } = {}) {
    const response = await actor.request.fetch("/api/" + path, { method, data, headers });
    const body = await response.text();
    if (status !== undefined) assert.equal(response.status(), status, `${method} ${path} ${body}`);
    else assert.ok(response.ok(), `${method} ${path} ${body}`);
    return body ? JSON.parse(body) : undefined;
  }
  const post = (actor, path, data, options) => call(actor, path, { method: "POST", data, ...options });
  await post(admin, "login", { email: "review@sample.invalid", password });
  const { id: adminId } = await call(admin, "me");
  const { id: team } = await post(admin, "admin/teams", { name: "汇总审核交接验收" });
  async function joinTeam(actor, email, name) {
    const invitation = await post(admin, "admin/invitations", {});
    const { id } = await post(actor, "register", { email, name, password, token: invitation.token });
    await call(admin, `admin/teams/${team}/members/${id}`, {
      method: "PUT", data: { joined: true, reason: "参与报表审核交接" },
    });
    return id;
  }
  const old = await joinTeam(reviewer, "column-former@sample.invalid", "原审核人");
  const email = "column-successor@sample.invalid";
  const replacement = await joinTeam(successor, email, "接任审核人");
  await call(admin, `admin/members/${old}`, {
    method: "PATCH", data: { role: "admin", enabled: true, password, reason: "发布交接验收服务" },
  });
  await call(admin, `teams/${team}/budget-owner`, {
    method: "PUT", data: { member: adminId, previous: "", reason: "管理交接验收预算" },
  });
  const agent = await post(admin, "agents/tools", {
    team, name: "实收汇总助手", description: "按地区汇总已核准行",
    tool: { operation: "aggregate", config: {
      group: "region", amount: "amount", filterColumn: "status", filterValue: "ok",
    } },
  });
  await post(admin, `agents/${agent.id}/validate`, {
    revision: 1, input: "region,amount,status\nEast,7.00,ok", expected: "7.00", authorizeOneCall: true,
  });
  await post(admin, `agents/${agent.id}/publish`, { revision: 1 });
  const service = await post(reviewer, "services", { name: "跨表实收汇总交接", agent: agent.id, instructions: "汇总已核准实收金额" });
  const app = await post(reviewer, `services/${service.id}/applications`, { name: "账表应用", days: 1 });
  const authorization = { Authorization: "Bearer " + app.token };
  const requestHeaders = () => ({ ...authorization, "Idempotency-Key": randomUUID() });
  const attachment = (name, data) => ({ name, data: Buffer.from(data).toString("base64") });
  const { id: run, project } = await post(admin, `v1/services/${service.id}/runs`, {
    version: 1, input: "合并两份实收报表", authorizeCalls: 1, attachments: [
      attachment("东区.csv", "region,Gross sales,Net settled amount,status\nEast,100.00,30.10,ok"),
      attachment("西区.csv", "region,标价合计,折后实收金额,status\nWest,80.00,60.20,ok"),
    ],
  }, { headers: requestHeaders(), status: 202 });
  const status = () => call(admin, `v1/runs/${run}`, { headers: authorization });
  async function waitFor(predicate, label) {
    for (let i = 0; i < 110; i += 1) {
      const value = await status();
      if (predicate(value)) return value;
      await new Promise(resolve => setTimeout(resolve, 180));
    }
    assert.fail(`${label}: ${JSON.stringify(await status())}`);
  }
  const pending = await waitFor(s => s.nextAction === "review_columns", "field review");
  assert.equal(pending.finalReviewer, "原审核人");
  assert.deepEqual(pending.columnCorrections, []);
  const budget = async () => (await call(admin, `teams/${team}/budget`)).resources.find(r => r.agent === agent.id);
  async function setLimit(limit) {
    const resource = await budget();
    await call(admin, `teams/${team}/resource-budgets/${resource.id}`, { method: "PUT", data: {
      limit: resource.limit, callLimit: limit, expected: resource.revision, warningPercent: 80,
      reason: "控制本次重算派发时机",
    } });
  }
  const before = await budget();
  await setLimit(before.calls);
  const confirm = async (actor, [east, west], button) => {
    for (const [file, column] of [["东区.csv", east], ["西区.csv", west]]) {
      await actor.locator("fieldset").filter({ hasText: file }).getByLabel(/amount 对应的原表列/).selectOption(column);
    }
    await actor.getByLabel(/我已核对字段含义/).check();
    const response = actor.waitForResponse(r => r.request().method() === "POST" && r.url().endsWith(`/runs/${run}/calls/1/review`));
    await actor.getByRole("button", { name: button, exact: true }).click();
    const receipt = await response;
    assert.equal(receipt.status(), 202, await receipt.text());
  };
  await reviewer.goto(pending.humanActionUrl);
  await confirm(reviewer, ["Gross sales", "标价合计"], "按对应字段重新计算");
  await waitFor(s => s.status === "blocked", "budget block after first approval");
  assert.equal((await budget()).calls, before.calls);

  await call(admin, `admin/members/${old}`, {
    method: "PATCH", data: { role: "member", enabled: false, employment: "depart", password, reason: "原审核人已离职" },
  });
  const current = await waitFor(s => s.nextAction === "review_columns", "reconfirmation after departure");
  assert.deepEqual(current.columnCorrections, []);
  assert.equal(current.project, project);
  await setLimit(null);
  const action = (data, expected) => post(admin, `v1/runs/${run}/actions`, data, { headers: requestHeaders(), status: expected });
  const blocked = await action({ action: "resume", expected: current.controlRevision, reason: "预算恢复" }, 409);
  assert.match(JSON.stringify(blocked), /重新确认原表字段/);
  assert.equal((await budget()).calls, before.calls);
  const recovery = async actor => (await call(actor, "project-recoveries")).find(item => item.project === project);
  const candidate = await recovery(admin);
  assert.equal(candidate.state, "available");
  const { id: offer } = await post(admin, `project-recoveries/${project}`, {
    email, reason: "重新核对两份报表的实收金额字段并审核交付", expected: candidate.revision, hours: 2, grantAccess: true,
  });
  await post(successor, `project-recoveries/${project}/${offer}`, {
    action: "accept", reason: "接受本次报表审核职责", expected: (await recovery(successor)).revision,
  });
  const detail = await call(successor, `projects/${project}`);
  const [attempt] = detail.run.attempts;
  assert.equal(detail.run.id, run);
  assert.equal(attempt.callReview.attempt, 1);
  assert.equal(attempt.callReview.reconfirmationRequired, true);
  assert.equal(attempt.callReview.authorized, false);
  assert.equal(attempt.callReview.history.length, 1);
  await successor.goto(current.humanActionUrl);
  const reconfirmation = successor.getByText("上一次确认的审核职责已变化。请重新核对字段，并授权本次重算。");
  await reconfirmation.waitFor();
  await reconfirmation.locator("xpath=ancestor::form").screenshot({ path: join(evidence, "column-reconfirmation-mobile.png") });
  await confirm(successor, ["Net settled amount", "折后实收金额"], "重新确认字段并授权重算");
  const reviewed = await waitFor(s => s.stage === "review", "same run produces one artifact");
  assert.equal(reviewed.finalReviewer, "接任审核人");
  assert.equal(reviewed.id, run);
  assert.equal(reviewed.project, project);
  assert.equal((await budget()).calls, before.calls + 1);
  await call(admin, `v1/runs/${run}/delivery/package`, { headers: authorization, status: 409 });
  await successor.getByRole("button", { name: /审核此版本/ }).click();
  await successor.getByRole("button", { name: "确认交付并下载成果包" }).click();
  await waitFor(s => s.stage === "delivered", "successor actually reviews and confirms");
  const download = await admin.request.get(`/api/v1/runs/${run}/delivery/package`, { headers: authorization });
  assert.ok(download.ok(), await download.text());
  await writeFile(join(evidence, "column-reconfirmation.zip"), await download.body());
  const { events } = await call(admin, `v1/runs/${run}/events`, { headers: authorization });
  for (const state of ["columns_confirmed", "columns_reconfirmed"]) {
    assert.equal(events.filter(event => event.state === state).length, 1);
  }
  await writeFile(join(evidence, "column-reconfirmation.json"), JSON.stringify({
    run, project, old, replacement, unchangedBeforeReconfirmation: true, extraCalls: 1, expectedTotal: "90.30",
  }, null, 2) + "\n");
} finally {
  await browser.close();
}

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
const local = process.env.COLUMN_REAL_MODEL === "1";
const artifact = local ? "column-suggestion-local" : "column-suggestion";
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const page = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  async function call(client, method, path, data, headers) {
    const response = await client.request.fetch("/api/" + path, { method, data, headers });
    assert.ok(response.ok(), await response.text());
    return response.status() === 204 ? undefined : response.json();
  }
  const post = (client, path, data, headers) => call(client, "POST", path, data, headers);
  const get = (client, path) => call(client, "GET", path);
  await post(admin, "login", { email: "review@sample.invalid", password });
  const { id: team } = await post(admin, "admin/teams", { name: local ? "本地模型字段验证" : "报表字段验证" });
  const administrator = await get(admin, "me");
  await call(admin, "PUT", `teams/${team}/budget-owner`, {
    member: administrator.id, previous: "", reason: "核对本次建议费用",
  });
  const model = await post(admin, "resources", {
    team, name: "报表字段模型", model: local ? "local-course-qwen" : "column-suggestion-fixture", endpoint,
    key: "synthetic-fields", estimate: "0.03",
  });
  await call(admin, "PUT", `teams/${team}/setup/planner`, {
    resource: model.id, revision: 1, expected: 0,
  });
  const invite = await post(admin, "admin/invitations", {});
  await post(page, "register", {
    email: local ? "column-local@sample.invalid" : "column-owner@sample.invalid", name: "字段核对人", token: invite.token, password,
  });
  const user = await get(page, "me");
  assert.equal(user.role, "member");
  await call(admin, "PUT", `admin/teams/${team}/members/${user.id}`, {
    joined: true, reason: "分配合成报表任务",
  });
  const agent = await post(admin, "agents/tools", {
    team, name: "含字段核对的汇总助手", description: "按地区核对核准金额",
    tool: { operation: "aggregate", config: {
      group: "region", amount: "amount", filterColumn: "status", filterValue: "ok",
    } },
  });
  await post(admin, `agents/${agent.id}/validate`, {
    revision: 1, input: "region,amount,status\nEast,7.00,ok", expected: "7.00",
    authorizeOneCall: true,
  });
  await post(admin, `agents/${agent.id}/publish`, { revision: 1 });

  async function openCase(files) {
    const { id: project } = await post(page, "projects", { team, goal: "内部工作目标" });
    const path = "projects/" + project;
    const pins = [];
    for (const [name, content] of files) {
      const file = await post(page, path + "/sources?name=" + encodeURIComponent(name), Buffer.from(content), {
        "Content-Type": "text/csv",
      });
      pins.push({ id: file.id, version: 1 });
    }
    await post(page, path + "/plan", {
      expected: (await get(page, path)).revision, agents: [agent.id], agentRevisions: [1],
      sources: pins, brief: "按区域汇总已核准金额",
    });
    const { id: run } = await post(page, path + "/runs", {
      expected: (await get(page, path)).revision, authorizeCalls: 1,
    }, { "Idempotency-Key": randomUUID() });
    await page.goto("/#" + project);
    await page.getByRole("button", { name: "按对应字段重新计算" }).waitFor();
    const state = await get(page, path);
    assert.equal(state.run.id, run);
    assert.equal(state.costSummary.calls, 1);
    return {
      project, path, run, pins,
      request: { attempt: 1, expected: state.run.controlRevision, authorizeOneCall: true },
    };
  }
  const button = page.getByRole("button", { name: "授权一次模型调用并推荐字段" });
  const apply = page.getByRole("button", { name: "将建议填入空白字段" });
  function suggestionPath(task) { return `runs/${task.run}/calls/1/column-suggestion`; }
  async function limitModel(limit) {
    const state = await get(admin, `teams/${team}/budget`);
    const budget = state.resources.find((r) => r.model === model.id);
    await call(admin, "PUT", `teams/${team}/resource-budgets/${budget.id}`, {
      limit: budget.limit, callLimit: limit, expected: budget.revision, warningPercent: 80, reason: "字段建议额度校验",
    });
  }

  const valid = await openCase([
    ["敏感东区.csv", "region,Gross sales,Net settled amount,status\nPRIVATE-ROW-367,100,30.10,ok"],
    ["西区.csv", "region,标价合计,折后实收金额,status\nWest,80,60.20,ok"],
  ]);
  await page.getByText(/报表字段模型.*预计 ¥0.030000/).waitFor();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  await limitModel(0);
  const denied = await page.request.post("/api/" + suggestionPath(valid), { data: valid.request });
  assert.equal(denied.status(), 409);
  assert.equal((await get(page, valid.path)).costSummary.calls, 1);
  const wrongActor = await admin.request.post("/api/" + suggestionPath(valid), { data: valid.request });
  assert.equal(wrongActor.status(), 404);
  await limitModel(null);
  const selects = page.getByLabel(/amount 对应的原表列/);
  const east = page.getByRole("group", { name: "敏感东区.csv · v1", exact: true }).getByRole("combobox");
  await east.selectOption("Gross sales");
  const started = page.waitForRequest(r => r.method() === "POST" && r.url().endsWith(suggestionPath(valid)));
  await button.click();
  await started;
  const concurrent = await post(page, suggestionPath(valid), valid.request);
  assert.ok(["submitted", "ready"].includes(concurrent.status));
  await apply.waitFor();
  await apply.click();
  assert.equal(await east.inputValue(), "Gross sales", "suggestion overwrote a member's choice");
  const west = page.getByRole("group", { name: "西区.csv · v1", exact: true }).getByRole("combobox");
  assert.equal(await west.inputValue(), "折后实收金额");
  assert.equal(await east.locator('option[value="Net settled amount"]').textContent(), "Net settled amount（Agent 建议）");
  await east.selectOption("Net settled amount");
  assert.equal(await selects.count(), 2);
  await apply.locator("xpath=ancestor::form").screenshot({ path: join(evidence, artifact + "-mobile.png") });
  const suggestedState = await get(page, valid.path);
  assert.equal(suggestedState.run.controlRevision, valid.request.expected);
  assert.equal(suggestedState.run.attempts[0].callReview.authorized, false);
  assert.equal(suggestedState.artifacts.length, 0);
  assert.equal(suggestedState.costSummary.calls, 2);
  const charge = suggestedState.costDetails.find(c => c.scope === "column_suggestion");
  assert.ok(local ? charge.tokens > 0 : charge.tokens === 31);
  assert.equal(charge.step, 1);
  assert.equal((await post(page, suggestionPath(valid), valid.request)).status, "ready");
  await page.reload();
  await apply.waitFor();
  assert.equal(await east.inputValue(), "");
  await apply.click();
  await page.getByLabel(/我已核对字段含义/).check();
  await page.getByRole("button", { name: "按对应字段重新计算" }).click();
  await page.getByRole("button", { name: "审核此版本" }).waitFor();
  const solved = await get(page, valid.path);
  assert.equal(solved.run.id, valid.run);
  assert.equal(solved.costSummary.calls, 3);
  assert.match(solved.artifacts[0].body, /合计 · 90\.30/);
  await page.getByRole("button", { name: "审核此版本" }).click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = page.waitForEvent("download");
  await page.getByRole("link", { name: "下载交付包（ZIP）" }).click();
  await (await download).saveAs(join(evidence, artifact + "-delivery.zip"));

  const report = {
    run: valid.run, billedModelCalls: 1, totalProjectCalls: 3, expectedTotal: "90.30",
    tokens: charge.tokens, preservedManualChoice: true, explicitRecalculation: true,
  };
  if (!local) {
    for (const [columnHeaders, rows, status, label] of [
      ["invalid_net", "East,15.00,ok", "invalid", "本次模型建议不可用"],
      ["possible_a,possible_b", "East,12.00,13.00,ok", "ambiguous", "Agent 无法判断字段含义"],
      ["unconfirmed_net", "East,15.00,ok", "unknown", "模型调用结果待核对"],
    ]) {
      const task = await openCase([["分支.csv", `region,${columnHeaders},status\n${rows}`]]);
      await button.click();
      await page.getByText(label, { exact: false }).waitFor();
      assert.equal(await apply.count(), 0);
      assert.equal(await selects.inputValue(), "");
      const snapshot = await get(page, task.path);
      assert.equal(snapshot.run.attempts[0].callReview.columnSuggestion.status, status);
      assert.equal(snapshot.costSummary.calls, 2);
      assert.equal(snapshot.artifacts.length, 0);
      assert.equal((await post(page, suggestionPath(task), task.request)).status, status);
    }

    const revoked = await openCase([["待撤销.csv", "region,revoked_net,status\nEast,5.00,ok"]]);
    const inFlight = page.request.post("/api/" + suggestionPath(revoked), { data: revoked.request });
    await page.waitForFunction(async (path) => (
      (await (await fetch("/api/" + path)).json()).run.attempts[0].callReview.columnSuggestion?.status === "submitted"
    ), revoked.path);
    await call(page, "DELETE", revoked.path + "/sources/" + revoked.pins[0].id);
    const result = await (await inFlight).json();
    assert.deepEqual(result, { status: "stale" });
    const after = await get(page, revoked.path);
    assert.equal(after.run.attempts[0].callReview.columnSuggestion, null);
    assert.equal(after.costSummary.calls, 2);
    assert.equal(after.artifacts.length, 0);

    report.withdrawnSuggestion = result.status;
  }
  const reportName = local ? "column-suggestions-local.json" : "column-suggestions.json";
  await writeFile(join(evidence, reportName), JSON.stringify(report, null, 2) + "\n");
} finally {
  await browser.close();
}

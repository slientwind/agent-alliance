import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const browser = await chromium.launch();
const checks = [];
try {
  for (const width of [390, 1440]) {
    const page = await browser.newPage({
      baseURL: process.env.SMOKE_ORIGIN,
      viewport: { width, height: 850 },
    });
    page.setDefaultTimeout(6000);
    const failures = [];
    page.on("pageerror", (error) => failures.push(error.message));
    const target = {
      team: "team",
      resource: null,
      scope: "project",
      name: "培训",
      owner: "财务",
      revision: 3,
    };
    const other = {
      ...target,
      scope: "resource_calls",
      resource: "connector",
      name: "计算",
    };
    const run = {
      id: "original",
      revision: 1,
      change: false,
      created: "2026-08-20T12:00:00Z",
    };
    const list = { targets: [target, other], runs: [run], items: [] };
    let denied = false;
    let stage = "paused";
    let reply;
    let slow = true,
      loseResponse = false;
    const keys = [];
    const sent = [];
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      const id = path.split("/")[3];
      if (path.endsWith("/budget-requests")) {
        if (route.request().method() === "POST") {
          sent.push(route.request().postDataJSON());
          keys.push(route.request().headers()["idempotency-key"]);
          const previous = keys.indexOf(keys.at(-1));
          if (
            previous !== keys.length - 1 &&
            JSON.stringify(sent[previous]) !== JSON.stringify(sent.at(-1))
          )
            return route.fulfill({
              status: 409,
              json: { error: "请求标识已用于其他预算申请" },
            });
          if (slow)
            await new Promise((resolve) => {
              reply = resolve;
            });
          if (loseResponse) {
            loseResponse = false;
            return route.abort("failed");
          }
          return route.fulfill({ json: { id: "receipt" } });
        }
        return route.fulfill({ json: list });
      }
      if (path === "/api/projects/" + id) {
        if (denied && id === "one")
          return route.fulfill({
            status: 403,
            json: { error: "已撤销项目访问" },
          });
        return route.fulfill({
          json: {
            id,
            title: id === "one" ? "培训项目" : "报表项目",
            stage,
            revision: 1,
            team: "team",
            owner: "owner",
            messages: [],
            participants: [],
            handoffs: [],
            distributionCases: [],
            sharedAgents: [],
            deliveries: [],
            artifacts: [],
            costSummary: { calls: 0, reserved: "0", recorded: "0", unknown: 0 },
            budget: { limit: "10", warningPercent: 80, pending: 0 },
            plan: null,
          },
        });
      }
      const data = {
        "/api/me": { id: "participant", name: "申请人", role: "member" },
        "/api/instance": { initialized: true, name: "验收" },
        "/api/teams": [
          { id: "team", name: "培训", default: true, joined: true },
        ],
      };
      return route.fulfill({ json: data[path] ?? [] });
    });
    const visit = async (id) => {
      await page.evaluate((id) => {
        location.hash = id;
      }, id);
      await page
        .getByRole("heading", { name: id === "one" ? "培训项目" : "报表项目" })
        .waitFor();
    };
    const open = async () => {
      await page.getByText(/^项目费用 ·/).click();
      await page.getByText("预算追加申请", { exact: true }).click();
      await page.getByText("填写追加申请", { exact: true }).click();
      await page.getByLabel("额度不足原因").waitFor();
    };
    await page.goto("/#one");
    await open();
    await page.getByLabel("关联预算阻塞任务").selectOption(run.id);
    await page.getByLabel("追加金额（CNY）").fill("6.25");
    const reason = page.getByLabel("额度不足原因");
    await reason.fill("项目一的用途");
    await page.getByLabel("剩余工作与新增需求").fill("补齐报告");
    await page.getByLabel("可选替代方案").fill("人工整理");
    await visit("two");
    await open();
    assert.equal(await reason.inputValue(), "");
    await reason.fill("项目二的用途");
    list.runs = [];
    list.targets = [other];
    await visit("one");
    await open();
    const submit = page.getByRole("button", { name: "提交预算申请" });
    assert.ok(await submit.isDisabled());
    assert.equal(await reason.inputValue(), "项目一的用途");
    assert.equal(
      await page.getByLabel("关联预算阻塞任务").inputValue(),
      run.id,
    );
    await page.getByLabel("追加到").selectOption("resource_callsconnector");
    assert.equal(await page.getByLabel("追加调用次数").inputValue(), "");
    await page.getByLabel("追加调用次数").fill("2");
    assert.ok(await submit.isDisabled());
    await page.getByLabel("关联预算阻塞任务").selectOption("");
    const pending = page.waitForRequest(
      (r) => r.method() === "POST" && r.url().endsWith("/budget-requests"),
    );
    await submit.click();
    await pending;
    await visit("two");
    await open();
    assert.equal(await reason.inputValue(), "项目二的用途");
    await visit("one");
    await open();
    await reason.fill("提交后另起的草稿");
    const settled = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" && r.url().endsWith("/budget-requests"),
    );
    reply();
    await settled;
    await page.getByRole("button", { name: "刷新申请记录" }).click();
    assert.equal(await reason.inputValue(), "提交后另起的草稿");
    assert.deepEqual(
      sent.map((s) => [
        s.scope,
        s.resource,
        s.amount,
        s.run,
        s.expected,
        s.reason,
      ]),
      [["resource_calls", "connector", "2", "", 3, "项目一的用途"]],
    );
    slow = false;
    await submit.click();
    await page.getByText(/这份草稿已有其他版本提交成功/).waitFor();
    assert.equal(keys[0], keys[1]);
    assert.equal(await reason.inputValue(), "提交后另起的草稿");
    stage = "cancelled";
    await visit("two");
    await visit("one");
    await open();
    assert.equal(await submit.count(), 0);
    assert.ok((await reason.getAttribute("readonly")) !== null);
    await page.getByRole("button", { name: "丢弃申请草稿" }).click();
    await reason.waitFor({ state: "detached" });
    assert.equal(
      await page.evaluate(() =>
        Object.keys(sessionStorage).some((k) =>
          k.startsWith("alliance.budget."),
        ),
      ),
      false,
    );
    stage = "paused";
    await visit("two");
    await visit("one");
    await open();
    assert.equal(await reason.inputValue(), "");
    // A lost receipt keeps the text and reuses the exact submission intent.
    slow = false;
    loseResponse = true;
    await page.getByLabel("追加调用次数").fill("1");
    await reason.fill("响应中断的申请");
    await page.getByLabel("剩余工作与新增需求").fill("复核报表");
    await page.getByLabel("可选替代方案").fill("人工处理");
    const dropped = page.waitForEvent(
      "requestfailed",
      (r) => r.method() === "POST" && r.url().endsWith("/budget-requests"),
    );
    await submit.click();
    await dropped;
    await page.getByText(/尚未收到明确回执/).waitFor();
    await visit("two");
    await visit("one");
    await open();
    assert.equal(await reason.inputValue(), "响应中断的申请");
    await submit.click();
    await page.getByText("已提交预算申请。", { exact: true }).waitFor();
    assert.deepEqual(sent[2], sent[3]);
    assert.ok(keys[2] && keys[2] === keys[3] && keys[0] !== keys[2]);
    assert.equal(await reason.inputValue(), "");
    await reason.fill("权限收回时清理");
    await visit("two");
    denied = true;
    await page.evaluate(() => {
      location.hash = "one";
    });
    await page.getByRole("heading", { name: "新对话" }).waitFor();
    denied = false;
    await visit("one");
    await open();
    assert.equal(await reason.inputValue(), "");
    assert.deepEqual(failures, []);
    checks.push({
      width,
      projectIsolation: true,
      missingTargetAndRunBlocked: true,
      lateReceiptPreservedNewerDraft: true,
      terminalReadOnly: true,
      revocationClearedDraft: true,
      lostReceiptReusedKey: true,
      acceptedDraftEditDidNotDuplicate: true,
      requests: sent.length,
    });
    await page.close();
  }
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/budget-drafts-mock.json",
    JSON.stringify({ input: "synthetic", transport: "mock", checks }, null, 2),
  );
} finally {
  await browser.close();
}

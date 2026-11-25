import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173",
    viewport: { width: 320, height: 740 },
  });
  const id = "2b40fe15-8725-45cf-9576-5661de125cb5";
  const source = "64023bae-ab1d-4d35-a467-45f80ef8cbc9";
  const externalAgent = "d514f853-296d-40cf-9c96-432330b76c81";
  const external = process.env.EXTERNAL_AGENT;
  const direct = external === "direct";
  const submitted = [];
  const decisions = [];
  let awaitingApproval = false;
  const review = process.env.APPROVAL_LEVEL === "L2";
  const search = process.env.WEB_SEARCH === "1";
  const details = {
    id, title: "测试对话", stage: "brief", revision: 1, owner: "member", reviewer: "member", team: "team", planVersion: 0,
    messages: [], artifacts: [], participants: [], handoffs: [], sharedAgents: [], deliveries: [], distributionCases: [],
    budget: { warning: false, pending: 0 }, costSummary: {}, run: null, suggestion: null, plan: null,
  };
  const sourcePath = "/api/projects/" + id + "/sources";
  const approvalsPath = "/api/projects/" + id + "/tool-approvals";
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/projects" && request.method() === "POST") {
      assert.equal(request.postDataJSON().goal, "请检查采购金额");
      details.messages.push({ kind: "user", seq: 1, body: request.postDataJSON().goal });
      return route.fulfill({ status: 201, json: { id } });
    }
    if (path === sourcePath && request.method() === "POST") {
      return route.fulfill({ status: 201, json: { id: source, version: 1, readable: true } });
    }
    if (path === "/api/projects/" + id + "/general-runs" && request.method() === "POST") {
      assert.ok(request.headers()["idempotency-key"]);
      const input = request.postDataJSON();
      submitted.push(input);
      details.stage = "running";
      details.revision++;
      details.planVersion++;
      details.run = { id: "run", status: "running", general: true, attempts: [] };
      awaitingApproval = !direct;
      return route.fulfill({ status: 202, json: { id: "run" } });
    }
    if (path === approvalsPath) {
      return route.fulfill({ json: awaitingApproval
        ? [{ id: "approval", level: review || external ? "L2" : "L1", revision: 1, tool: "reports", recipient: "records.example.test",
            external: !!external,
            planVersion: 1, arguments: { query: "X".repeat(600) } }]
        : [] });
    }
    if (path === approvalsPath + "/approval" && request.method() === "POST") {
      decisions.push(request.postDataJSON());
      awaitingApproval = false;
      details.revision++;
      return route.fulfill({ status: 204 });
    }
    if (path.endsWith("/session-events")) return route.fulfill({ contentType: "text/event-stream", body: ": connected\n\n" });
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "测试用户", role: "member", enabled: true },
      "/api/teams": [{ id: "team", name: "团队", joined: true, default: true }],
      "/api/general-tiers": direct ? [] : ["default", "fast", "reasoning"],
      "/api/search/availability": { enabled: search },
      "/api/agents/admissions": external ? [{ id: externalAgent, revision: 2, name: "外部采购助手", phase: "beta" }] : [],
      ["/api/projects/" + id]: details,
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("/");
  const input = page.getByRole("textbox", { name: "消息", exact: true });
  const tier = page.getByLabel("模型档位");
  if (!direct) {
    await tier.selectOption("fast");
    assert.equal(await tier.inputValue(), "fast");
    await page.getByRole("button", { name: "深度思考" }).click();
    assert.equal(await tier.inputValue(), "reasoning");
  }
  if (external) {
    await input.fill("@");
    await page.getByLabel("外部 Agent", { exact: true }).selectOption(externalAgent + "/2");
    assert.equal(await input.inputValue(), "");
    await page.getByLabel("外部 Agent 使用方式").selectOption(external);
  }
  await input.fill("请检查采购金额");
  const sendButton = page.getByRole("button", { name: "发送消息" });
  if (search) {
    const toggle = page.getByRole("button", { name: "联网搜索", exact: true });
    await toggle.click();
    assert.equal(await toggle.getAttribute("aria-pressed"), "true");
    assert.equal(await sendButton.isDisabled(), true);
    await page.getByRole("searchbox", { name: "公开搜索词" }).fill("公开采购规则");
  }
  if (!direct) {
    await page.locator('.composer input[type="file"]').setInputFiles({
      name: "采购.csv", mimeType: "text/csv", buffer: Buffer.from("金额\n27\n"),
    });
  }
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  const dispatched = page.waitForResponse(response => response.url().endsWith("/general-runs"));
  await sendButton.click();
  assert.equal((await dispatched).status(), 202);
  await page.waitForURL("**/chat/" + id);
  assert.deepEqual(submitted, [{
    goal: "请检查采购金额", expected: 1, sources: direct ? [] : [{ id: source, revision: 1 }],
    ...(direct ? {} : { tier: "reasoning" }),
    ...(search ? { searchQuery: "公开采购规则" } : {}),
    ...(external ? { externalAgents: [{ id: externalAgent, revision: 2 }], directExternal: direct } : {}),
  }]);
  if (!direct) {
    const approval = page.getByRole("region", { name: external ? "确认外部委派" : review ? "审核写入请求" : "确认付费查询" });
    await approval.getByText("records.example.test", { exact: true }).waitFor();
    await approval.getByText("查看发送内容", { exact: true }).click();
    await approval.getByText(/"query":/).waitFor();
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    const decided = page.waitForResponse(response => response.url().endsWith(approvalsPath + "/approval"));
    await approval.getByRole("button", { name: external ? "确认发送" : review ? "允许写入" : "确认并继续" }).click();
    assert.equal((await decided).status(), 204);
    assert.deepEqual(decisions, [{ expected: 1, accept: true }]);
    await approval.waitFor({ state: "hidden" });
  }
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  assert.deepEqual(errors, []);
  console.log("Composer sent the selected tier and attachment version, then rendered and submitted the inline tool confirmation.");
} finally {
  await browser.close();
}

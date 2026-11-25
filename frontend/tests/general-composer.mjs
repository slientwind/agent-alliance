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
  const submitted = [];
  const details = {
    id, title: "测试对话", stage: "brief", revision: 1, owner: "member", reviewer: "member", team: "team", planVersion: 0,
    messages: [], artifacts: [], participants: [], handoffs: [], sharedAgents: [], deliveries: [], distributionCases: [],
    budget: { warning: false, pending: 0 }, costSummary: {}, run: null, suggestion: null, plan: null,
  };
  const sourcePath = "/api/projects/" + id + "/sources";
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
      return route.fulfill({ status: 202, json: { id: "run" } });
    }
    if (path.endsWith("/session-events")) return route.fulfill({ contentType: "text/event-stream", body: ": connected\n\n" });
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "测试用户", role: "member", enabled: true },
      "/api/teams": [{ id: "team", name: "团队", joined: true, default: true }],
      "/api/general-tiers": ["default", "fast", "reasoning"],
      ["/api/projects/" + id]: details,
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("/");
  const input = page.getByRole("textbox", { name: "消息", exact: true });
  const tier = page.getByLabel("模型档位");
  await tier.selectOption("fast");
  assert.equal(await tier.inputValue(), "fast");
  await page.getByRole("button", { name: "深度思考" }).click();
  assert.equal(await tier.inputValue(), "reasoning");
  await input.fill("请检查采购金额");
  await page.locator('.composer input[type="file"]').setInputFiles({
    name: "采购.csv", mimeType: "text/csv", buffer: Buffer.from("金额\n27\n"),
  });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  const dispatched = page.waitForResponse(response => response.url().endsWith("/general-runs"));
  await page.getByRole("button", { name: "发送消息" }).click();
  assert.equal((await dispatched).status(), 202);
  await page.waitForURL("**/chat/" + id);
  assert.deepEqual(submitted, [{ goal: "请检查采购金额", tier: "reasoning", expected: 1, sources: [{ id: source, revision: 1 }] }]);
  assert.deepEqual(errors, []);
  console.log("Composer dispatched the selected model tier and exact attachment version without horizontal overflow.");
} finally {
  await browser.close();
}

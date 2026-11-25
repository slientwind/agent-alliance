import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 390, height: 840 },
  });
  page.setDefaultTimeout(9000);
  const project = "ce79221f-3d4c-45f7-a299-7015c7c556b4";
  const run = "52d87649-567b-4b73-ad35-7cc3a6dbfe5a";
  const draft = "3cf3a247-312c-4c93-88e9-ef2135591b3b";
  const input = "核对当月报价";
  const answer = "本期报价总计 27 元";
  const calls = [];
  const errors = [];
  let retried = false;
  page.on("pageerror", error => errors.push(error.message));
  await page.addInitScript(() => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: {
      writeText: async text => { window.fixtureCopiedText = text; },
    } });
  });
  const work = {
    id: project, title: "月度报价", stage: "brief", revision: 4, owner: "member", reviewer: "member", team: "team", planVersion: 0,
    messages: [{ seq: 1, kind: "user", body: input }],
    artifacts: [{ id: "reply", name: "任务答复", ordinal: 33, version: 1, body: answer, approved: false,
      reviewNotesReadable: true, feedback: [], sourceChanges: [], replyRun: run }],
    participants: [], handoffs: [], sharedAgents: [], deliveries: [], distributionCases: [],
    budget: { warning: false, pending: 0 }, costSummary: {}, suggestion: null, plan: null,
  };
  await page.route("**/api/**", route => {
    const request = route.request(), path = new URL(request.url()).pathname;
    if (request.method() === "POST") {
      calls.push({ path, body: request.postDataJSON() });
      if (path === "/api/studio/from-run") return route.fulfill({ status: 201, json: { id: draft } });
      if (path === `/api/projects/${project}/general-runs/${run}/retry`) {
        assert.ok(request.headers()["idempotency-key"]);
        retried = true;
        return route.fulfill({ status: 202, json: { id: "new-run" } });
      }
      return route.fulfill({ status: 204 });
    }
    if (path.endsWith("/session-events")) return route.fulfill({ contentType: "text/event-stream", body: ": connected\n\n" });
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "项目发起人", role: "member", enabled: true },
      "/api/teams": [{ id: "team", name: "测试团队", joined: true, default: true }],
      "/api/general-tiers": ["default"],
      [`/api/studio/${draft}`]: { id: draft, team: "team", revision: 1,
        spec: { name: "核对助手", purpose: "", instructions: "", steps: [], checks: [], capabilities: [], tier: "default" }, messages: [] },
      [`/api/projects/${project}`]: { ...work, run: { id: retried ? "new-run" : run, status: retried ? "queued" : "completed", general: true, attempts: [] } },
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/chat/" + project);
  const footer = page.getByRole("group", { name: "答复操作" });
  await footer.getByRole("button", { name: "复制答复" }).click();
  await footer.getByText("已复制答复").waitFor();
  assert.equal(await page.evaluate(() => window.fixtureCopiedText), answer);
  await footer.getByRole("button", { name: "答复有帮助" }).click();
  await footer.getByText("已记录反馈").waitFor();
  assert.equal(await footer.getByRole("button", { name: "答复有帮助" }).getAttribute("aria-pressed"), "true");
  await footer.getByRole("button", { name: "答复没帮助" }).click();
  await page.waitForFunction(() => document.querySelector('[aria-label="答复没帮助"]')?.getAttribute("aria-pressed") === "true");
  await footer.getByRole("button", { name: "设为定时" }).click();
  await page.waitForURL("**/automations");
  assert.equal(await page.getByLabel("任务名称").inputValue(), work.title);
  assert.equal(await page.getByLabel("工作目标").inputValue(), input);
  await page.goBack();
  await footer.getByRole("button", { name: "存为助手" }).click();
  await page.waitForURL("**/agents/studio/" + draft);
  await page.goBack();
  const retriedResponse = page.waitForResponse(response => response.url().endsWith("/retry"));
  await footer.getByRole("button", { name: "重新生成答复" }).click();
  assert.equal((await retriedResponse).status(), 202);
  assert.deepEqual(calls, [
    { path: "/api/telemetry/signals", body: { runId: run, kind: "reply_copied" } },
    { path: "/api/telemetry/signals", body: { runId: run, kind: "feedback_thumbs_up" } },
    { path: "/api/telemetry/signals", body: { runId: run, kind: "feedback_thumbs_down" } },
    { path: "/api/studio/from-run", body: { run } },
    { path: `/api/projects/${project}/general-runs/${run}/retry`, body: { expected: 4 } },
  ]);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  assert.deepEqual(errors, []);
  console.log("Reply actions copied, recorded feedback, seeded a schedule, opened Studio, and retried.");
} finally {
  await browser.close();
}

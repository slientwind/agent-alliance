import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179" });
  await page.addInitScript(() => {
    window.testStreams = {};
    window.EventSource = class extends EventTarget {
      static CLOSED = 2;
      readyState = 1;
      constructor(url) {
        super();
        window.testStreams[url.split("/").at(-1)] = this;
        setTimeout(() => { if (this.readyState !== 2) this.onopen?.(); });
      }
      close() { this.readyState = 2; }
    };
  });
  const id = "60cf1e60-ac61-48ae-a364-a2a30189a311";
  const run = "b2b5b568-6955-4697-ae9b-c52135f7e73f";
  const project = {
    id, title: "销售汇总", stage: "running", revision: 1, owner: "member", reviewer: "member", team: "team", planVersion: 1,
    messages: [], artifacts: [], participants: [], handoffs: [], sharedAgents: [], deliveries: [], distributionCases: [],
    budget: { warning: false, pending: 0 }, costSummary: {}, suggestion: null, plan: null,
    run: { id: run, status: "running", general: true, controlRevision: 1, attempts: [], draftAvailable: false },
  };
  await page.route("**/api/**", route => {
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "测试用户", role: "member", enabled: true },
      "/api/teams": [{ id: "team", name: "团队", joined: true, default: true }],
      "/api/projects": [project], [`/api/projects/${id}`]: project,
    };
    return route.fulfill({ json: data[new URL(route.request().url()).pathname] ?? [] });
  });
  const send = (stream, type, data) => page.evaluate(({ stream, type, data }) => {
    window.testStreams[stream].dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }));
  }, { stream, type, data });

  await page.goto(`/chat/${id}`);
  await page.waitForFunction(() => !!window.testStreams["session-events"]);
  await send("session-events", "session.snapshot", { run: "", drafts: [], hot: true });
  await page.waitForFunction(() => !!window.testStreams["live-events"]);
  await send("live-events", "hot.reset", { run, drafts: [] });
  await send("live-events", "thinking.progress", { run, step: 1, attempt: 1, text: "正在处理这轮任务" });
  await page.getByText("正在处理这轮任务", { exact: true }).waitFor();
  const draft = { run, step: 1, attempt: 1, status: "submitted" };
  await send("live-events", "draft.delta", { ...draft, offset: 0, text: "第一段" });
  await send("session-events", "session.snapshot", { run: "", drafts: [], hot: true });
  await send("live-events", "draft.delta", { ...draft, offset: Buffer.byteLength("第一段"), text: "与第二段" });
  await page.getByText("第一段与第二段", { exact: true }).waitFor();
  await send("live-events", "hot.reset", { run: "", drafts: [] });
  await page.getByRole("region", { name: "回复草稿" }).waitFor({ state: "hidden" });
  assert.equal(await page.getByText("第一段与第二段", { exact: true }).count(), 0);
} finally {
  await browser.close();
}

import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179", viewport: { width: 390, height: 840 } });
  const messages = [];
  page.on("pageerror", error => messages.push(error.message));
  const runs = "/api/spaces/team-space/eval-suites/suite-fixture/runs";
  let submitted;
  await page.route("**/api/**", route => {
    const req = route.request();
    const url = new URL(req.url()).pathname;
    if (url === runs && req.method() === "POST") {
      submitted = req.postDataJSON();
      return route.fulfill({ status: 202, json: { id: "run-fixture" } });
    }
    const fixture = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "owner", name: "空间管理员", role: "admin", enabled: true },
      "/api/spaces": [{ id: "team-space", kind: "team", name: "供应链", role: "space_admin" }],
      "/api/admin/models/providers": [{ id: "provider", name: "模型供应商", kind: "openai", enabled: true,
        models: [{ id: "candidate", name: "候选模型", enabled: true, capabilities: ["text"] }] }],
      "/api/spaces/team-space/eval-suites": [{ id: "suite-fixture", name: "出库口径", caseCount: 1 }],
      [runs]: submitted ? [{ id: "run-fixture", kind: submitted.kind, state: "completed", report: {
        baselinePassRate: 1, candidatePassRate: 0, baselineLatencyMs: 300, candidateLatencyMs: 510, caseCount: 1,
        cases: [{ caseId: "quantity", baseline: { passed: true }, candidate: { passed: false, deviations: ["number:1"] } }],
      } }] : [],
    };
    return route.fulfill({ json: fixture[url] ?? [] });
  });
  await page.goto("/admin/models");
  const panel = page.getByRole("region", { name: "黄金回归体检" });
  await panel.getByLabel("候选模型").selectOption("candidate");
  await panel.getByRole("button", { name: "运行回归体检" }).click();
  await panel.getByText("100% → 0%").waitFor();
  await panel.getByText("300 → 510 ms").waitFor();
  await panel.getByText("查看口径偏差").click();
  await panel.getByText("指标数值").waitFor();
  assert.deepEqual(submitted, { kind: "model", baselineTier: "default", candidateModelId: "candidate" });
  await panel.getByLabel("变更类型").selectOption("knowledge");
  await panel.getByRole("button", { name: "运行回归体检" }).click();
  await panel.getByText("已完成 · 知识变更预检").waitFor();
  assert.deepEqual(submitted, { kind: "knowledge", baselineTier: "default" });
  assert.deepEqual(messages, []);
} finally {
  await browser.close();
}

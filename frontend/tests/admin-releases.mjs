import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 375, height: 760 },
  });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const digest = "d".repeat(64);
  const requests = [];
  let admin = true;
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() === "POST") {
      requests.push({ path, body: request.postDataJSON() });
      return route.fulfill({ status: 204 });
    }
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "审批用户", role: admin ? "admin" : "member", enabled: true },
      "/api/teams": [{ id: "team", name: "测试团队", joined: true, default: true }],
      "/api/admin/studio/releases": [
        { id: "studio", revision: 4, name: "业务核对助手", owner: "业务同事", team: "财务", digest,
          spec: { purpose: "核对报表", instructions: "合计后复核", steps: ["核对明细"], checks: ["检查余额"] },
          dependencies: { modelTier: "default", capabilities: ["office"], externalAgents: [] } },
      ],
      "/api/admin/external-agents/releases": [
        { id: "verify", agent: "external", revision: 2, name: "外部采购助手", description: "查询公示规则",
          state: "pending_verify", buildDigest: "build", evidenceDigest: "evidence" },
      ],
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/admin/releases");
  await page.getByText("查看冻结的配置").click();
  await page.getByText("合计后复核").waitFor();
  await page.getByRole("button", { name: "批准上架" }).click();
  await page.getByText("业务核对助手 已全企可用").waitFor();
  await page.getByRole("button", { name: "外部 Agent 认证" }).click();
  const request = page.getByRole("region", { name: "外部采购助手" });
  assert.equal(await request.getByRole("button", { name: "退回", exact: true }).isDisabled(), true);
  await request.getByLabel("退回原因").fill("请补充一条公开规则查询样例");
  await request.getByRole("button", { name: "退回", exact: true }).click();
  await page.getByText("外部采购助手 已退回").waitFor();
  assert.deepEqual(requests, [
    { path: "/api/admin/studio/releases/studio", body: { action: "approve", digest, reason: "" } },
    { path: "/api/agents/external/release-requests/verify", body: { action: "return", revision: 2,
      buildDigest: "build", evidenceDigest: "evidence", reason: "请补充一条公开规则查询样例" } },
  ]);
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  admin = false;
  await page.reload();
  await page.getByText("需要企业管理员权限").waitFor();
  assert.equal(await page.getByRole("button", { name: "外部 Agent 认证" }).count(), 0);
  assert.deepEqual(errors, []);
  console.log("The release queue reviewed a frozen Studio release and an external verification request.");
} finally {
  await browser.close();
}

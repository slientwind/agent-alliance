import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";

const origin = process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173";
const evidence = process.env.VISUAL_EVIDENCE;
const initialFrom = "2026-03-01T00:00:00Z";
const initialTo = "2026-04-01T00:00:00Z";
const member = {
  id: "user1",
  name: "培训负责人",
  kind: "person",
  runs: 3,
  completed: 2,
  waiting: 1,
  attempts: 7,
  tokens: 132,
  unknown: 1,
  recordedCny: "0.325000",
  passedAgentChecks: 1,
  reviewedVersions: 2,
  adoptedAnswers: 0,
  deliveredEditions: 1,
  unsettledCalls: 1,
  remainingEstimateCny: "0.000002",
};
const app = {
  ...member,
  id: "app1",
  name: "业务申请入口",
  kind: "application",
  runs: 1,
  waiting: 0,
  completed: 1,
  attempts: 2,
};
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  let report = {
    from: initialFrom,
    to: initialTo,
    items: [member, app],
    enterpriseExport: { id: "grant1", name: "协作演练企业", costs: false },
  };
  let generated;
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/admin/usage")
      return route.fulfill({ json: report });
    if (
      url.pathname.endsWith("/exports") &&
      route.request().method() === "POST"
    ) {
      generated = route.request().postDataJSON();
      return route.fulfill({
        status: 201,
        json: { id: "file", expiresAt: "2026-04-01T01:00:00Z" },
      });
    }
    const data = {
      "/api/instance": { name: "协作演练企业", initialized: true },
      "/api/me": { id: member.id, name: member.name, role: "admin" },
    };
    return route.fulfill({ json: data[url.pathname] ?? [] });
  });
  await page.goto(origin);
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByRole("button", { name: "企业管理", exact: true }).click();
  await page.getByRole("button", { name: "用户与监控", exact: true }).click();
  const panel = page.getByRole("region", { name: "运行监控" });
  const prepare = panel.getByRole("button", { name: "准备导出汇总" });
  await prepare.waitFor();
  if (evidence)
    await panel.screenshot({
      path: path.join(evidence, "enterprise-dashboard-mobile.png"),
    });
  assert.ok(await panel.evaluate((e) => e.scrollWidth <= e.clientWidth));
  await prepare.click();
  const form = panel.getByRole("region", { name: "管理汇总导出" });
  assert.equal(await form.getByRole("checkbox").count(), 0);
  await form.getByLabel("接收对象").fill("培训项目组");
  await form.getByLabel("使用目的").fill("核对月报");
  async function refresh() {
    const finished = page.waitForResponse(
      (r) => new URL(r.url()).pathname === "/api/admin/usage",
    );
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await (await finished).finished();
  }
  report = {
    ...report,
    from: "2026-03-02T00:00:00Z",
    to: "2026-04-02T00:00:00Z",
  };
  await refresh();
  await form.getByRole("button", { name: "生成 JSON 文件" }).click();
  await form.getByRole("button", { name: "下载管理汇总" }).waitFor();
  assert.equal(generated.from, initialFrom);
  assert.equal(generated.to, initialTo);
  assert.equal(generated.costs, false);
  await panel.getByLabel("发起成员或应用").selectOption(app.id);
  assert.equal(await prepare.count(), 0);
  assert.equal(await form.count(), 0);
  await panel.getByLabel("发起成员或应用").selectOption("");
  await prepare.waitFor();
  report = { ...report, enterpriseExport: null };
  await refresh();
  await prepare.waitFor({ state: "detached" });
  await panel.getByText(/发起运行 4/).waitFor();
  if (evidence)
    await writeFile(
      path.join(evidence, "enterprise-dashboard-ui.json"),
      JSON.stringify(
        {
          source: "Chromium with synthetic dashboard responses",
          viewport: "390x844",
          fullReportAvailableOnAllOnly: true,
          preparedWindowSurvivedRefresh: true,
          adminFeesNotInBusinessOnlyExport: true,
          withdrawalRetainedDashboard: true,
          overflow: false,
        },
        null,
        2,
      ),
    );
  console.log(
    "Enterprise dashboard: fixed reporting window, filter switch, independent expense fields, withdrawal and mobile layout",
  );
} finally {
  await browser.close();
}

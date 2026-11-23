import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";

const origin = process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173";
const output = process.env.VISUAL_EVIDENCE;
const alice = {
  id: "member",
  name: "项目发起人",
  kind: "person",
  runs: 4,
  completed: 3,
  waiting: 1,
  attempts: 9,
  tokens: 107,
  unknown: 1,
  recordedCny: "10000000000.000001",
  passedAgentChecks: 1,
  reviewedVersions: 3,
  adoptedAnswers: 2,
  deliveredEditions: 1,
  unsettledCalls: 2,
  remainingEstimateCny: "0.099999",
};
const app = {
  ...alice,
  id: "app",
  name: "内部应用",
  kind: "application",
  runs: 1,
  completed: 1,
  waiting: 0,
  attempts: 1,
  tokens: 12,
  unknown: 0,
  reviewedVersions: 2,
  adoptedAnswers: 0,
  passedAgentChecks: 0,
  deliveredEditions: 2,
  unsettledCalls: 1,
  recordedCny: "0.000002",
  remainingEstimateCny: "0.000001",
};
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  let release, started;
  let legacy = false;
  const delayed = new Promise((resolve) => {
    release = resolve;
  });
  const requested = new Promise((resolve) => {
    started = resolve;
  });
  await page.route("**/api/**", async (route) => {
    const { pathname, searchParams } = new URL(route.request().url());
    if (pathname === "/api/admin/usage") {
      if (legacy) {
        const {
          passedAgentChecks,
          reviewedVersions,
          adoptedAnswers,
          deliveredEditions,
          unsettledCalls,
          remainingEstimateCny,
          ...previous
        } = app;
        await route.fulfill({ json: [previous] });
        return;
      }
      if (searchParams.get("days") === "7") {
        started();
        await delayed;
        await route.fulfill({
          status: 503,
          json: { error: "七天汇总暂不可用" },
        });
      } else {
        await route.fulfill({
          json: searchParams.get("days") === "90" ? [app] : [alice, app],
        });
      }
      return;
    }
    const responses = {
      "/api/instance": { initialized: true, name: "合成企业" },
      "/api/me": { id: "member", name: "示例管理员", role: "admin" },
    };
    await route.fulfill({ json: responses[pathname] ?? [] });
  });
  await page.goto(origin);
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByRole("button", { name: "企业管理", exact: true }).click();
  await page.getByRole("button", { name: "用户与监控", exact: true }).click();
  const panel = page.getByRole("region", { name: "运行监控" });
  await panel.getByText(/已记账 ¥10,000,000,000\.000003/).waitFor();
  await panel.getByText(/剩余预估 ¥0\.10 · 1 笔未知费用待核对/).waitFor();
  assert.equal(
    await panel
      .locator("dt")
      .filter({ hasText: "正式交付批次" })
      .locator("../dd")
      .innerText(),
    "3",
  );
  await panel.getByLabel("发起成员或应用").selectOption(app.id);
  await panel.getByText(/已记账 ¥0\.000002/).waitFor();
  assert.equal(
    await panel
      .locator("dt")
      .filter({ hasText: "正式交付批次" })
      .locator("../dd")
      .innerText(),
    "2",
  );
  const select = panel.getByLabel("统计周期");
  await select.selectOption("7");
  await requested;
  await panel.getByRole("status").waitFor();
  await select.selectOption("90");
  await panel.getByText(/剩余预估 ¥0\.000001/).waitFor();
  const late = page.waitForResponse((r) => {
    const url = new URL(r.url());
    return (
      url.pathname === "/api/admin/usage" &&
      url.searchParams.get("days") === "7"
    );
  });
  release();
  await (await late).finished();
  await page.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(resolve)),
      ),
  );
  assert.equal(await panel.getByRole("alert").count(), 0);
  await panel.getByText(/剩余预估 ¥0\.000001/).waitFor();
  await select.selectOption("7");
  await panel.getByRole("alert").getByText("七天汇总暂不可用").waitFor();
  assert.equal(await panel.locator("dd").count(), 0);
  await select.selectOption("30");
  await panel.getByLabel("发起成员或应用").selectOption("");
  await panel.getByText(/已记账 ¥10,000,000,000\.000003/).waitFor();
  assert.equal(
    await panel.evaluate((el) => el.scrollWidth <= el.clientWidth),
    true,
  );
  if (output) {
    await mkdir(output, { recursive: true });
    await panel.screenshot({
      path: path.join(output, "mobile-management.png"),
    });
  }
  legacy = true;
  await select.selectOption("90");
  await panel.getByText(/当前管理服务提供近 30 天/).waitFor();
  assert.equal(await select.inputValue(), "30");
  assert.equal(await select.isDisabled(), true);
  await panel.getByText(/已记账 ¥0\.000002/).waitFor();
  assert.equal(
    await panel.getByText("正式交付批次", { exact: true }).count(),
    0,
  );
  const serverTime = "2020-05-10T00:00:00Z";
  const expiresAt = "2020-05-11T00:00:00Z";
  const first = {
    id: "first",
    kind: "team",
    name: "项目组",
    expiresAt,
    costs: false,
  };
  const second = { ...first, id: "second", name: "运营组" };
  let scopes = [first, second],
    secondReads = 0;
  await page.route("**/api/management-scopes**", (route) => {
    const url = new URL(route.request().url());
    if (url.pathname.endsWith("/management-scopes"))
      return route.fulfill({ json: scopes });
    const isFirst = url.pathname.includes("/first/");
    if (!isFirst) secondReads++;
    return route.fulfill({
      json: {
        runs: isFirst ? 4 : 17,
        completed: 1,
        waiting: 0,
        reviewed: 1,
        adopted: 0,
        editions: 1,
        checked: 0,
        from: "2020-04-10T00:00:00Z",
        to: "2021-05-10T00:00:00Z",
        checkedAt: serverTime,
        expiresAt,
        costs: null,
      },
    });
  });
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByText("协作与授权", { exact: true }).click();
  await page.getByText("获授权的管理汇总", { exact: true }).click();
  const scoped = page.getByRole("region", { name: "范围汇总结果" });
  const runs = scoped
    .locator("dt")
    .filter({ hasText: "发起运行" })
    .locator("../dd");
  await runs.getByText("4", { exact: true }).waitFor();
  scopes = [second];
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await page.getByText(/先前所选范围的访问已结束/).waitFor();
  assert.equal(await scoped.count(), 0);
  assert.equal(secondReads, 0);
  await page.getByLabel("已授权范围").selectOption("second");
  await runs.getByText("17", { exact: true }).waitFor();
  assert.equal(secondReads, 1);
  if (output) {
    await writeFile(
      path.join(output, "management-ui.json"),
      JSON.stringify(
        {
          data: "synthetic browser fixture",
          viewport: "390x844",
          allRecordedCny: "10000000000.000003",
          selectedRecordedCny: "0.000002",
          remainingEstimateCny: "0.10",
          staleResponseIsolated: true,
          failedPeriodHidesTotals: true,
          recovered: true,
          horizontalOverflow: false,
          oldApiShowsOnlyAvailableMeasures: true,
          revokedScopeRequiresExplicitSelection: true,
          serverClockControlsScopeExpiry: true,
        },
        null,
        2,
      ),
    );
  }
  console.log(
    "Browser: exact six-decimal totals, member filter, overlapping periods, failed request, recovery, mobile layout and old API compatibility OK",
  );
} finally {
  await browser.close();
}

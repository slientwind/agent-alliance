import { chromium } from "playwright";
import assert from "node:assert/strict";
import path from "node:path";
import { writeFile } from "node:fs/promises";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  const origin = process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173";
  let releaseFirst, started;
  const pending = new Promise((resolve) => {
    started = resolve;
  });
  const hold = new Promise((resolve) => {
    releaseFirst = resolve;
  });
  let scopeAExpenses = true;
  const summary = {
    from: "2026-03-01T00:00:00Z",
    to: "2026-04-01T00:00:00Z",
    checkedAt: "2026-04-01T00:00:01Z",
    expiresAt: "2026-04-02T00:00:01Z",
    runs: 1,
    completed: 1,
    waiting: 0,
    checked: 0,
    adopted: 0,
    editions: 0,
    reviewed: 0,
    costs: null,
  };
  const task = (id) => ({
    id,
    actor: "张华",
    kind: "person",
    reviewer: "李芳",
    status: "completed",
    createdAt: summary.from,
    finishedAt: summary.from,
    elapsedSeconds: 0,
    block: null,
  });
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url()),
      endpoint = url.pathname;
    let data = [];
    if (endpoint === "/api/instance")
      data = { initialized: true, name: "合成企业" };
    if (endpoint === "/api/me")
      data = { id: "user", name: "试用成员", role: "member" };
    if (endpoint === "/api/management-scopes")
      data = ["a", "b"].map((id) => ({
        id,
        name: id === "a" ? "甲部门" : "乙部门",
        kind: "department",
        details: true,
        detailCosts: id === "a" && scopeAExpenses,
        expiresAt: summary.expiresAt,
      }));
    if (endpoint.endsWith("/summary")) data = summary;
    if (endpoint.endsWith("/members"))
      data = {
        rows: [{ id: "person", name: "张华", kind: "person", runs: 1 }],
      };
    if (endpoint.endsWith("/tasks")) {
      const isA = endpoint.includes("/a/");
      if (isA && scopeAExpenses) {
        started();
        await hold;
      }
      const row = task(isA ? "甲部门任务编号" : "乙部门任务编号");
      if (isA && scopeAExpenses)
        row.costs = {
          entries: 1,
          recordedCny: "18.000000",
          remainingEstimateCny: "0",
          unknown: 0,
          pending: 0,
        };
      data = { rows: [row] };
    }
    return route.fulfill({ json: data });
  });
  await page.goto(origin);
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByText("协作与授权", { exact: true }).click();
  await page.getByText("获授权的管理汇总", { exact: true }).click();
  const scope = page.getByLabel("已授权范围");
  const details = page.getByText("成员与任务明细", { exact: true });
  const list = page.getByRole("region", { name: "管理明细" });
  await scope.selectOption("a");
  await details.click();
  await list.getByRole("button", { name: "全部任务" }).click();
  await pending;
  await scope.selectOption("b");
  await details.click();
  await list.getByRole("button", { name: "全部任务" }).click();
  await list.getByText("任务 乙部门任务编号").waitFor();
  const old = page.waitForResponse((r) => r.url().includes("/a/tasks"));
  releaseFirst();
  await (await old).finished();
  await page.evaluate(
    () =>
      new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  );
  assert.equal(await list.getByText(/甲部门任务编号|18\.000000/).count(), 0);
  await scope.selectOption("a");
  await details.click();
  await list.getByRole("button", { name: "张华 · 查看任务" }).click();
  await list.getByText(/18\.000000/).waitFor();
  scopeAExpenses = false;
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await list.waitFor({ state: "detached" });
  await details.click();
  await list.getByRole("button", { name: "全部任务" }).click();
  await list.getByText("任务 甲部门任务编号").waitFor();
  assert.equal(await list.getByText(/18\.000000/).count(), 0);
  assert.ok((await list.innerText()).includes("不足 1 秒"));
  assert.ok(await list.evaluate((el) => el.scrollWidth <= el.clientWidth));
  if (process.env.VISUAL_EVIDENCE)
    await writeFile(
      path.join(process.env.VISUAL_EVIDENCE, "management-detail-switch.json"),
      JSON.stringify(
        {
          input: "synthetic delayed responses, Chromium mobile",
          latePreviousScopeHidden: true,
          expenseWithdrawalPreservedTask: true,
          subSecondDurationVisible: true,
          overflow: false,
        },
        null,
        2,
      ),
    );
  console.log(
    "Mobile: delayed previous scope response concealed, expense withdrawal preserves task visibility",
  );
} finally {
  await browser.close();
}

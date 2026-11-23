import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const context = await browser.newContext({
    baseURL: process.env.SMOKE_ORIGIN,
    viewport: { width: 390, height: 844 },
  });
  const page = await context.newPage();
  const logged = await context.request.post("/api/login", {
    data: {
      email: "review@sample.invalid",
      password: process.env.BROWSER_PASSWORD,
    },
  });
  assert.equal(logged.status(), 200);
  const me = await (await context.request.get("/api/me")).json();
  const get = async (url) => {
    const r = await context.request.get("/api/" + url);
    assert.equal(r.status(), 200);
    return r.json();
  };
  const team = (await get("teams")).find((t) => t.default);
  const usage = async () => {
    const [r] = await get(
      "admin/usage?days=7&member=" + me.id + "&team=" + team.id,
    );
    assert.equal(
      Object.values(r.runStates).reduce((sum, n) => sum + n, 0),
      r.runs,
    );
    return r;
  };
  const before = await usage();
  await page.goto("/#" + process.env.FAILURE_PROJECT);
  const control = page
    .locator("details")
    .filter({ has: page.getByText("控制运行", { exact: true }) });
  await control.locator("summary").first().click();
  await control
    .getByRole("combobox")
    .selectOption("confirm_failure");
  await control
    .getByRole("textbox", { name: "原因" })
    .fill("已核对远端拒绝回执，结束本次运行");
  const sent = page.waitForRequest(
    (req) =>
      req.method() === "POST" &&
      req.url().endsWith("/runs/" + process.env.FAILURE_RUN + "/control"),
  );
  await page.getByRole("button", { name: "确认控制动作" }).click();
  const command = await sent;
  assert.equal((await command.response()).status(), 202);
  const replay = await context.request.post(command.url(), {
    data: command.postDataJSON(),
    headers: { "Idempotency-Key": command.headers()["idempotency-key"] },
  });
  assert.equal(replay.status(), 202);
  const after = await usage();
  assert.equal(after.runStates.failed, (before.runStates.failed ?? 0) + 1);
  assert.equal(after.runStates.reconcile ?? 0, before.runStates.reconcile - 1);
  assert.equal(after.runs, before.runs);
  assert.equal(after.attempts, before.attempts);
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByRole("button", { name: "企业管理", exact: true }).click();
  await page.getByRole("button", { name: "用户与监控", exact: true }).click();
  const monitor = page.getByRole("region", { name: "运行监控" });
  await monitor.getByLabel("统计周期").selectOption("7");
  await monitor.getByLabel("用量统计团队").selectOption(team.id);
  await monitor.getByLabel("发起成员或应用").selectOption(me.id);
  const breakdown = monitor
    .locator("details")
    .filter({ has: page.getByText(/^运行状态分布/) });
  await breakdown.locator("summary").click();
  await breakdown
    .locator("dt")
    .filter({ hasText: "执行失败" })
    .locator("../dd")
    .getByText(String(after.runStates.failed), { exact: true })
    .waitFor();
  await breakdown.screenshot({
    path: path.join(process.env.VISUAL_EVIDENCE, "run-states-mobile.png"),
  });
  await writeFile(
    path.join(process.env.VISUAL_EVIDENCE, "run-states.json"),
    JSON.stringify(
      {
        before: before.runStates,
        after: after.runStates,
        source: "local A2A rejection, real Go API and PostgreSQL",
        runs: after.runs,
        extraCalls: after.attempts - before.attempts,
        repeatStatus: replay.status(),
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

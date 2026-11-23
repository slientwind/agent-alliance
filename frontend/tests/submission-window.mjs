import assert from "node:assert/strict";
import { chromium } from "playwright";
const browser = await chromium.launch();
try {
  const c = await browser.newContext({ baseURL: process.env.SMOKE_ORIGIN });
  const res = await c.request.post("/api/login", {
    data: {
      email: "review@sample.invalid",
      password: process.env.BROWSER_PASSWORD,
    },
  });
  assert.equal(res.status(), 200);
  const p = await c.newPage();
  await p.goto("/#" + process.env.SUBMISSION_PROJECT);
  await p.getByText(/待核对调用 · 调用记录/).click();
  const section = p.getByText("步骤 1 · 调用核对", { exact: true });
  await section.click();
  await p.getByText(/核对时限已过。可以继续等待/).waitFor();
  await p.getByLabel("核对决定").selectOption("redo");
  await p
    .getByLabel(/核对依据（项目成员可见）/)
    .fill("已核查供应商记录，接受原请求可能迟到和新增费用。");
  const check = p.getByLabel(/我已核对重复执行/);
  assert.equal(await check.isChecked(), false);
  await check.check();
  await section
    .locator("..")
    .screenshot({
      path: process.env.VISUAL_EVIDENCE + "/submission-window.png",
    });
  const posted = p.waitForResponse(
    (r) =>
      r.url().endsWith("/calls/1/review") && r.request().method() === "POST",
  );
  await p.getByRole("button", { name: "记录核对决定" }).click();
  assert.equal((await posted).status(), 202);
} finally {
  await browser.close();
}

import assert from "node:assert/strict";
import { chromium } from "playwright";
const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN,
    viewport: { width: 390, height: 844 },
  });
  const mode = process.env.REMINDER_ACTION;
  await page.goto(mode === "review" ? "/?inbox=review" : "/");
  await page.getByLabel("邮箱").fill(process.env.REMINDER_EMAIL);
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.REMINDER_PASSWORD);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  if (mode !== "review") {
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByText("账号设置", { exact: true }).click();
    await page.getByText("飞书账号绑定", { exact: true }).click();
    if (mode === "enable") {
      await page.getByRole("button", { name: "开启审核单聊提醒" }).click();
      await page.getByRole("button", { name: "关闭审核单聊提醒" }).waitFor();
    } else if (mode === "retry") {
      const response = page.waitForResponse(
        (r) =>
          r.url().endsWith("/review-reminders/retry") &&
          r.request().method() === "POST",
      );
      await page.getByRole("button", { name: "重新提醒仍有效的事项" }).click();
      assert.equal((await response).status(), 202);
    } else {
      throw Error("Unknown review reminder test action");
    }
  } else {
    const drawer = page.getByRole("dialog");
    await drawer.getByText("提醒与协调", { exact: true }).click();
    await drawer
      .getByText(/已等待/)
      .first()
      .waitFor();
    assert.equal(
      await drawer
        .getByRole("button", { name: "我的待办" })
        .getAttribute("aria-pressed"),
      "true",
    );
    await drawer.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/review-reminders-mobile.png",
    });
    await drawer.getByRole("button", { name: "前往处理" }).click();
    await page.getByRole("button", { name: "审核此版本" }).click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).waitFor();
    await page.getByText(/^项目协作 ·/).click();
    await page.getByText("审核提醒节奏", { exact: true }).click();
    await page.getByLabel("处理时限（小时）", { exact: true }).fill("10");
    const policy = page
      .locator("form")
      .filter({ has: page.getByRole("button", { name: "保存审核节奏" }) });
    await policy.getByLabel("调整原因").fill("后续反馈使用新的节奏");
    const saved = page.waitForResponse(
      (r) =>
        r.request().method() === "PUT" && r.url().endsWith("/review-policy"),
    );
    await policy.getByRole("button", { name: "保存审核节奏" }).click();
    assert.equal((await saved).status(), 204);
    await policy
      .locator("..")
      .getByText("已保存，对新产生的待办生效。")
      .waitFor();
  }
} finally {
  await browser.close();
}

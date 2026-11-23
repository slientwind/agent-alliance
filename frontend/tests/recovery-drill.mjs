import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.DRILL_ORIGIN });
  await page.goto("/#" + process.env.DRILL_ALLOWED_PROJECT);
  await page.getByLabel("邮箱").fill("drill@sample.invalid");
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.DRILL_PASSWORD);
  const login = page.waitForResponse(
    (r) => r.url().endsWith("/api/login") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "登录", exact: true }).click();
  assert.equal((await login).status(), 200);
  await page.goto("/#" + process.env.DRILL_ALLOWED_PROJECT);
  const downloading = page.waitForEvent("download");
  await page.getByRole("link", { name: "下载交付包（ZIP）" }).click();
  const stream = await (await downloading).createReadStream();
  const hash = createHash("sha256");
  for await (const chunk of stream) hash.update(chunk);
  assert.equal(hash.digest("hex"), process.env.DRILL_PACKAGE_SHA256);
  await page.goto("/#" + process.env.DRILL_HELD_PROJECT);
  await page.getByText("等待核对分发范围", { exact: true }).first().waitFor();
  assert.equal(
    await page.getByRole("link", { name: "下载交付包（ZIP）" }).count(),
    0,
  );
  await page.screenshot({
    path: process.env.DRILL_EVIDENCE + "/restricted.png",
  });
} finally {
  await browser.close();
}

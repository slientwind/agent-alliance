import { chromium } from "playwright";
import assert from "node:assert/strict";
const browser = await chromium.launch();
try {
  const context = await browser.newContext({
    baseURL: process.env.SMOKE_ORIGIN,
    viewport: { width: 390, height: 844 },
  });
  const page = await context.newPage();
  const link = new URL(process.env.FEISHU_REVIEW_URL);
  const cardRoute = link.pathname + link.search + link.hash;
  const expected = link.searchParams.get("task");
  await page.goto(cardRoute);
  await page.getByLabel("邮箱").fill("review@sample.invalid");
  await page
    .getByLabel("密码", { exact: true })
    .fill(process.env.BROWSER_PASSWORD);
  await page.getByRole("button", { name: "登录", exact: true }).click();
  await page.waitForFunction((id) => document.activeElement?.id === id, expected);
  assert.equal(new URL(page.url()).searchParams.has("task"), false);
  assert.equal(
    (await (await context.request.get("/api/projects/" + process.env.FEISHU_PROJECT)).json()).artifacts[0].approved,
    false,
  );
  await page.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/feishu-review-card-focus.png",
  });
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByText("账号设置", { exact: true }).click();
  await page.getByText("飞书账号绑定", { exact: true }).click();
  await page.getByText("已绑定飞书身份", { exact: true }).waitFor();
  await page.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/feishu-identity.png",
  });
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  // The fixture uses a synthetic HTTPS public hostname. Open its actual path on this isolated instance.
  await page.goto("/#" + process.env.FEISHU_PROJECT);
  const thread = page.locator("details.feedback");
  await thread.getByText("反馈 · 1", { exact: true }).click();
  await thread.getByText("建议核对摘要", { exact: true }).waitFor();
  await thread.getByLabel("处理方式").selectOption("decline");
  await thread.getByLabel("处理依据").fill("已核对本次合成摘要并保留原稿");
  await thread.getByRole("button", { name: "记录决定" }).click();
  await thread.getByText("已说明不采纳", { exact: true }).waitFor();
  await page.getByRole("button", { name: "审核此版本" }).click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = page.getByRole("link", { name: /下载交付包/ });
  await download.waitFor();
  const rsp = await context.request.get(await download.getAttribute("href"));
  assert.equal(rsp.status(), 200);
  assert((await rsp.body()).subarray(0, 2).equals(Buffer.from("PK")));
  await page.goto(cardRoute);
  await page.getByRole("alert").filter({ hasText: "这条待办已更新" }).waitFor();
  assert.equal(await page.getByRole("button", { name: "审核此版本", exact: true }).count(), 0);

  const other = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN });
  const signIn = await other.request.post("/api/login", {
    data: { email: "feishu-scope@sample.invalid", password: process.env.FEISHU_OTHER_PASSWORD },
  });
  assert.ok(signIn.ok());
  const denied = other.waitForResponse((r) => r.url().endsWith("/api/projects/" + process.env.FEISHU_PROJECT));
  await other.goto(cardRoute);
  assert.equal((await denied).status(), 404);
  assert.equal(await other.getByText("Feishu review desk", { exact: true }).count(), 0);
} finally {
  await browser.close();
}

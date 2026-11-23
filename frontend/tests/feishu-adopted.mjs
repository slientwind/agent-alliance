import assert from "node:assert/strict";
import { chromium } from "playwright";

const { SMOKE_ORIGIN, BROWSER_PASSWORD, FEISHU_ADOPTED_PROJECT, VISUAL_EVIDENCE } = process.env;
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: SMOKE_ORIGIN, viewport: { width: 390, height: 844 } });
  const signIn = await page.request.post("/api/login", {
    data: { email: "review@sample.invalid", password: BROWSER_PASSWORD },
  });
  assert.ok(signIn.ok());
  await page.goto("/#" + FEISHU_ADOPTED_PROJECT);
  const receipt = page.getByRole("status").filter({ hasText: "已采纳回答" });
  await receipt.waitFor();
  assert.match(await receipt.innerText(), /Reviewer/);
  assert.equal(await page.getByRole("button", { name: "明确采纳这条回答" }).count(), 0);
  await page.screenshot({ path: VISUAL_EVIDENCE + "/feishu-adoption-web.png" });
} finally {
  await browser.close();
}

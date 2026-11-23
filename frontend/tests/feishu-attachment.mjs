import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const { SMOKE_ORIGIN, FEISHU_PROJECT, FEISHU_SOURCE_NAME, VISUAL_EVIDENCE } = process.env;
const evidence = process.env.FEISHU_EVIDENCE_PREFIX || "feishu-attachment";
const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: SMOKE_ORIGIN,
    viewport: { width: 390, height: 844 },
  });
  const login = await page.request.post("/api/login", {
    data: { email: "review@sample.invalid", password: process.env.BROWSER_PASSWORD },
  });
  assert.ok(login.ok());
  await page.goto("/#" + FEISHU_PROJECT);
  const path = "/api/projects/" + FEISHU_PROJECT;
  const state = await (await page.request.get(path)).json();
  assert.equal(state.stage, "review");
  const premature = await page.request.post(path + "/deliver", {
    data: { expected: state.revision },
  });
  assert.equal(premature.status(), 409);
  const source = page.getByRole("link", { name: "查看" + FEISHU_SOURCE_NAME + " v1 原件" }).last();
  await source.waitFor();
  await source.scrollIntoViewIfNeeded();
  const original = await page.request.get(await source.getAttribute("href"));
  assert.ok(original.ok());
  await writeFile(VISUAL_EVIDENCE + "/" + (process.env.FEISHU_EVIDENCE_PREFIX ? evidence + "-source.txt" : "feishu-source.txt"), await original.body());
  await page.screenshot({ path: VISUAL_EVIDENCE + "/" + evidence + "-review.png" });
  const review = page.getByRole("button", { name: "审核此版本", exact: true });
  await review.first().click();
  await review.first().click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = page.getByRole("link", { name: /下载交付包/ });
  await download.waitFor();
  const packageResponse = await page.request.get(await download.getAttribute("href"));
  assert.ok(packageResponse.ok());
  await writeFile(VISUAL_EVIDENCE + "/" + evidence + "-delivery.zip", await packageResponse.body());
} finally {
  await browser.close();
}

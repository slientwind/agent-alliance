import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const env = process.env;
const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: env.SMOKE_ORIGIN,
    viewport: { width: 1100, height: 1440 },
  });
  const login = await page.request.post("/api/login", {
    data: { email: "review@sample.invalid", password: env.BROWSER_PASSWORD },
  });
  assert.equal(login.status(), 200);
  await page.goto("/");
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByRole("button", { name: "企业管理", exact: true }).click();
  await page.getByText("维护模型与凭据", { exact: true }).click();
  await page
    .getByLabel("要维护的模型修订")
    .selectOption(env.SWAP_SOURCE + "/1");
  const section = page
    .locator("details")
    .filter({
      has: page.locator(":scope > summary", {
        hasText: "按批次替换助手的模型",
      }),
    })
    .last();
  await section.locator(":scope > summary").click();
  if (env.SWAP_ADOPT === "rollback") {
    const item = section.locator("li").filter({ hasText: "业务摘要助手 v2" });
    await item.locator(":scope > details > summary").click();
    await item.getByText("回退此助手的发布修订", { exact: true }).click();
    await item.getByLabel("回退依据").fill("完成对照后恢复原模型");
    await item.getByRole("button", { name: "确认恢复发布 v1" }).click();
    await item
      .getByText("业务摘要助手 v2 · 当前已回到原修订", { exact: true })
      .waitFor();
    await item.screenshot({
      path: env.VISUAL_EVIDENCE + "/replacement-rollback.png",
    });
  } else if (env.SWAP_ADOPT) {
    const item = section.locator("li").filter({ hasText: "业务摘要助手 v2" });
    await item.locator(":scope > details > summary").click();
    const suite = (version) =>
      item
        .locator("details")
        .filter({
          has: page.locator(":scope > summary", {
            hasText: "必要检查 · v" + version,
          }),
        })
        .last();
    await suite(1).locator(":scope > summary").click();
    await suite(1)
      .getByText("双人审核 · old · private", { exact: true })
      .waitFor();
    await suite(2).locator(":scope > summary").click();
    await suite(2)
      .getByRole("checkbox", { name: /授权以共享样例试运行/ })
      .check();
    await suite(2).getByRole("button", { name: "运行此检查" }).click();
    await suite(2)
      .getByText("双人审核 · new · team", { exact: true })
      .waitFor();
    await item.getByRole("button", { name: "发布已复验的 v2" }).click();
    await item.getByText("业务摘要助手 v2 · 已发布", { exact: true }).waitFor();
    await item.evaluate((el) => el.scrollIntoView({ block: "start" }));
    await item.screenshot({
      path: env.VISUAL_EVIDENCE + "/replacement-comparison.png",
    });
  } else {
    await section
      .getByLabel("目标模型修订")
      .selectOption(env.SWAP_TARGET + "/1/1");
    const locked = section.getByRole("checkbox", { name: /已有定制/ });
    assert.ok(await locked.isDisabled());
    await section.getByRole("checkbox", { name: /业务摘要助手/ }).check();
    await section.getByRole("checkbox", { name: /发布材料助手/ }).check();
    await section
      .getByLabel("替换依据与已核对的差异")
      .fill(
        "同一份业务样例逐项复验，团队已核对来源和目标的处理范围与单次费用。",
      );
    const save = section.getByRole("button", {
      name: "为所选助手创建替换草稿",
    });
    assert.ok(await save.isDisabled());
    await section
      .getByRole("checkbox", { name: /统一使用目标团队凭据/ })
      .check();
    const sourcePath =
      "/api/admin/model-resources/" + env.SWAP_SOURCE + "/revisions/1";
    const current = await (await page.request.get(sourcePath)).json();
    const concurrent = current.agents.find(
      (a) => a.name === "发布材料助手" && a.published,
    ).id;
    const lifecycle = "/api/admin/catalog/agents/" + concurrent;
    const setEnabled = async (action) =>
      assert.equal(
        (
          await page.request.post(lifecycle, {
            data: { action, reason: "并发变更演练" },
          })
        ).status(),
        204,
      );
    await setEnabled("disable");
    await save.click();
    await section.getByRole("button", { name: "移除已失效的选择" }).click();
    assert.equal(
      (await (await page.request.get(sourcePath)).json()).replacements.length,
      0,
    );
    await setEnabled("enable");
    await section.getByRole("button", { name: "刷新替换进度" }).click();
    await section.getByRole("checkbox", { name: /发布材料助手/ }).check();
    assert.ok(await save.isDisabled());
    await section
      .getByRole("checkbox", { name: /统一使用目标团队凭据/ })
      .check();
    const response = page.waitForResponse(
      (r) =>
        r.url().endsWith("/replacements") && r.request().method() === "POST",
    );
    await save.click();
    const saved = await response;
    assert.equal(saved.status(), 201, await saved.text());
    const payload = saved.request().postDataJSON();
    const key = await saved.request().headerValue("idempotency-key");
    await section.getByText(/业务摘要助手 v2 · 待复验/).waitFor();
    await section.evaluate((el) => el.scrollIntoView({ block: "start" }));
    await section.screenshot({
      path: env.VISUAL_EVIDENCE + "/model-replacement.png",
    });
    await writeFile(
      env.VISUAL_EVIDENCE + "/replacement-request.json",
      JSON.stringify({ id: (await saved.json()).id, payload, key }),
    );
    await page.setViewportSize({ width: 390, height: 844 });
    assert.ok(
      await page
        .getByRole("dialog")
        .evaluate((el) => el.scrollWidth <= el.clientWidth),
    );
  }
} finally {
  await browser.close();
}

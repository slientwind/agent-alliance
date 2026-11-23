import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const origin = process.env.SMOKE_ORIGIN;
const directory = process.env.VISUAL_EVIDENCE;
if (!origin || !directory)
  throw new Error("Set SMOKE_ORIGIN and VISUAL_EVIDENCE");
await mkdir(directory, { recursive: true });
const browser = await chromium.launch();
const evidence = [];
try {
  for (const width of [320, 390, 1440]) {
    const page = await browser.newPage({
      baseURL: origin,
      viewport: { width, height: 880 },
    });
    const agent = "合同摘要";
    let generation = 1,
      denied = false,
      writes = 0;
    const resource = (id, name, enabled) => ({
      id,
      team: "team",
      teamName: "测试团队",
      name,
      enabled,
      emergency: false,
      revisions: [
        {
          revision: 1,
          operation: "text",
          model: id,
          estimate: null,
          blocked: false,
          generation: id === "new" ? generation : 1,
          endpoint:
            "https://example.invalid/enterprise/" + "q".repeat(110) + "/api/v1",
        },
      ],
    });
    await page.route("**/api/**", async (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.startsWith("/api/admin/model-resources")) {
        if (route.request().method() !== "GET") {
          writes++;
          return route.fulfill({ status: 500, json: { error: "不应提交" } });
        }
        if (denied)
          return route.fulfill({
            status: 403,
            json: { error: "模型管理授权已收回" },
          });
        if (path === "/api/admin/model-resources")
          return route.fulfill({
            json: [
              resource("old", "原资源", true),
              resource("new", "目标资源", true),
            ],
          });
        return route.fulfill({
          json: {
            pinnedServices: 0,
            pinnedProjects: 0,
            defaultPlanner: false,
            enabled: true,
            generation: 1,
            blocked: false,
            lifecycle: 1,
            services: [],
            projects: [],
            candidate: null,
            replacements: [],
            agents: [
              {
                id: "a",
                name: agent,
                published: true,
                eligible: true,
                revision: 1,
                generation: 0,
                independent: false,
              },
            ],
          },
        });
      }
      const data = {
        "/api/instance": { initialized: true, name: "合成企业" },
        "/api/me": { id: "operator", name: "配置人", role: "admin" },
      };
      return route.fulfill({ json: data[path] ?? [] });
    });
    await page.goto("/");
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "企业管理", exact: true }).click();
    await page.getByText("维护模型与凭据", { exact: true }).click();
    await page.getByText("按批次替换助手的模型", { exact: true }).click();
    const dialog = page.getByRole("dialog");
    const target = dialog.getByLabel("目标模型修订");
    const check = dialog.getByRole("checkbox", {
      name: /统一使用目标团队凭据/,
    });
    const submit = dialog.getByRole("button", {
      name: "为所选助手创建替换草稿",
    });
    await target.selectOption("new/1/1");
    await dialog.getByRole("checkbox", { name: /合同摘要/ }).check();
    await dialog.getByLabel("替换依据与已核对的差异").fill("核对模型配置");
    await check.check();
    assert.ok(await submit.isEnabled());
    assert.ok(
      await dialog.evaluate((el) => el.scrollWidth <= el.clientWidth),
      "long endpoint overflow at " + width,
    );
    generation = 2;
    await dialog.getByRole("button", { name: "刷新替换进度" }).click();
    await target
      .locator('option[value="new/1/2"]')
      .waitFor({ state: "attached" });
    assert.equal(await target.inputValue(), "");
    assert.ok(await submit.isDisabled());
    await target.selectOption("new/1/2");
    assert.ok(!(await check.isChecked()) && (await submit.isDisabled()));
    denied = true;
    await dialog.getByRole("button", { name: "刷新替换进度" }).click();
    await dialog.getByText("暂无可维护的模型。", { exact: true }).waitFor();
    assert.equal(await dialog.getByLabel("要维护的模型修订").count(), 0);
    assert.equal(
      await dialog.getByText(/example.invalid\/enterprise/).count(),
      0,
    );
    assert.equal(writes, 0);
    evidence.push({
      width,
      rotatedTargetRequiredNewConsent: true,
      withdrawnScopeRemoved: true,
      submitted: 0,
    });
    await page.close();
  }
  await writeFile(
    directory + "/replacement-access-mock.json",
    JSON.stringify(
      { source: "Chromium, synthetic HTTP responses", evidence },
      null,
      2,
    ) + "\n",
  );
} finally {
  await browser.close();
}

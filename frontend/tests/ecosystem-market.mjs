import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179" });
  const approved = {
    id: "approved", name: "报表助手 Skill", kind: "skill", source: "企业仓库", version: "v1", license: "MIT",
    package: "package", reviewStatus: "clean", enabled: false, bindingRevision: 0, licenseTag: "permissive",
    homepage: "https://example.test/approved", activity: "recent",
  };
  const discovered = {
    id: "candidate", name: "公告校对 Skill", kind: "skill", source: "目录", version: "v4", reviewStatus: "review_required",
    license: "commercial_restrictions", licenseTag: "review",
  };
  const mutations = [];
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/ecosystem/bindings/package") {
      const input = request.postDataJSON();
      mutations.push(input);
      approved.bindingRevision++;
      approved.enabled = input.enabled;
      return route.fulfill({ json: { enabled: approved.enabled, revision: approved.bindingRevision } });
    }
    if (path === "/api/ecosystem/catalog/candidate/request") {
      discovered.request = "pending";
      return route.fulfill({ status: 202, json: { id: "application" } });
    }
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "目录用户", role: "member", enabled: true },
      "/api/teams": [{ id: "team", name: "运营团队", joined: true, default: true }],
      "/api/ecosystem/catalog": [approved, discovered],
      "/api/ecosystem/scan-status": [{ id: "source", name: "公开目录", status: "completed", finishedAt: "2026-11-25T08:00:00Z" }],
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/agents");
  await page.getByRole("heading", { name: "团队能力库" }).waitFor();
  await page.getByRole("link", { name: "生态发现市场" }).click();
  await page.waitForURL("**/agents?view=ecosystem");
  await page.getByRole("heading", { name: "生态发现市场" }).waitFor();
  assert.equal(await page.getByRole("link", { name: "生态发现市场" }).getAttribute("aria-current"), "page");
  assert.equal(await page.getByRole("link", { name: "构建助手" }).getAttribute("href"), "/agents/studio");
  assert.equal(await page.getByRole("link", { name: "接入外部 Agent" }).getAttribute("href"), "/agents/connect");
  await page.reload();
  const scans = page.getByRole("region", { name: "定期扫描状态" });
  await scans.getByText("公开目录 · 扫描完成").waitFor();
  assert.equal(await scans.locator("time").getAttribute("datetime"), "2026-11-25T08:00:00Z");
  const selection = page.getByRole("article", { name: "报表助手 Skill v1" });
  assert.equal(await selection.getByText("MIT", { exact: true }).getAttribute("data-tone"), "clean");
  assert.equal(await selection.getByRole("link", { name: "查看上游来源" }).getAttribute("href"), "https://example.test/approved");
  await selection.getByRole("button", { name: "加载到我的助手" }).click();
  await page.getByText("报表助手 Skill 已加载到我的助手").waitFor();
  await selection.getByRole("button", { name: "卸载" }).click();
  await page.getByText("已卸载 报表助手 Skill").waitFor();
  await selection.getByRole("button", { name: "加载到我的助手" }).waitFor();
  assert.deepEqual(mutations, [{ expected: 0, enabled: true }, { expected: 1, enabled: false }]);
  const next = page.getByRole("article", { name: "公告校对 Skill v4" });
  await next.getByText("待人工复核").waitFor();
  assert.equal(await next.getByText("含商业使用限制").getAttribute("data-tone"), "review_required");
  await next.getByRole("button", { name: "申请管理员准入" }).click();
  await next.getByText("准入申请审核中").waitFor();
  assert.equal(await next.getByRole("button", { name: "申请管理员准入" }).count(), 0);
} finally {
  await browser.close();
}

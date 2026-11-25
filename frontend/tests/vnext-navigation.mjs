import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  for (const [width, height] of [[1440, 900], [390, 844]]) {
    const page = await browser.newPage({
      baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173",
      viewport: { width, height },
    });
    const id = "2b40fe15-8725-45cf-9576-5661de125cb5";
    const project = {
      id, title: "采购报价核对", stage: "brief", revision: 1, owner: "member", reviewer: "member", team: "team", planVersion: 0,
      messages: [{ seq: 1, kind: "user", body: "整理报价表" }], artifacts: [], participants: [], handoffs: [],
      sharedAgents: [], deliveries: [], distributionCases: [], budget: { warning: false, pending: 0 }, costSummary: {},
      run: null, suggestion: null, plan: null,
    };
    await page.route("**/api/**", (route) => {
      const path = new URL(route.request().url()).pathname;
      if (path.endsWith("/session-events") || path.endsWith("/events")) {
        return route.fulfill({ contentType: "text/event-stream", body: ": ready\n\n" });
      }
      const data = {
        "/api/instance": { initialized: true, name: "示例企业" },
        "/api/me": { id: "member", name: "林予", role: "admin", enabled: true },
        "/api/projects": [project],
        ["/api/projects/" + id]: project,
        "/api/teams": [{ id: "team", name: "团队", default: true, joined: true }],
      };
      return route.fulfill({ json: data[path] ?? [] });
    });
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    page.on("console", (message) => { if (message.type() === "error") errors.push(message.text()); });
    const checkWidth = async () => assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.goto("/");
    await page.waitForURL("**/chat");
    await page.getByRole("textbox", { name: "消息" }).waitFor();
    await checkWidth();
    await page.keyboard.press("Meta+k");
    const search = page.getByRole("combobox", { name: "搜索对话、Agent 和操作" });
    await search.waitFor();
    assert.equal(await search.evaluate(input => input === document.activeElement), true);
    await search.fill("报价");
    await page.getByRole("option", { name: "采购报价核对 历史对话" }).waitFor();
    await search.press("Enter");
    await page.waitForURL("**/chat/" + id);
    await page.getByText("整理报价表", { exact: true }).waitFor();
    await page.keyboard.press("Meta+n");
    await page.waitForURL("**/chat");
    const rail = page.locator(".workspace-shell");
    const collapseBefore = await rail.getAttribute("data-collapsed");
    await page.keyboard.press("Meta+Backslash");
    assert.notEqual(await rail.getAttribute("data-collapsed"), collapseBefore);
    await page.keyboard.press("Meta+Backslash");
    assert.equal(await rail.getAttribute("data-collapsed"), collapseBefore);
    const searchButton = page.getByRole("button", { name: "搜索对话和操作" });
    await searchButton.click();
    await search.fill("没有对应的内容");
    await page.getByText("没有找到匹配的结果。").waitFor();
    await page.keyboard.press("Escape");
    await search.waitFor({ state: "detached" });
    assert.equal(await searchButton.evaluate(button => button === document.activeElement), true);
    if (width === 390) {
      await page.getByRole("button", { name: "展开侧栏" }).click();
      await page.getByRole("button", { name: "折叠侧栏" }).waitFor();
      await checkWidth();
      await page.getByRole("button", { name: "折叠侧栏" }).click();
    }
    await page.getByRole("button", { name: "切换深色主题" }).click();
    assert.equal(await page.locator("html").getAttribute("data-theme"), "dark");
    await page.getByRole("button", { name: "Agent 广场", exact: true }).click();
    await page.waitForURL("**/agents");
    await page.getByRole("heading", { name: "Agent 广场" }).waitFor();
    assert.equal(await page.locator("dialog[open]").count(), 0);
    await checkWidth();
    await page.getByRole("button", { name: "待办与审核", exact: true }).click();
    await page.waitForURL("**/inbox");
    await page.getByRole("heading", { name: "待办与审核" }).waitFor();
    await page.getByRole("button", { name: "企业管理", exact: true }).click();
    await page.waitForURL("**/admin");
    assert.equal(await page.locator("dialog[open]").count(), 0);
    await checkWidth();
    await page.goto("/#" + id);
    await page.waitForURL("**/chat/" + id);
    await page.getByText("整理报价表", { exact: true }).waitFor();
    await checkWidth();
    await page.reload();
    await page.getByText("整理报价表", { exact: true }).waitFor();
    assert.equal(await page.locator("html").getAttribute("data-theme"), "dark");
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log("Workspace shortcuts, navigation, legacy and direct conversation links, and theme persistence passed at desktop and mobile widths.");
} finally {
  await browser.close();
}

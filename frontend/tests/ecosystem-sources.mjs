import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 390, height: 840 },
  });
  page.setDefaultTimeout(9000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  let source;
  const base = "/api/admin/ecosystem/sources";
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === base) {
      if (request.method() === "POST") {
        const input = request.postDataJSON();
        assert.equal(input.kind, "mcp_registry");
        assert.equal(input.endpoint, "https://registry.modelcontextprotocol.io/v0.1/servers");
        assert.equal(input.enabled, true);
        source = { ...input, id: "fixture", revision: 1, schedule: "ready", hasCredential: false };
        return route.fulfill({ status: 201, json: { id: source.id, revision: 1 } });
      }
      return route.fulfill({ json: source ? [source] : [] });
    }
    if (path === base + "/fixture/scan") {
      assert.ok(request.headers()["idempotency-key"]);
      assert.deepEqual(request.postDataJSON(), { expected: 1 });
      source.lastScan = { status: "completed", found: 11, added: 3 };
      return route.fulfill({ status: 202, json: { id: "scan" } });
    }
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "admin", name: "测试管理员", role: "admin", enabled: true },
      "/api/teams": [{ id: "team", name: "测试团队", joined: true, default: true }],
      "/api/admin/integrations/search": { provider: "tavily", endpoint: "https://api.tavily.com/search",
        revision: 1, enabled: false, hasCredential: false, health: "unchecked" },
      "/api/admin/integrations/email": { host: "", port: 465, security: "tls", sender: "", username: "",
        revision: 1, enabled: false, hasPassword: false, health: "unchecked" },
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/admin/integrations");
  const section = page.getByRole("region", { name: "生态扫描源" });
  await section.getByRole("button", { name: "添加扫描源" }).click();
  await section.getByLabel("扫描源名称").fill("测试官方目录");
  await section.getByLabel("启用定时扫描").check();
  await section.getByRole("button", { name: "保存扫描源" }).click();
  await section.getByRole("button", { name: "立即扫描" }).click();
  await section.getByText("已提交扫描，可刷新查看进度。").waitFor();
  await section.getByRole("button", { name: "刷新扫描状态" }).click();
  await section.getByText("扫描完成 · 找到 11 项，新增 3 项").waitFor();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  assert.deepEqual(errors, []);
  console.log("The ecosystem sources panel saved a feed, queued a scan, and displayed its status.");
} finally {
  await browser.close();
}

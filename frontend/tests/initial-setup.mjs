import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 390, height: 844 },
  });
  let signedIn = false;
  let firstKey;
  let configured = false;
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/initialize") {
      signedIn = true;
      return route.fulfill({ status: 204 });
    }
    if (path === "/api/instance") return route.fulfill({ json: { initialized: signedIn, name: "测试企业" } });
    if (path === "/api/me") return signedIn
      ? route.fulfill({ json: { id: "owner", name: "管理员", enabled: true, role: "admin" } })
      : route.fulfill({ status: 401, json: {} });
    if (path === "/api/admin/onboarding" && request.method() === "GET") {
      return route.fulfill({ json: { complete: false, hasProvider: false, sources: [{ id: "mcp", name: "MCP 官方目录" }] } });
    }
    if (path === "/api/admin/onboarding" && request.method() === "POST") {
      const key = request.headers()["idempotency-key"];
      const input = request.postDataJSON();
      assert.match(key, /^[a-f0-9-]{36}$/);
      assert.deepEqual(input.sources, ["mcp"]);
      assert.equal(input.inviteCount, 2);
      assert.equal(input.provider.Models[0].InputPerMillion, "1000029");
      assert.equal(input.provider.Models[0].Capabilities.join(" "), "text tool_call");
      if (!firstKey) {
        firstKey = key;
        return route.fulfill({ status: 503, json: { error: "工作空间暂未保存，请重试" } });
      }
      assert.equal(key, firstKey);
      configured = true;
      return route.fulfill({ status: 201, json: {
        tiers: ["default", "fast"], invitations: ["fixture-invite-1", "fixture-invite-2"], expires: "2026-11-27T12:00:00Z",
      } });
    }
    if (path === "/api/general-tiers") return route.fulfill({ json: configured ? ["default", "fast"] : [] });
    return route.fulfill({ json: [] });
  });

  await page.goto("/");
  await page.getByLabel("企业名称").fill("测试企业");
  await page.getByLabel("安装凭证").fill("fixture-bootstrap");
  await page.getByLabel("姓名").fill("管理员");
  await page.getByLabel("邮箱").fill("owner@example.test");
  await page.getByLabel("密码", { exact: true }).fill("fixture-password");
  await page.locator("form.card button.primary").first().click();
  const setup = page.getByRole("dialog", { name: "首次设置" });
  await setup.getByLabel("供应商名称").fill("测试供应商");
  await setup.getByLabel("HTTPS 服务地址").fill("https://models.example.test/v1");
  await setup.getByLabel("访问凭据").fill("fixture-provider-secret");
  await setup.getByLabel("模型名称").fill("example-model");
  await setup.getByLabel("每百万输入 token（元）").fill("1.000029");
  await setup.getByLabel("每百万输出 token（元）").fill("2.000043");
  await setup.getByLabel("每百万缓存 token（元）").fill("0");
  await setup.getByLabel("每张图片（元）").fill("0");
  await setup.getByRole("checkbox", { name: "MCP 官方目录" }).check();
  await setup.getByLabel("生成团队邀请凭证").fill("2");
  await setup.getByRole("button", { name: "完成设置" }).click();
  await setup.getByRole("alert").getByText("工作空间暂未保存，请重试").waitFor();
  await setup.getByRole("button", { name: "完成设置" }).click();
  await setup.getByText("已启用 通用、快速。", { exact: true }).waitFor();
  assert.equal(await setup.getByLabel("邀请凭证 2").inputValue(), "fixture-invite-2");
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await setup.getByRole("button", { name: "开始使用" }).click();
  await setup.waitFor({ state: "detached" });
} finally {
  await browser.close();
}

import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 390, height: 840 },
  });
  page.setDefaultTimeout(8000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const now = "2026-11-25T08:00:00Z";
  const run = { runId: "run", title: "合规核对", teamId: "team", projectId: "project", status: "running", createdAt: now };
  const agent = { teamId: "team", agentId: "agent", name: "报表助手", source: "model", calls: 8, successRate: 0.875, effectiveRate: 0.8,
    approvalRate: null, p50Ms: 250, p95Ms: 600, tokens: 123, costMicroCny: "1234567", undisclosed: 1 };
  const range = { kpi: { calls: 8, successfulCalls: 7, tokens: 123, cachedTokens: 10, costMicroCny: "1234567", undisclosed: 1 },
    quality: { lightweight: { total: 5, met: 4 }, collaborative: { total: 2, met: 1 } },
    funnel: { submitted: 7, completed: 6, outputPassed: 5, accepted: 5 },
    taskTrend: [{ day: "2026-11-25", tasks: 7, completed: 6, lightweight: 5, effective: 4, collaborative: 2, approved: 1 }],
    usageTrend: [{ day: "2026-11-25", tokens: 123, costMicroCny: "1234567" }] };
  const modelCaps = ["text", "reasoning", "vision", "image", "embedding"];
  const routes = [
    { modelId: "primary", model: "标准", provider: "测试", priority: 0, weight: 1, health: "healthy" },
    { modelId: "fast", model: "速答", provider: "测试", priority: 2, weight: 3, health: "healthy" },
  ];
  const tiers = ["default", "fast", "reasoning", "vision", "image", "embedding"].map(tier => ({ tier, revision: 2, routes: tier === "default" ? routes : [] }));
  const prices = [{ id: "price", modelId: "primary", providerId: "vendor", model: "标准", effectiveAt: now,
    inputPerMillionMicroCny: "1234567", outputPerMillionMicroCny: "0", cachedPerMillionMicroCny: "10", perImageMicroCny: "1" }];
  let outbound = [];
  let role = "admin";
  const mutations = [];
  const providers = [{ id: "vendor", name: "测试", kind: "openai", endpoint: "https://models.example.test/v1", revision: 1, enabled: true, models: [
    { id: "primary", name: "标准", enabled: true, capabilities: modelCaps }, { id: "fast", name: "速答", enabled: true, capabilities: ["text"] },
  ] }];
  let submittedProvider;
  await page.route("**/api/**", route => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    if (request.method() === "PUT") {
      const input = request.postDataJSON();
      mutations.push(input);
      if (path.endsWith("/tiers/default")) {
        tiers[0].revision++;
        tiers[0].routes = input.routes.map(item => ({ ...routes.find(route => route.modelId === item.modelId), ...item }));
        return route.fulfill({ json: { tier: "default", revision: tiers[0].revision } });
      }
      if (path.endsWith("/outbound-hosts")) {
        outbound = [{ ...input, revision: 1 }];
        return route.fulfill({ json: { revision: 1 } });
      }
    }
    if (request.method() === "POST" && path === "/api/admin/models/providers") {
      submittedProvider = request.postDataJSON();
      providers.push({ id: "second", name: submittedProvider.name, kind: "openai", endpoint: submittedProvider.endpoint, revision: 1,
        hasKey: true, enabled: true, models: [] });
      return route.fulfill({ status: 201, json: { id: "second", revision: 1 } });
    }
    if (request.method() === "POST" && path === "/api/admin/models/pricing") {
      const input = request.postDataJSON();
      mutations.push(input);
      prices.unshift({ ...prices[0], ...input, id: "saved-price" });
      return route.fulfill({ status: 201, json: { id: "saved-price" } });
    }
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "admin", name: "监控管理员", role, enabled: true },
      "/api/teams": [{ id: "team", name: "财务", default: true, joined: true }],
      "/api/admin/monitor/overview": { overview: range },
      "/api/admin/monitor/agents": { rows: [agent] },
      "/api/admin/monitor/agents/agent/runs": [run],
      "/api/admin/monitor/queue": { counts: { running: 1 }, rows: [run] },
      "/api/admin/monitor/alerts": { rules: [{ id: "rule", name: "用量提醒", teamId: "team", enabled: true }],
        deliveries: [{ id: "alert", ruleId: "rule", message: "该团队已用本月额度的 80%", state: "sent", createdAt: now }] },
      "/api/admin/monitor/trace/run": { trace: { status: "completed", start: now, end: "2026-11-25T08:00:02Z", traceId: "test-id", spans: null,
        gatewaySpans: [{ turn: 1, spanId: "model", model: "标准", type: "model", status: "success", start: now, end: "2026-11-25T08:00:01Z" }],
        actionSpans: [{ turn: 1, spanId: "tool", name: "create_xlsx", type: "tool", status: "completed", start: "2026-11-25T08:00:01Z", end: "2026-11-25T08:00:02Z" }],
        sandboxSpans: [{ spanId: "sandbox", type: "sandbox", status: "completed", start: "2026-11-25T08:00:02Z", end: "2026-11-25T08:00:02Z" }] } },
      "/api/admin/models/tiers": tiers,
      "/api/admin/models/providers": providers,
      "/api/admin/models/pricing": prices,
      "/api/admin/integrations/search": { provider: "tavily", endpoint: "", enabled: false, hasCredential: false, revision: 1, health: "unchecked" },
      "/api/admin/integrations/email": { host: "", port: 465, security: "tls", sender: "", username: "", enabled: false, hasPassword: false,
        revision: 1, health: "unchecked" },
      "/api/admin/integrations/outbound-hosts": outbound,
    };
    return route.fulfill({ json: data[path] ?? [] });
  });

  await page.goto("/admin/monitor");
  await page.getByRole("heading", { name: "运行监控", exact: true }).waitFor();
  await page.getByRole("region", { name: "任务质量漏斗" }).getByText("成果检查通过", { exact: true }).waitFor();
  await page.getByRole("region", { name: "执行质量趋势" }).getByRole("img", { name: "轻量任务有效率" }).waitFor();
  await page.getByRole("region", { name: "用量与费用趋势" }).getByRole("img", { name: "已计费用" }).waitFor();
  await page.getByText("该团队已用本月额度的 80%").waitFor();
  await Promise.all([
    page.waitForRequest(request => new URL(request.url()).searchParams.get("team") === "team"
      && new URL(request.url()).pathname === "/api/admin/monitor/overview"),
    page.getByRole("combobox", { name: /^团队/ }).selectOption("team"),
  ]);
  await Promise.all([
    page.waitForRequest(request => new URL(request.url()).searchParams.get("sort") === "p95"),
    page.getByRole("combobox", { name: /^排序/ }).selectOption("p95"),
  ]);
  await page.getByRole("button", { name: "报表助手" }).click();
  const runs = page.getByRole("region", { name: "报表助手 运行记录" });
  await runs.getByRole("button", { name: "查看运行详情" }).click();
  const drawer = page.getByRole("dialog", { name: "运行时间线" });
  await drawer.getByText("模型 · 标准").waitFor();
  await drawer.getByText("工具 · create_xlsx").waitFor();
  await drawer.getByText("沙箱产物导入", { exact: true }).waitFor();
  await drawer.getByRole("button", { name: "关闭" }).click();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));

  await page.getByRole("button", { name: "模型网关", exact: true }).click();
  await page.waitForURL("**/admin/gateway");
  const providerPanel = page.getByRole("region", { name: "全局模型供应商" });
  await providerPanel.getByText("添加模型供应商", { exact: true }).click();
  const newProvider = providerPanel.locator("details").filter({ has: page.getByText("添加模型供应商", { exact: true }) });
  await newProvider.getByLabel("供应商名称").fill("企业统一供应商");
  await newProvider.getByLabel("服务地址").fill("https://new.example.test/v1");
  await newProvider.getByLabel("访问凭据", { exact: true }).fill("fixture-provider-key");
  await newProvider.getByRole("button", { name: "保存供应商" }).click();
  await providerPanel.getByText("企业统一供应商 · 已启用", { exact: true }).waitFor();
  assert.equal(submittedProvider.key, "fixture-provider-key");
  assert.equal(await newProvider.getByLabel("访问凭据", { exact: true }).inputValue(), "");
  const defaultTier = page.getByRole("form", { name: "通用档位" });
  await defaultTier.getByRole("button", { name: "设为主模型" }).click();
  await defaultTier.getByRole("button", { name: "保存档位" }).click();
  await page.getByText("通用档位已保存").waitFor();
  assert.equal(await page.locator("form.admin-tier").count(), 6);
  assert.deepEqual(mutations[0], { expectedRevision: 2, routes: [{ modelId: "fast", priority: 0, weight: 3 }, { modelId: "primary", priority: 2, weight: 1 }] });
  const pricing = page.getByRole("region", { name: "模型单价" });
  await pricing.getByText("¥1.234567", { exact: true }).waitFor();
  await pricing.getByRole("combobox", { name: /^模型/ }).selectOption("primary");
  await pricing.getByText("新增生效单价").click();
  await pricing.getByLabel("每百万输入 token（元）").fill("2.000009");
  await pricing.getByRole("button", { name: "保存单价" }).click();
  await pricing.getByText("¥2.000009", { exact: true }).waitFor();
  assert.equal(mutations[1].inputPerMillionMicroCny, "2000009");
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));

  await page.getByRole("button", { name: "企业服务", exact: true }).click();
  const hosts = page.getByRole("region", { name: "域名访问许可" });
  await hosts.getByRole("button", { name: "添加域名许可" }).click();
  await hosts.getByLabel("域名", { exact: true }).fill("registry.example.test");
  await hosts.getByRole("button", { name: "保存域名许可" }).click();
  await hosts.getByText("registry.example.test:443 · 已启用").waitFor();
  assert.deepEqual(mutations[2], { host: "registry.example.test", port: 443, enabled: true, expectedRevision: 0, trustedNetworks: [] });
  role = "member";
  await page.goto("/admin/monitor");
  await page.getByText("需要企业管理员权限").waitFor();
  assert.equal(await page.getByRole("navigation", { name: "企业管理导航" }).count(), 0);
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}

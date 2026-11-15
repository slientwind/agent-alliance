import { test, expect } from "@playwright/test";
import { fileURLToPath } from "node:url";
const artifact = fileURLToPath(new URL("../../artifacts/", import.meta.url));
const EMAIL = "browser-" + Date.now() + "@example.test";
const PASSWORD = "browser-test-password-123";
const prefix = "验收" + Date.now().toString().slice(-6);
const names = {
  model: prefix + "模型",
  agent: prefix + "分析师",
  external: prefix + "外部采集",
  service: prefix + "协作服务",
  compatible: prefix + "兼容模型",
  compatAgent: prefix + "兼容助手",
  a2a: prefix + "A2A",
};
let page, context;
const errors = [];
async function nav(name) {
  await page.locator("nav").getByRole("button", { name, exact: true }).click();
}
async function call(path, method = "GET", body) {
  return page.evaluate(
    async ({ path, method, body }) => {
      const workspace = localStorage.getItem("alliance-workspace") || "";
      const me = await fetch("/api/v1/auth/me", {
        headers: { "X-Workspace-ID": workspace },
      });
      const identity = await me.json();
      const r = await fetch("/api/v1/" + path, {
        method,
        headers: {
          "Content-Type": "application/json",
          "X-Workspace-ID": workspace,
          "X-CSRF-Token": identity.user?.csrfToken || "",
        },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      return { status: r.status, body: await r.json() };
    },
    { path, method, body },
  );
}
async function agentCard(name) {
  return page
    .locator(".agent-card")
    .filter({ has: page.getByRole("heading", { name, exact: true }) });
}
async function serviceCard() {
  return page.locator(".service-card").filter({
    has: page.getByRole("heading", { name: names.service, exact: true }),
  });
}
async function runAgent(name, input) {
  await nav("Agent 注册中心");
  const card = await agentCard(name);
  await card.getByRole("button", { name: "试运行", exact: true }).click();
  await page.getByLabel("任务输入").fill(input);
  await page.getByRole("button", { name: "开始运行", exact: true }).click();
  await expect(page.getByText("TRACE EXPLORER", { exact: true })).toBeVisible();
}
async function saveAgent() {
  await page.getByRole("button", { name: "保存 Agent", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
}
test.describe.serial("产品全链路验收 · 真实 Go API + HTTP mock", () => {
  test.beforeAll(async ({ browser }) => {
    context = await browser.newContext({
      viewport: { width: 1440, height: 1040 },
    });
    page = await context.newPage();
    page.on("pageerror", (e) => errors.push(e.message));
  });
  test.afterAll(async () => {
    await context.close();
  });
  test("B01 账号登录，不要求模型 API key", async () => {
    await page.goto("/");
    await expect(page.getByLabel("API key", { exact: true })).toHaveCount(0);
    await page.getByLabel("邮箱", { exact: true }).fill(EMAIL);
    await page.getByLabel("账号密码", { exact: true }).fill("wrong-password");
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("邮箱或密码不正确");
    await page
      .getByRole("button", { name: "没有账号？创建工作区", exact: true })
      .click();
    await page.getByLabel("姓名", { exact: true }).fill("业务验收用户");
    await page
      .getByLabel("工作区名称", { exact: true })
      .fill(prefix + "工作区");
    await page.getByLabel("账号密码", { exact: true }).fill(PASSWORD);
    await page.getByRole("button", { name: "注册并进入", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "工作台", exact: true }),
    ).toBeVisible();
    expect((await call("metrics")).status).toBe(200);
  });
  test("B02 工作台指标与最近运行，截图留证", async () => {
    await expect(page.locator(".metrics")).toContainText("已注册 Agent");
    await expect(page.getByText("暂无运行记录", { exact: true })).toBeVisible();
    await page.screenshot({ path: artifact + "dashboard.png", fullPage: true });
  });
  test("B03 浏览器创建 Mock 模型", async () => {
    await nav("模型配置");
    await page.getByRole("button", { name: "添加模型", exact: true }).click();
    await page.getByLabel("配置名称", { exact: true }).fill(names.model);
    await page.getByRole("button", { name: "保存模型", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: names.model, exact: true }),
    ).toBeVisible();
  });
  test("B04 自然语言生成、审核并注册托管 Agent", async () => {
    await nav("Agent 注册中心");
    await page.getByRole("button", { name: "注册 Agent", exact: true }).click();
    await page.getByLabel("Agent 名称", { exact: true }).fill(names.agent);
    await page
      .getByLabel("职责描述", { exact: true })
      .fill("从客户反馈识别痛点，输出三条可执行建议。");
    await page
      .getByLabel("运行模型", { exact: true })
      .selectOption({ label: names.model + " · mock-v1" });
    await page.getByRole("button", { name: "根据描述生成" }).click();
    await expect(page.getByLabel("System prompt", { exact: true })).toHaveValue(
      /识别痛点/,
    );
    await expect(
      page.getByText("已生成 Mock 模板草稿，请审核后保存。"),
    ).toBeVisible();
    await saveAgent();
    await expect(
      page.getByRole("heading", { name: names.agent, exact: true }),
    ).toBeVisible();
  });
  test("B05 无效 Agent Card 校验与外部注册", async () => {
    await page.getByRole("button", { name: "注册 Agent", exact: true }).click();
    await page.getByRole("button", { name: "Agent Card 接入" }).click();
    await page.getByLabel("Agent Card JSON").fill('{"name":"invalid"}');
    await page.getByRole("button", { name: "保存 Agent", exact: true }).click();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "Agent Card 缺少",
    );
    await page.getByLabel("Agent Card JSON").fill(
      JSON.stringify({
        name: names.external,
        description: "通过 HTTP 接入的外部采集 Agent",
        url: "http://127.0.0.1:8090/agent",
        version: "1.0.0",
        protocol: "http-json",
        skills: [{ id: "collect", name: "采集", description: "整理输入" }],
      }),
    );
    await saveAgent();
    await expect(
      page.getByRole("heading", { name: names.external, exact: true }),
    ).toBeVisible();
  });
  test("B06 搜索与类型筛选、空结果", async () => {
    await page.getByLabel("搜索", { exact: true }).fill(names.agent);
    await expect(page.locator(".agent-card")).toHaveCount(1);
    await page.getByRole("button", { name: "外部接入", exact: true }).click();
    await expect(
      page.getByText("没有匹配的 Agent", { exact: true }),
    ).toBeVisible();
    await page.getByLabel("搜索", { exact: true }).fill("");
    await expect(
      page.getByRole("heading", { name: names.external, exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "全部 Agent", exact: true }).click();
  });
  test("B07 编辑 prompt、独立执行、节点输入输出和用量", async () => {
    const card = await agentCard(names.agent);
    await card.getByRole("button", { name: "配置", exact: true }).click();
    await page
      .getByLabel("System prompt", { exact: true })
      .fill("你是验收分析师，输出清晰的优先级建议。版本二。");
    await saveAgent();
    await runAgent(names.agent, "请分析客户对导出功能的需求");
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
    await page.locator(".trace-tree .child").click();
    await expect(page.locator(".trace-detail")).toContainText("版本二");
    await expect(page.locator(".trace-detail")).toContainText("Mock 估算");
    await page.screenshot({ path: artifact + "trace.png", fullPage: true });
  });
  test("B08 外部 Agent 通过真实 HTTP 链路运行", async () => {
    await runAgent(names.external, "采集客户反馈");
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
    await expect(page.locator(".trace-detail pre").nth(1)).toContainText(
      "外部采集 Agent",
    );
    await page.locator(".trace-tree .child").click();
    await expect(page.locator(".trace-meta")).toContainText("未知");
  });
  test("B09 停用阻止调用，恢复启用", async () => {
    await nav("Agent 注册中心");
    let card = await agentCard(names.agent);
    await card.getByRole("button", { name: "配置", exact: true }).click();
    await page.getByLabel("运行状态", { exact: true }).selectOption("disabled");
    await saveAgent();
    await expect(
      card.getByRole("button", { name: "试运行", exact: true }),
    ).toBeDisabled();
    const a = (await call("agents")).body.find((a) => a.name === names.agent);
    expect(
      (await call("agents/" + a.id + "/invoke", "POST", { input: "x" })).status,
    ).toBe(409);
    await card.getByRole("button", { name: "配置", exact: true }).click();
    await page.getByLabel("运行状态", { exact: true }).selectOption("active");
    await saveAgent();
  });
  test("B10 编排循环校验、保存草稿、发布并调用", async () => {
    await nav("编排与服务");
    await page.getByRole("button", { name: "创建服务", exact: true }).click();
    await page.getByLabel("服务名称", { exact: true }).fill(names.service);
    await page
      .getByLabel("服务描述", { exact: true })
      .fill("采集 → 分析，端到端验收");
    await page
      .getByLabel("选择 Agent", { exact: true })
      .selectOption({ label: names.external });
    await page.getByRole("button", { name: "添加 Agent 节点" }).click();
    await page
      .getByLabel("选择 Agent", { exact: true })
      .nth(1)
      .selectOption({ label: names.agent });
    await page
      .getByLabel("依赖节点 ID", { exact: true })
      .first()
      .fill("step_2");
    await page.getByRole("button", { name: "保存编排", exact: true }).click();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "循环依赖",
    );
    await page.getByLabel("依赖节点 ID", { exact: true }).first().fill("");
    await page.getByRole("button", { name: "保存编排", exact: true }).click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const card = await serviceCard();
    await expect(
      card.getByRole("button", { name: "运行", exact: true }),
    ).toBeDisabled();
    await card.getByRole("button", { name: "发布", exact: true }).click();
    await expect(card).toContainText("已发布 v1");
    await card.getByRole("button", { name: "运行", exact: true }).click();
    await page.getByRole("button", { name: "开始运行", exact: true }).click();
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
    await expect(page.locator(".trace-tree .child")).toHaveCount(2);
    await page.screenshot({
      path: artifact + "workflow-trace.png",
      fullPage: true,
    });
  });
  test("B11 发布快照隔离草稿修改，重新发布更新版本", async () => {
    await nav("编排与服务");
    const card = await serviceCard();
    await card.getByRole("button", { name: "编辑编排", exact: true }).click();
    await page
      .getByLabel("节点补充指令（可选）", { exact: true })
      .nth(1)
      .fill("草稿版本三专属指令");
    await page.getByRole("button", { name: "保存编排", exact: true }).click();
    await card.getByRole("button", { name: "运行", exact: true }).click();
    await page.getByRole("button", { name: "开始运行", exact: true }).click();
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
    await page.locator(".trace-tree .child").nth(1).click();
    await expect(page.locator(".trace-detail pre").first()).not.toContainText(
      "草稿版本三",
    );
    await nav("编排与服务");
    await card.getByRole("button", { name: "发布新版本", exact: true }).click();
    await expect(card).toContainText("已发布 v2");
  });
  test("B12 失败注入与错误 Trace，修复输入后重跑", async () => {
    await runAgent(names.agent, "[fail] 验证失败");
    await expect(page.locator(".run-heading .badge")).toHaveText("失败");
    await expect(page.locator(".trace-detail .error")).toContainText(
      "已注入失败",
    );
    await runAgent(names.agent, "恢复正常输入");
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
  });
  test("B13 慢任务取消与终态一致", async () => {
    await runAgent(names.agent, "[slow] 验证取消");
    await page.getByRole("button", { name: "取消运行", exact: true }).click();
    await expect(page.locator(".run-heading .badge")).toHaveText("已取消");
    await expect(
      page.getByRole("button", { name: "取消运行", exact: true }),
    ).toHaveCount(0);
    await page.locator(".trace-tree .child").click();
    await expect(page.locator(".trace-detail .badge")).toHaveText("已取消");
  });
  test("B14 自带兼容模型、密钥不可读取、真实适配器运行", async () => {
    await nav("模型配置");
    await page.getByRole("button", { name: "添加模型", exact: true }).click();
    await page.getByLabel("配置名称", { exact: true }).fill(names.compatible);
    await page.getByLabel("模型类型", { exact: true }).selectOption("openai");
    await page.getByLabel("模型名称", { exact: true }).fill("fixture-chat");
    await page.getByLabel("Base URL").fill("http://127.0.0.1:8090/v1");
    await page.getByRole("button", { name: "保存模型", exact: true }).click();
    expect(JSON.stringify((await call("providers")).body)).not.toContain(
      "browser-fixture-secret",
    );
    await nav("Agent 注册中心");
    await page.getByRole("button", { name: "注册 Agent", exact: true }).click();
    await page
      .getByLabel("Agent 名称", { exact: true })
      .fill(names.compatAgent);
    await page.getByLabel("职责描述", { exact: true }).fill("测试兼容模型");
    await page
      .getByLabel("运行模型", { exact: true })
      .selectOption({ label: names.compatible + " · fixture-chat" });
    await page
      .getByLabel("此 Agent 的模型 API key", { exact: true })
      .fill("browser-fixture-secret");
    await page.getByRole("button", { name: "根据描述生成" }).click();
    await expect(page.getByLabel("System prompt", { exact: true })).toHaveValue(
      /兼容模型 Mock/,
    );
    await saveAgent();
    await runAgent(names.compatAgent, "用户提供模型运行");
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
    await page.locator(".trace-tree .child").click();
    await expect(page.locator(".trace-meta")).toContainText("42");
  });
  test("B15 A2A Agent Card 接入与 JSON-RPC 执行", async () => {
    await nav("Agent 注册中心");
    await page.getByRole("button", { name: "注册 Agent", exact: true }).click();
    await page.getByRole("button", { name: "Agent Card 接入" }).click();
    await page.getByLabel("Agent Card JSON").fill(
      JSON.stringify({
        name: names.a2a,
        description: "A2A 协议验收",
        url: "http://127.0.0.1:8090/a2a",
        version: "1.0.0",
        protocolVersion: "0.3.0",
        preferredTransport: "JSONRPC",
        capabilities: { streaming: false },
        defaultInputModes: ["text/plain"],
        defaultOutputModes: ["text/plain"],
        securitySchemes: { bearer: { type: "http", scheme: "bearer" } },
        security: [{ bearer: [] }],
        skills: [{ id: "a2a", name: "A2A 消息" }],
      }),
    );
    await page
      .getByLabel("外部 API key（Bearer，可选）", { exact: true })
      .fill("external-browser-secret");
    await saveAgent();
    await runAgent(names.a2a, "A2A 文本任务");
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
    await expect(page.locator(".trace-detail pre").nth(1)).toContainText(
      "A2A Mock",
    );
  });
  test("B16 开发者 API 示例、文档下载、浏览器 fetch 调用", async () => {
    await nav("开发者 API");
    await page.getByLabel("能力类型", { exact: true }).selectOption("services");
    await page
      .getByLabel("调用目标", { exact: true })
      .selectOption({ label: names.service });
    await expect(page.locator(".api-code pre").first()).toContainText(
      "/services/",
    );
    const dlPromise = page.waitForEvent("download");
    await page.getByRole("button", { name: "下载 OpenAPI 文档" }).click();
    const dl = await dlPromise;
    expect(dl.suggestedFilename()).toBe("alliance-openapi.json");
    await dl.saveAs(artifact + "alliance-openapi.json");
    const s = (await call("services")).body.find(
      (s) => s.name === names.service,
    );
    const run = await call("services/" + s.id + "/invoke", "POST", {
      input: "独立 API 验证",
    });
    expect(run.status).toBe(202);
    await expect
      .poll(async () => (await call("runs/" + run.body.id)).body.status)
      .toBe("succeeded");
    expect((await call("runs/" + run.body.id)).body.spans).toHaveLength(2);
    const unauth = await page.evaluate(async () => {
      const r = await fetch("/api/v1/agents", { credentials: "omit" });
      return r.status;
    });
    expect(unauth).toBe(401);
  });
  test("B17 资源引用删除保护", async () => {
    await nav("Agent 注册中心");
    const card = await agentCard(names.agent);
    await card
      .getByRole("button", { name: "删除 " + names.agent, exact: true })
      .click();
    await page.getByRole("button", { name: "确认删除", exact: true }).click();
    await expect(page.locator(".error.global")).toContainText("引用");
    await expect(
      page.getByRole("heading", { name: names.agent, exact: true }),
    ).toBeVisible();
    await nav("模型配置");
    const model = page.locator(".provider-card").filter({
      has: page.getByRole("heading", { name: names.model, exact: true }),
    });
    await model
      .getByRole("button", { name: "删除 " + names.model, exact: true })
      .click();
    await page.getByRole("button", { name: "确认删除", exact: true }).click();
    await expect(page.locator(".error.global")).toContainText("引用");
  });
  test("B18 响应式布局与刷新持久化", async () => {
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "工作台", exact: true }),
    ).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(
      page.getByRole("heading", { name: "工作台", exact: true }),
    ).toBeVisible();
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.screenshot({ path: artifact + "mobile.png", fullPage: true });
    await page.setViewportSize({ width: 1440, height: 1040 });
    await nav("Agent 注册中心");
    await expect(
      page.getByRole("heading", { name: names.agent, exact: true }),
    ).toBeVisible();
  });
  test("B24 连接失败提示与自动恢复", async () => {
    await nav("工作台");
    await page.route("**/api/v1/metrics", (route) => route.abort());
    await expect(page.locator(".error.global")).toContainText("连接中断");
    await expect(page.locator(".status-light")).toContainText("连接中断");
    await page.unroute("**/api/v1/metrics");
    await expect(page.locator(".status-light")).toContainText("平台运行正常");
    await expect(page.locator(".error.global")).toHaveCount(0);
  });
  test("B21 并行分支、汇聚编排与时间线", async () => {
    await nav("编排与服务");
    const card = await serviceCard();
    await card.getByRole("button", { name: "编辑编排", exact: true }).click();
    await page.getByLabel("依赖节点 ID", { exact: true }).nth(1).fill("");
    await page.getByRole("button", { name: "添加 Agent 节点" }).click();
    await page
      .getByLabel("选择 Agent", { exact: true })
      .nth(2)
      .selectOption({ label: names.agent });
    await page
      .getByLabel("依赖节点 ID", { exact: true })
      .nth(2)
      .fill("step_1,step_2");
    await page.getByRole("button", { name: "保存编排", exact: true }).click();
    await expect(
      card.locator(".flow-layer").first().locator(".flow-node"),
    ).toHaveCount(2);
    await expect(card).toContainText("并行执行");
    await card.getByRole("button", { name: "发布新版本", exact: true }).click();
    await page.screenshot({
      path: artifact + "orchestration.png",
      fullPage: true,
    });
    await card.getByRole("button", { name: "运行", exact: true }).click();
    await page.getByRole("button", { name: "开始运行", exact: true }).click();
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
    await expect(page.locator(".trace-tree .child")).toHaveCount(3);
    await page.locator(".trace-tree .child").nth(2).click();
    await expect(page.locator(".trace-detail pre").first()).toContainText(
      "step_1:",
    );
    await expect(page.locator(".trace-detail pre").first()).toContainText(
      "step_2:",
    );
  });
  test("B22 外部服务故障可定位", async () => {
    await runAgent(names.external, "[fail] remote unavailable");
    await expect(page.locator(".run-heading .badge")).toHaveText("失败");
    await expect(page.locator(".trace-detail .error")).toContainText(
      "HTTP 503",
    );
  });
  test("B23 编辑模型并保留密钥", async () => {
    await nav("模型配置");
    const model = page.locator(".provider-card").filter({
      has: page.getByRole("heading", { name: names.compatible, exact: true }),
    });
    await model.getByRole("button", { name: "编辑", exact: true }).click();
    await page
      .getByLabel("模型名称", { exact: true })
      .fill("fixture-chat-updated");
    await page.getByRole("button", { name: "保存模型", exact: true }).click();
    await expect(model).toContainText("fixture-chat-updated");
    const provider = (await call("providers")).body.find(
      (p) => p.name === names.compatible,
    );
    expect(provider.hasKey).toBe(false);
    const agent = (await call("agents")).body.find(
      (a) => a.name === names.compatAgent,
    );
    expect(agent.hasKey).toBe(true);
    expect(agent.apiKey).toBeUndefined();
    expect(provider.apiKey).toBeUndefined();
    await runAgent(names.compatAgent, "更新模型后运行");
    await expect(page.locator(".run-heading .badge")).toHaveText("成功");
  });
  test("B19 撤销发布、删除服务与 Agent/模型清理", async () => {
    await nav("编排与服务");
    const card = await serviceCard();
    await card.getByRole("button", { name: "撤销发布", exact: true }).click();
    await expect(
      card.getByRole("button", { name: "运行", exact: true }),
    ).toBeDisabled();
    await card
      .getByRole("button", { name: "删除 " + names.service, exact: true })
      .click();
    await page.getByRole("button", { name: "确认删除", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: names.service, exact: true }),
    ).toHaveCount(0);
    await nav("Agent 注册中心");
    for (const name of [
      names.agent,
      names.external,
      names.compatAgent,
      names.a2a,
    ]) {
      const c = await agentCard(name);
      await c
        .getByRole("button", { name: "删除 " + name, exact: true })
        .click();
      await page.getByRole("button", { name: "确认删除", exact: true }).click();
      await expect(
        page.getByRole("heading", { name, exact: true }),
      ).toHaveCount(0);
    }
    await nav("模型配置");
    for (const name of [names.model, names.compatible]) {
      const c = page
        .locator(".provider-card")
        .filter({ has: page.getByRole("heading", { name, exact: true }) });
      await c
        .getByRole("button", { name: "删除 " + name, exact: true })
        .click();
      await page.getByRole("button", { name: "确认删除", exact: true }).click();
      await expect(
        page.getByRole("heading", { name, exact: true }),
      ).toHaveCount(0);
    }
    await nav("运行与 Trace");
    await page.getByLabel("搜索", { exact: true }).fill(prefix);
    await expect(page.locator("tbody tr").first()).toBeVisible();
  });
  test("B20 无浏览器未捕获异常，安全退出", async () => {
    expect(errors).toEqual([]);
    await page.getByRole("button", { name: "退出工作区", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "登录工作区", exact: true }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "登录工作区", exact: true }),
    ).toBeVisible();
  });
});

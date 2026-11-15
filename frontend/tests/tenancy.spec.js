import { test, expect } from "@playwright/test";
import { createServer } from "node:http";
const stamp = Date.now().toString();
const password = "tenant-browser-password-123";
async function register(page, suffix) {
  const email = `tenant-${stamp}-${suffix}@example.test`;
  await page.goto("/");
  await page
    .getByRole("button", { name: "没有账号？创建工作区", exact: true })
    .click();
  await page.getByLabel("姓名", { exact: true }).fill("用户 " + suffix);
  await page
    .getByLabel("工作区名称", { exact: true })
    .fill("Workspace " + suffix + " " + stamp);
  await page.getByLabel("邮箱", { exact: true }).fill(email);
  await page.getByLabel("账号密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "注册并进入", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "工作台", exact: true }),
  ).toBeVisible();
  return email.toLowerCase();
}
async function nav(p, name) {
  await p.locator("nav").getByRole("button", { name, exact: true }).click();
}
async function call(page, path, method = "GET", body, headers = {}) {
  return page.evaluate(
    async ({ path, method, body, headers }) => {
      const ws = localStorage.getItem("alliance-workspace");
      const meta = await (
        await fetch("/api/v1/auth/me", { headers: { "X-Workspace-ID": ws } })
      ).json();
      const r = await fetch("/api/v1/" + path, {
        method,
        headers: {
          "Content-Type": "application/json",
          "X-Workspace-ID": ws,
          "X-CSRF-Token": meta.user?.csrfToken || "",
          ...headers,
        },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      return { status: r.status, body: await r.json() };
    },
    { path, method, body, headers },
  );
}
async function createAgent(page, name, modelLabel, key) {
  await nav(page, "Agent 注册中心");
  await page.getByRole("button", { name: "注册 Agent", exact: true }).click();
  await page.getByLabel("Agent 名称", { exact: true }).fill(name);
  await page.getByLabel("职责描述", { exact: true }).fill("验收专用 Agent");
  if (modelLabel)
    await page
      .getByLabel("运行模型", { exact: true })
      .selectOption({ label: modelLabel });
  if (key)
    await page.getByLabel("此 Agent 的模型 API key", { exact: true }).fill(key);
  await page.getByLabel("System prompt", { exact: true }).fill("处理用户输入");
  await page.getByRole("button", { name: "保存 Agent", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
}
async function run(page, name, input) {
  await nav(page, "Agent 注册中心");
  await page
    .locator(".agent-card")
    .filter({ has: page.getByRole("heading", { name, exact: true }) })
    .getByRole("button", { name: "试运行", exact: true })
    .click();
  await page.getByLabel("任务输入", { exact: true }).fill(input);
  await page.getByRole("button", { name: "开始运行", exact: true }).click();
  await expect(page.locator(".run-heading .badge")).toHaveText("成功");
}
let a, b, ca, cb, emailB, workspaceA, agentA, mock, url;
const observed = [];
test.describe.serial("生产化身份、租户与独立凭据", () => {
  test.beforeAll(async ({ browser }) => {
    ca = await browser.newContext();
    cb = await browser.newContext();
    a = await ca.newPage();
    b = await cb.newPage();
    mock = createServer(async (req, res) => {
      let raw = "";
      for await (const chunk of req) raw += chunk;
      const body = JSON.parse(raw);
      const input = body.messages.at(-1).content;
      observed.push({ input, auth: req.headers.authorization });
      const expected =
        input === "verify-A"
          ? "model-key-A"
          : input === "verify-B"
            ? "model-key-B"
            : input === "verify-A2"
              ? "model-key-A2"
              : null;
      if (expected && req.headers.authorization !== "Bearer " + expected) {
        res.writeHead(401);
        res.end("{}");
        return;
      }
      res.setHeader("Content-Type", "application/json");
      res.end(
        JSON.stringify({
          choices: [{ message: { content: "独立凭据验证成功：" + input } }],
          usage: { total_tokens: 9 },
        }),
      );
    });
    await new Promise((resolve) => mock.listen(0, "127.0.0.1", resolve));
    url = "http://127.0.0.1:" + mock.address().port + "/v1";
  });
  test.afterAll(async () => {
    await ca.close();
    await cb.close();
    await new Promise((resolve) => mock.close(resolve));
  });
  test("P01 双账号工作区隔离、伪造工作区拒绝", async () => {
    await register(a, "A");
    emailB = await register(b, "B");
    await createAgent(a, "Tenant A private");
    agentA = (await call(a, "agents")).body[0];
    workspaceA = (await call(a, "auth/me")).body.user.workspaceId;
    await nav(b, "Agent 注册中心");
    await expect(
      b.getByRole("heading", { name: "Tenant A private", exact: true }),
    ).toHaveCount(0);
    for (const [method, path, body] of [
      ["GET", "agents/" + agentA.id],
      ["DELETE", "agents/" + agentA.id],
      ["POST", "agents/" + agentA.id + "/invoke", { input: "forbidden" }],
    ])
      expect((await call(b, path, method, body)).status).toBe(404);
    expect(
      (
        await call(b, "agents", "GET", undefined, {
          "X-Workspace-ID": workspaceA,
        })
      ).status,
    ).toBe(403);
  });
  test("P02 成员只读权限、提权后运行、移除后即时失效", async () => {
    await nav(a, "工作区与权限");
    await a.getByLabel("成员邮箱", { exact: true }).fill(emailB);
    await a.getByLabel("新成员角色", { exact: true }).selectOption("viewer");
    await a.getByRole("button", { name: "添加成员", exact: true }).click();
    await expect(a.locator("section").filter({has:a.getByRole("heading",{name:"成员与角色",exact:true})}).locator("tbody")).toContainText(emailB);
    await b.reload();
    await b.getByLabel("切换工作区", { exact: true }).selectOption(workspaceA);
    await expect(
      b.getByRole("heading", { name: "工作台", exact: true }),
    ).toBeVisible();
    await nav(b, "Agent 注册中心");
    await expect(
      b.getByRole("heading", { name: "Tenant A private", exact: true }),
    ).toBeVisible();
    expect((await call(b, "agents/" + agentA.id, "DELETE")).status).toBe(403);
    expect(
      (
        await call(b, "agents/" + agentA.id + "/invoke", "POST", {
          input: "no",
        })
      ).status,
    ).toBe(403);
    await a
      .getByLabel("角色 " + emailB, { exact: true })
      .selectOption("operator");
    await expect(a.getByLabel("角色 " + emailB, { exact: true })).toHaveValue(
      "operator",
    );
    await run(b, "Tenant A private", "operator allowed");
    expect(
      (
        await call(b, "providers", "POST", {
          name: "bad",
          type: "mock",
          model: "mock",
        })
      ).status,
    ).toBe(403);
    a.once("dialog", (d) => d.accept());
    await a
      .getByRole("button", { name: "移除 " + emailB, exact: true })
      .click();
    await expect(a.getByText(emailB, { exact: true })).toHaveCount(0);
    expect(
      (
        await call(b, "agents", "GET", undefined, {
          "X-Workspace-ID": workspaceA,
        })
      ).status,
    ).toBe(403);
    await b.reload();
    await expect(
      b.getByRole("heading", { name: "工作台", exact: true }),
    ).toBeVisible();
    await nav(b, "Agent 注册中心");
    await expect(
      b.getByRole("heading", { name: "Tenant A private", exact: true }),
    ).toHaveCount(0);
  });
  test("P03 平台访问 key 权限与撤销，最后管理员保护", async () => {
    await nav(a, "工作区与权限");
    await a.getByLabel("访问 key 名称", { exact: true }).fill("external-app");
    await a
      .getByLabel("访问 key 权限", { exact: true })
      .selectOption("operator");
    await a.getByRole("button", { name: "创建访问 key", exact: true }).click();
    const key = await a.getByTestId("new-platform-key").textContent();
    const invoke = await a.evaluate(
      async ({ key, id }) => {
        const r = await fetch("/api/v1/agents/" + id + "/invoke", {
          method: "POST",
          credentials: "omit",
          headers: {
            Authorization: "Bearer " + key,
            "Content-Type": "application/json",
          },
          body: JSON.stringify({ input: "bearer call" }),
        });
        return r.status;
      },
      { key, id: agentA.id },
    );
    expect(invoke).toBe(202);
    await a.getByRole("button", { name: "我已保存", exact: true }).click();
    await expect(a.getByTestId("new-platform-key")).toHaveCount(0);
    await a
      .getByRole("button", { name: "撤销 external-app", exact: true })
      .click();
    await expect(
      a.getByRole("button", { name: "撤销 external-app", exact: true }),
    ).toBeDisabled();
    await expect(a.getByRole("row").filter({ hasText: "external-app" })).toContainText("已撤销");
    expect(
      await a.evaluate(
        async (key) =>
          (
            await fetch("/api/v1/agents", {
              credentials: "omit",
              headers: { Authorization: "Bearer " + key },
            })
          ).status,
        key,
      ),
    ).toBe(401);
    const me = (await call(a, "auth/me")).body.user;
    await a
      .getByLabel("角色 " + me.email, { exact: true })
      .selectOption("viewer");
    await expect(a.getByRole("alert")).toContainText("最后一位管理员");
    const audits = (await call(a, "audit")).body;
    expect(audits.some((x) => x.action === "key.revoke")).toBe(true);
  });
  test("P04 共用 endpoint 的两个 Agent 独立模型 key、轮转与清除", async () => {
    await nav(a, "模型配置");
    await a.getByRole("button", { name: "添加模型", exact: true }).click();
    await a.getByLabel("配置名称", { exact: true }).fill("shared endpoint");
    await a.getByLabel("模型类型", { exact: true }).selectOption("openai");
    await a.getByLabel("模型名称", { exact: true }).fill("same-model");
    await a.getByLabel("Base URL", { exact: true }).fill(url);
    await expect(a.getByLabel("API key", { exact: true })).toHaveCount(0);
    await a.getByRole("button", { name: "保存模型", exact: true }).click();
    await createAgent(
      a,
      "Key A",
      "shared endpoint · same-model",
      "model-key-A",
    );
    await createAgent(
      a,
      "Key B",
      "shared endpoint · same-model",
      "model-key-B",
    );
    await run(a, "Key A", "verify-A");
    await run(a, "Key B", "verify-B");
    await nav(a, "Agent 注册中心");
    const card = a
      .locator(".agent-card")
      .filter({ has: a.getByRole("heading", { name: "Key A", exact: true }) });
    await card.getByRole("button", { name: "配置", exact: true }).click();
    await expect(
      a.getByLabel("此 Agent 的模型 API key", { exact: true }),
    ).toHaveValue("");
    await a
      .getByLabel("此 Agent 的模型 API key", { exact: true })
      .fill("model-key-A2");
    await a.getByRole("button", { name: "保存 Agent", exact: true }).click();
    await run(a, "Key A", "verify-A2");
    await run(a, "Key B", "verify-B");
    const agents = (await call(a, "agents")).body;
    expect(JSON.stringify(agents)).not.toContain("model-key-");
    expect((await call(a, "providers")).body.every((p) => !p.hasKey)).toBe(
      true,
    );
    await nav(a, "Agent 注册中心");
    await card.getByRole("button", { name: "配置", exact: true }).click();
    await a.getByLabel("清除此 Agent 已保存的模型密钥").check();
    await a.getByRole("button", { name: "保存 Agent", exact: true }).click();
    expect(
      (await call(a, "agents")).body.find((x) => x.name === "Key A").hasKey,
    ).toBe(false);
    expect(
      (await call(a, "agents")).body.find((x) => x.name === "Key B").hasKey,
    ).toBe(true);
    expect(
      observed
        .filter((x) => x.input === "verify-B")
        .every((x) => x.auth === "Bearer model-key-B"),
    ).toBe(true);
  });
  test("P05 创建与切换第二工作区，刷新保持选择", async () => {
    await nav(a, "工作区与权限");
    await a.getByLabel("新工作区名称", { exact: true }).fill("Second " + stamp);
    await a.getByRole("button", { name: "创建工作区", exact: true }).click();
    await expect(a.getByLabel("切换工作区").locator("option")).toHaveCount(2);
    await a.getByLabel("切换工作区").selectOption({ label: "Second " + stamp });
    await nav(a, "Agent 注册中心");
    await expect(
      a.getByRole("heading", { name: "Key B", exact: true }),
    ).toHaveCount(0);
    await a.reload();
    await expect(a.getByLabel("切换工作区")).toHaveText(/Second/);
    expect((await call(a, "agents")).body).toHaveLength(0);
    await a.getByLabel("切换工作区").selectOption(workspaceA);
    await nav(a, "Agent 注册中心");
    await expect(
      a.getByRole("heading", { name: "Key B", exact: true }),
    ).toBeVisible();
  });
});

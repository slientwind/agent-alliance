import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
const baseURL = process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173";
try {
  for (const width of [1440, 390]) {
    const page = await browser.newPage({ baseURL, viewport: { width, height: 900 } });
    let pool = { active: 1, maxJobs: 2, timeoutSeconds: 60, revision: 1 };
    const project = { id: "fixture-project", title: "验收项目" };
    const team = { id: "fixture-team", name: "验收团队" };
    const bots = [
      { provider: "feishu", enabled: false, revision: 0, hasCredentials: false, fields: { app: "", tenant: "", bot: "" } },
      { provider: "dingtalk", enabled: false, revision: 0, hasCredentials: false, fields: { app: "", tenant: "", bot: "" } },
      { provider: "slack", enabled: false, revision: 0, hasCredentials: false, fields: { app: "", tenant: "", bot: "" } },
    ];
    let sources = [], mcp = [];
    const prefix = "/api/admin/integrations";
    await page.route("**/api/**", async route => {
      const request = route.request();
      const path = new URL(request.url()).pathname;
      const method = request.method();
      if (path === prefix + "/sandbox" && method === "PUT") {
        assert.deepEqual(request.postDataJSON(), { expected: 1, maxJobs: 4, timeoutSeconds: 45 });
        pool = { ...pool, maxJobs: 4, timeoutSeconds: 45, revision: 2 };
        return route.fulfill({ json: { revision: 2 } });
      }
      if (path === prefix + "/bots/slack" && method === "PUT") {
        const { credentials, expected, ...next } = request.postDataJSON();
        assert.deepEqual(credentials, { secret: "fixture-signing", verify: "fixture-bot-token", encrypt: "" });
        assert.equal(expected, 0);
        Object.assign(bots[2], next, { revision: 1, hasCredentials: true });
        return route.fulfill({ json: { revision: 1 } });
      }
      if (path.startsWith(prefix + "/knowledge/") && method === "PUT") {
        const id = path.split("/").at(-1);
        const { secret, expected, ...next } = request.postDataJSON();
        assert.equal(secret, "fixture-confluence-token");
        assert.equal(expected, 0);
        assert.equal(next.project, project.id);
        assert.equal(next.settings.document, "432");
        sources = [{ ...next, id, projectName: project.title, revision: 1, hasCredential: true, state: "ready", version: 1 }];
        return route.fulfill({ json: { revision: 1 } });
      }
      if (path.startsWith("/api/admin/mcp/servers/")) {
        const [, , , , , id, action, teamID] = path.split("/");
        if (action === "probe") {
          return route.fulfill({ json: { tools: [{ name: "lookup", allowed: false }] } });
        }
        if (action === "teams") {
          assert.equal(teamID, team.id);
          assert.deepEqual(request.postDataJSON(), { enabled: true, expected: 0 });
          mcp[0].teams = [{ id: teamID, enabled: true, revision: 1 }];
          return route.fulfill({ json: { revision: 1 } });
        }
        const { expected, bearer, ...next } = request.postDataJSON();
        if (expected === 0) assert.equal(bearer, "fixture-mcp-token");
        if (expected === 1) assert.deepEqual(next.tools, { lookup: "read_only" });
        mcp = [{ ...next, id, revision: expected + 1, hasCredential: true, teams: mcp[0]?.teams ?? [] }];
        return route.fulfill({ json: { revision: expected + 1 } });
      }
      const responses = {
        "/api/instance": { initialized: true, name: "验收企业" },
        "/api/me": { id: "admin", name: "验收管理员", role: "admin", enabled: true },
        "/api/teams": [team],
        [prefix + "/sandbox"]: pool,
        [prefix + "/bots"]: bots,
        [prefix + "/knowledge"]: sources,
        [prefix + "/knowledge/targets"]: [project],
        [prefix + "/search"]: { provider: "tavily", endpoint: "https://api.tavily.com/search", enabled: false, revision: 1, hasCredential: false, health: "unchecked" },
        [prefix + "/email"]: { host: "", port: 465, security: "tls", sender: "", username: "", enabled: false, revision: 1, hasPassword: false, health: "unchecked" },
        "/api/admin/mcp/servers": mcp,
      };
      return route.fulfill({ json: responses[path] ?? [] });
    });
    const errors = [];
    page.on("pageerror", error => errors.push(error.message));
    await page.goto("/admin/integrations");
    const sandbox = page.getByRole("region", { name: "沙箱配额" });
    await sandbox.getByLabel("最大并发").fill("4");
    await sandbox.getByLabel("任务时限（秒）").fill("45");
    await sandbox.getByRole("button", { name: "保存沙箱配额" }).click();
    await sandbox.getByText("沙箱配额已保存").waitFor();
    const knowledge = page.getByRole("region", { name: "企业知识源" });
    await knowledge.getByRole("button", { name: "添加知识源" }).click();
    await knowledge.getByLabel("来源", { exact: true }).selectOption("confluence");
    await knowledge.getByLabel("知识源名称").fill("政策说明");
    await knowledge.getByLabel("页面 ID").fill("432");
    await knowledge.getByLabel("Confluence 站点").fill("https://fixture.atlassian.net");
    await knowledge.getByLabel("Confluence 账号邮箱").fill("fixture@example.test");
    await knowledge.getByLabel("Confluence API Token").fill("fixture-confluence-token");
    await knowledge.getByRole("button", { name: "保存知识源" }).click();
    await knowledge.getByText(/已同步 · 资料版本 1/).waitFor();
    const channels = page.getByRole("region", { name: "IM 渠道机器人" });
    await channels.getByRole("button", { name: "编辑 Slack机器人" }).click();
    await channels.getByLabel("工作区 ID").fill("TTEST");
    await channels.getByLabel("App ID", { exact: true }).fill("ATEST");
    await channels.getByLabel("Signing Secret").fill("fixture-signing");
    await channels.getByLabel("Bot Token").fill("fixture-bot-token");
    await channels.getByLabel("启用机器人").check();
    await channels.getByRole("button", { name: "保存机器人配置" }).click();
    await channels.getByText(/已启用 · 数据库配置/).waitFor();
    const directory = page.getByRole("region", { name: "企业 MCP 服务" });
    await directory.getByRole("button", { name: "添加 MCP 服务" }).click();
    await directory.getByLabel("服务名称").fill("知识检索");
    await directory.getByLabel("服务地址").fill("https://mcp.example.test/api");
    await directory.getByLabel("访问凭据").fill("fixture-mcp-token");
    await directory.getByRole("button", { name: "保存 MCP 服务" }).click();
    await directory.getByRole("button", { name: "检测 知识检索" }).click();
    await directory.getByLabel("lookup").selectOption("read_only");
    await directory.getByRole("button", { name: "保存 MCP 服务" }).click();
    await directory.getByRole("button", { name: "授权团队访问" }).click();
    await directory.getByText(/1 个获准团队/).waitFor();
    await page.reload();
    await channels.getByRole("button", { name: "编辑 Slack机器人" }).click();
    assert.equal(await channels.getByLabel("Signing Secret").inputValue(), "");
    assert.equal(await sandbox.getByLabel("最大并发").inputValue(), "4");
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    assert.deepEqual(errors, []);
    await page.close();
  }
  console.log("Enterprise resource center forms, status refresh and write-only credentials passed at both widths.");
} finally {
  await browser.close();
}

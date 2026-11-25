import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  for (const [width, height] of [[1440, 900], [390, 844]]) {
    const page = await browser.newPage({
      baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173",
      viewport: { width, height },
    });
    await page.addInitScript(() => {
      const NativeEventSource = window.EventSource;
      window.EventSource = class extends NativeEventSource {
        constructor(url, options) {
          super(url, options);
          if (String(url).endsWith("/session-events")) window.testSessionStream = this;
        }
      };
    });
    const id = "2b40fe15-8725-45cf-9576-5661de125cb5";
    const artifact = {
      id: "artifact-1", name: "采购成果", ordinal: 1, version: 2, body: "采购成果第二版", file: null,
      approved: false, reviewNotesReadable: true, feedback: [], sourceChanges: [],
    };
    const project = {
      id, title: "采购报价核对", stage: "brief", revision: 1, owner: "member", reviewer: "member", team: "team", planVersion: 0,
      messages: [{ seq: 1, kind: "user", body: "整理报价表" }], artifacts: [artifact],
      participants: [{ id: "member", name: "林予", role: "owner", canReview: true, canViewCosts: true, enabled: true }], handoffs: [],
      sharedAgents: [], deliveries: [], distributionCases: [], budget: { warning: false, pending: 0 }, costSummary: {},
      run: null, suggestion: null, plan: null,
    };
    const collaborationRequests = [];
    const commentRequests = [];
    const comments = [];
    const searchSettings = {
      provider: "tavily", endpoint: "https://api.tavily.com/search",
      revision: 1, enabled: false, hasCredential: false, health: "unchecked",
    };
    let schedule = null;
    await page.route("**/api/**", (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const path = url.pathname;
      if (path === "/api/automations") {
        if (request.method() === "POST") {
          const next = request.postDataJSON();
          assert.ok(request.headers()["idempotency-key"]);
          assert.equal(next.onceAt, "2030-12-03T01:30:00.000Z");
          assert.equal(next.cron, null);
          schedule = { ...next, id: "schedule", revision: 1, state: "active", sync: "ready", cursor: null };
          return route.fulfill({ status: 201, json: { id: schedule.id } });
        }
        return route.fulfill({ json: schedule ? [schedule] : [] });
      }
      if (path === "/api/automations/schedule") {
        if (request.method() === "PATCH") {
          const next = request.postDataJSON();
          assert.deepEqual(next, { expected: 1, active: false });
          schedule.state = "paused";
          schedule.revision++;
        } else if (request.method() === "DELETE") {
          assert.equal(url.searchParams.get("expected"), "2");
          schedule = null;
        }
        return route.fulfill({ status: 204 });
      }
      if (path === "/api/automations/schedule/runs") return route.fulfill({ json: [] });
      if (path === "/api/admin/integrations/search") {
        if (request.method() === "PUT") {
          const next = request.postDataJSON();
          assert.equal(next.provider, "bocha");
          assert.equal(next.expected, 1);
          assert.equal(next.key, "fixture-secret");
          assert.equal(next.enabled, true);
          Object.assign(searchSettings, { provider: "bocha", endpoint: "https://api.bochaai.com/v1/web-search",
            revision: 2, enabled: true, hasCredential: true });
          return route.fulfill({ json: { revision: 2 } });
        }
        return route.fulfill({ json: searchSettings });
      }
      if (path === "/api/admin/integrations/search/probe") {
        searchSettings.health = "ready";
        return route.fulfill({ json: { health: "ready" } });
      }
      const artifactPath = "/api/projects/" + id + "/artifacts/" + artifact.id;
      if (path.endsWith("/session-events") || path.endsWith("/events")) {
        return route.fulfill({ contentType: "text/event-stream", body: ": ready\n\n" });
      }
      if (path === "/api/projects/" + id + "/participants" && request.method() === "POST") {
        collaborationRequests.push(request.postDataJSON());
        project.participants.push({ id: "coworker", name: "陈子", role: "reviewer", canReview: true, canViewCosts: false, enabled: true });
        project.revision++;
        return route.fulfill({ status: 204 });
      }
      if (path === "/api/projects/" + id + "/participants/coworker" && request.method() === "DELETE") {
        collaborationRequests.push(request.postDataJSON());
        project.participants.pop();
        project.revision++;
        return route.fulfill({ status: 204 });
      }
      if (path === artifactPath + "/versions") {
        const versions = [{ version: 2, body: artifact.body, file: null }, { version: 1, body: "采购成果第一版", file: null }];
        return route.fulfill({ json: url.searchParams.has("version") ? versions.filter(entry => entry.version === Number(url.searchParams.get("version"))) : versions });
      }
      if (path === artifactPath + "/comments") {
        if (request.method() === "POST") {
          const input = request.postDataJSON();
          commentRequests.push(input);
          comments.push({ id: "comment-1", author: "member", authorName: "林予", anchor: input.anchor, body: input.body, revision: 1 });
          return route.fulfill({ status: 201, json: comments[0] });
        }
        return route.fulfill({ json: { comments: url.searchParams.get("version") === "2" ? comments : [] } });
      }
      if (path === artifactPath + "/comments/comment-1" && request.method() === "PATCH") {
        commentRequests.push(request.postDataJSON());
        comments[0].resolvedAt = new Date().toISOString();
        return route.fulfill({ status: 204 });
      }
      const data = {
        "/api/instance": { initialized: true, name: "示例企业" },
        "/api/me": { id: "member", name: "林予", role: "admin", enabled: true },
        "/api/general-tiers": ["default"],
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
    const sourceResponse = response => new URL(response.url()).pathname === "/api/projects/" + id + "/sources";
    const initialSources = page.waitForResponse(sourceResponse);
    await search.press("Enter");
    await page.waitForURL("**/chat/" + id);
    await page.getByText("整理报价表", { exact: true }).waitFor();
    await initialSources;
    await page.waitForFunction(() => !!window.testSessionStream);
    const changedSources = page.waitForResponse(sourceResponse);
    await page.evaluate(() => window.testSessionStream.dispatchEvent(new MessageEvent("sources.changed", { data: "{}" })));
    await changedSources;
    await page.getByRole("button", { name: "拉同事协作" }).click();
    const collaboration = page.getByRole("dialog", { name: "协作成员" });
    await collaboration.getByLabel("企业成员邮箱").fill("colleague@example.invalid");
    await collaboration.getByLabel("分工").selectOption("reviewer");
    await collaboration.getByRole("button", { name: "保存分工" }).click();
    await collaboration.getByText("内容审核人", { exact: true }).filter({ visible: true }).waitFor();
    await collaboration.getByRole("button", { name: "移除 陈子" }).click();
    await collaboration.getByRole("button", { name: "确认移除" }).click();
    await collaboration.getByRole("button", { name: "移除 陈子" }).waitFor({ state: "detached" });
    await collaboration.getByText("已移除 陈子 的项目访问权。", { exact: true }).waitFor();
    assert.deepEqual(collaborationRequests, [{ email: "colleague@example.invalid", role: "reviewer", expected: 1 }, { expected: 2 }]);
    await checkWidth();
    await collaboration.getByRole("button", { name: "关闭协作成员" }).click();
    await page.getByRole("button", { name: "打开成果 采购成果" }).click();
    const canvas = page.getByRole("complementary", { name: "采购成果" });
    await canvas.getByText("采购成果第二版", { exact: true }).waitFor();
    await checkWidth();
    await canvas.getByLabel("段落编号").fill("1");
    await canvas.getByLabel("评论", { exact: true }).fill("请核对数字");
    await canvas.getByRole("button", { name: "发送评论" }).click();
    await canvas.getByText("请核对数字", { exact: true }).waitFor();
    await canvas.getByRole("button", { name: "标记已解决" }).click();
    await canvas.getByText("已解决", { exact: true }).waitFor();
    assert.deepEqual(commentRequests, [
      { expected_version: 2, anchor: { kind: "paragraph", index: 1 }, body: "请核对数字" },
      { expected_revision: 1, resolved: true },
    ]);
    await canvas.getByLabel("成果版本").selectOption("1");
    await canvas.getByText("采购成果第一版", { exact: true }).waitFor();
    assert.equal(await canvas.getByRole("button", { name: "发送评论" }).count(), 0);
    await canvas.getByRole("button", { name: "关闭成果面板" }).click();
    await canvas.waitFor({ state: "detached" });
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
    await page.getByRole("button", { name: "后台与定时", exact: true }).click();
    await page.waitForURL("**/automations");
    await page.getByRole("button", { name: "新建定时委托" }).click();
    await page.getByLabel("任务名称").fill("月度事项");
    await page.getByLabel("工作目标").fill("整理待处理工作");
    await page.getByRole("combobox", { name: /^执行频率/ }).selectOption("once");
    await page.getByLabel("执行时间").fill("2030-12-03T09:30");
    await page.getByLabel("时区").fill("Asia/Shanghai");
    await page.getByRole("button", { name: "保存定时委托" }).click();
    await page.getByRole("heading", { name: "月度事项" }).waitFor();
    await page.getByText(/2030-12-03 09:30/).waitFor();
    await page.getByRole("button", { name: "暂停", exact: true }).click();
    await page.getByText(/已暂停/).waitFor();
    await page.getByRole("button", { name: "运行记录" }).click();
    await page.getByText("暂无运行记录").waitFor();
    await page.getByRole("button", { name: "删除计划" }).click();
    await page.getByRole("heading", { name: "月度事项" }).waitFor({ state: "hidden" });
    await checkWidth();
    await page.getByRole("button", { name: "企业管理", exact: true }).click();
    await page.waitForURL("**/admin");
    assert.equal(await page.locator("dialog[open]").count(), 0);
    await checkWidth();
    await page.getByRole("button", { name: "企业服务", exact: true }).click();
    await page.waitForURL("**/admin/integrations");
    await page.getByRole("combobox", { name: /^搜索服务/ }).selectOption("bocha");
    await page.getByLabel("搜索服务密钥").fill("fixture-secret");
    await page.getByLabel("允许员工在对话中联网搜索").check();
    await page.getByRole("button", { name: "保存", exact: true }).click();
    await page.getByRole("button", { name: "检测连接" }).click();
    await page.getByText("连接正常", { exact: true }).waitFor();
    assert.equal(await page.getByLabel("搜索服务密钥").inputValue(), "");
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
  console.log("Navigation, shortcuts, collaboration, live source updates, and the artifact panel passed at desktop and mobile widths.");
} finally {
  await browser.close();
}

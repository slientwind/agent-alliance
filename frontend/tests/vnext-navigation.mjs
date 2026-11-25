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
      spaceKind: "team", messages: [{ seq: 1, kind: "user", mine: true, body: "整理报价表" }], artifacts: [artifact],
      participants: [
        { id: "member", name: "林予", role: "owner", canReview: true, canViewCosts: true, enabled: true },
        { id: "72cc7480-9e2b-4e46-a5d5-9d76b353ed4c", name: "叶青", role: "contributor", canReview: false, canViewCosts: false, enabled: true },
      ], handoffs: [],
      sharedAgents: [], deliveries: [], distributionCases: [], budget: { warning: false, pending: 0 }, costSummary: {},
      run: null, suggestion: null, plan: null,
    };
    const collaborationRequests = [];
    const commentRequests = [];
    const comments = [];
    const mentionID = "59478ac3-5971-4583-b4c2-af8e3ca97ad6";
    const searchSettings = {
      provider: "tavily", endpoint: "https://api.tavily.com/search",
      revision: 1, enabled: false, hasCredential: false, health: "unchecked",
    };
    const mailSettings = {
      host: "", port: 465, security: "tls", sender: "", username: "",
      revision: 1, enabled: false, hasPassword: false, health: "unchecked",
    };
    let schedule = null;
    const notification = { id: "notice", channel: "feishu", state: "failed", revision: 3 };
    const emailNotice = { id: "email-notice", channel: "email", state: "accepted", revision: 3, receipt: "mail-receipt" };
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
          assert.equal(next.notifyFeishu, true);
          assert.equal(next.notifyEmail, true);
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
      if (path === "/api/automations/notification-channels") return route.fulfill({ json: { feishu: true, email: true } });
      if (path === "/api/automations/schedule/runs") {
        return route.fulfill({ json: [{ occurrence: "one", state: "completed", scheduled: schedule.onceAt, notifications: [notification, emailNotice] }] });
      }
      if (path === "/api/automations/schedule/notifications/notice/retry") {
        assert.deepEqual(request.postDataJSON(), { expected: 3 });
        Object.assign(notification, { state: "accepted", receipt: "receipt-1", revision: 5 });
        return route.fulfill({ status: 204 });
      }
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
      if (path === "/api/admin/integrations/email") {
        if (request.method() === "PUT") {
          const { password, expected, ...next } = request.postDataJSON();
          assert.equal(expected, 1);
          assert.equal(password, "smtp-fixture");
          assert.equal(next.host, "smtp.example.test");
          assert.equal(next.enabled, true);
          Object.assign(mailSettings, next, { revision: 2, hasPassword: true });
          return route.fulfill({ json: { revision: 2 } });
        }
        return route.fulfill({ json: mailSettings });
      }
      if (path === "/api/admin/integrations/email/probe") {
        mailSettings.health = "ready";
        return route.fulfill({ json: { health: "ready" } });
      }
      if (path === "/api/projects/" + id + "/glossary" && request.method() === "POST") {
        const next = request.postDataJSON();
        assert.deepEqual({ ...next, id: undefined }, {
          id: undefined, messageSeq: 1, term: "核准报价", aliases: ["QCC"], definition: "复核后的正式报价",
        });
        assert.match(next.id, /^[0-9a-f-]{36}$/);
        return route.fulfill({ json: { id: next.id, revision: 1 } });
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
        const versions = [{ version: 2, body: "采购成果第二版", file: null }, { version: 1, body: "采购成果第一版", file: null }];
        if (artifact.version === 3) versions.unshift({ version: 3, body: artifact.body, file: null });
        return route.fulfill({ json: url.searchParams.has("version") ? versions.filter(entry => entry.version === Number(url.searchParams.get("version"))) : versions });
      }
      if (path === artifactPath + "/comments") {
        if (request.method() === "POST") {
          const input = request.postDataJSON();
          commentRequests.push(input);
          assert.match(request.headers()["idempotency-key"], /^[a-f0-9-]{36}$/);
          const entry = { id: input.agent ? mentionID : "comment-1", author: "member", authorName: "林予", anchor: input.anchor, body: input.body,
            mentions: input.mentions, agentRun: input.agent ? { id: "fake-run", status: "queued" } : undefined, revision: 1 };
          comments.push(entry);
          return route.fulfill({ status: 201, json: entry });
        }
        const focus = url.searchParams.get("focus");
        return route.fulfill({ json: { comments: url.searchParams.get("version") === "2" ? comments.filter(comment => !focus || comment.id === focus) : [] } });
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
        "/api/projects/review-inbox": [{ project: id, title: project.title, kind: "mention", name: artifact.name,
          subject: mentionID, version: 2, revision: 1, step: 1, artifact: artifact.id }],
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
    await page.getByRole("button", { name: "沉淀至团队口径词典" }).click();
    const correction = page.getByRole("form", { name: "保存团队术语" });
    await correction.getByLabel("术语", { exact: true }).fill("核准报价");
    await correction.getByLabel("同义词").fill("QCC");
    await correction.getByLabel("口径说明").fill("复核后的正式报价");
    await correction.getByRole("button", { name: "保存团队术语" }).click();
    await page.getByText("已保存到团队口径词典", { exact: true }).waitFor();
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
    await canvas.getByLabel("成果版本").selectOption("");
    await canvas.getByLabel("段落编号").fill("1");
    await canvas.getByLabel("评论", { exact: true }).fill("把单价改为 27");
    await canvas.getByRole("button", { name: "@同事" }).click();
    await canvas.getByRole("checkbox", { name: "叶青" }).check();
    await canvas.getByRole("checkbox", { name: "@Agent 局部修改此处" }).check();
    await canvas.getByRole("button", { name: "发送评论" }).click();
    await canvas.getByText("Agent 正在处理此处", { exact: true }).waitFor();
    assert.deepEqual(commentRequests[2], { expected_version: 2, anchor: { kind: "paragraph", index: 1 }, body: "把单价改为 27",
      mentions: ["72cc7480-9e2b-4e46-a5d5-9d76b353ed4c"], agent: true });
    comments[1].agentRun = { id: "fake-run", status: "completed", version: 3 };
    artifact.version = 3;
    artifact.body = "单价 27";
    await page.goto("/chat/" + id + "?task=mention-" + mentionID);
    await canvas.getByRole("button", { name: "查看局部修订 v3" }).waitFor();
    assert.equal(await canvas.locator("article:focus").count(), 1);
    await canvas.getByRole("button", { name: "查看局部修订 v3" }).click();
    await canvas.getByText("单价 27", { exact: true }).waitFor();
    await checkWidth();
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
    await page.getByLabel("完成时用飞书通知我").check();
    await page.getByLabel("完成时用邮件通知我").check();
    await page.getByRole("button", { name: "保存定时委托" }).click();
    await page.getByRole("heading", { name: "月度事项" }).waitFor();
    await page.getByText(/2030-12-03 09:30/).waitFor();
    await page.getByRole("button", { name: "暂停", exact: true }).click();
    await page.getByText(/已暂停/).waitFor();
    await page.getByRole("button", { name: "运行记录" }).click();
    await page.getByText(/飞书 · 发送失败/).waitFor();
    await page.getByText("mail-receipt").waitFor();
    await page.getByRole("button", { name: "补发通知" }).click();
    await page.getByText("receipt-1").waitFor();
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
    await page.getByLabel("SMTP 服务器").fill("smtp.example.test");
    await page.getByLabel("发件邮箱").fill("sender@example.test");
    await page.getByLabel("SMTP 用户名").fill("smtp-user");
    await page.getByLabel("SMTP 密码").fill("smtp-fixture");
    await page.getByLabel("允许发送定时任务邮件").check();
    await page.getByRole("button", { name: "保存邮件服务" }).click();
    await page.getByRole("button", { name: "检测邮件连接" }).click();
    await page.getByText("邮件连接正常", { exact: true }).waitFor();
    assert.equal(await page.getByLabel("SMTP 密码").inputValue(), "");
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

import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 390, height: 840 },
  });
  page.setDefaultTimeout(9000);
  const draftID = "5b4101f0-055d-45d8-954a-4959f1f310c2";
  const releaseID = "e5a977fb-b432-4e13-8b9a-faa1cfc1c9b9";
  const projectID = "fa66f253-ca83-4c40-b094-c6a8fa56df4a";
  const draftPath = "/api/studio/" + draftID;
  const projectPath = "/api/projects/" + projectID;
  const calls = [];
  const errors = [];
  const details = {
    id: projectID, title: "业务核对", stage: "brief", revision: 1, owner: "member", reviewer: "member", team: "team", planVersion: 0,
    messages: [], artifacts: [], participants: [], handoffs: [], sharedAgents: [], deliveries: [], distributionCases: [],
    budget: { warning: false, pending: 0 }, costSummary: {}, run: null, suggestion: null, plan: null,
  };
  const spec = { name: "", purpose: "", instructions: "", steps: [], checks: [], capabilities: [], tier: "default" };
  const draft = { id: draftID, team: "team", revision: 1, spec, messages: [] };
  let published;
  page.on("pageerror", error => errors.push(error.message));
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() === "POST" || request.method() === "PUT") {
      if (path === "/api/studio") return route.fulfill({ status: 201, json: draft });
      if (path === draftPath + "/chat") {
        assert.ok(request.headers()["idempotency-key"]);
        assert.match(request.headers()["content-type"], /^multipart\/form-data;/);
        assert.match(request.postData(), /参考格式.csv/);
        draft.revision++;
        Object.assign(spec, { name: "报价核对助手", purpose: "整理报价", instructions: "金额左对齐。保留币种。",
          steps: ["逐项核对明细"], checks: ["确认小计"], capabilities: ["office"] });
        const turn = { revision: draft.revision, spec, reply: "已生成报价核对草稿。", questions: [], patch: [] };
        draft.messages.push({ ...turn, message: "帮我做报价核对" });
        return route.fulfill({ json: turn });
      }
      if (path === draftPath + "/capabilities") {
        assert.deepEqual(request.postDataJSON(), { expected: 2, capabilities: ["office", "knowledge"] });
        draft.revision++;
        spec.capabilities.push("knowledge");
        return route.fulfill({ json: draft });
      }
      if (path === draftPath + "/try") {
        assert.ok(request.headers()["idempotency-key"]);
        assert.equal(request.postDataJSON().expected, draft.revision);
        assert.equal(request.headers().accept, "text/event-stream");
        return route.fulfill({ contentType: "text/event-stream", body: 'event: chunk\ndata: {"text":"金额列"}\n\n' +
          "event: complete\ndata: " + JSON.stringify({ id: "trial-" + draft.revision, revision: draft.revision, output: "金额列左对齐。" }) + "\n\n" });
      }
      if (path === draftPath + "/refine") {
        calls.push(request.postDataJSON());
        return route.fulfill({ json: { id: "change", revision: 3, selected: "金额列", diff: {
          before: "金额左对齐。保留币种。", after: "金额右对齐。保留币种。", offset: 0, removed: "金额左对齐", added: "金额右对齐",
        } } });
      }
      if (path === draftPath + "/refinements/change/apply") {
        assert.deepEqual(request.postDataJSON(), { expected: 3 });
        draft.revision++;
        spec.instructions = "金额右对齐。保留币种。";
        return route.fulfill({ json: { revision: draft.revision } });
      }
      if (path === draftPath + "/publish") {
        const input = request.postDataJSON();
        calls.push(input);
        if (input.scope === "personal") {
          published = { id: releaseID, draft: draftID, revision: 4, name: spec.name, scope: "personal", team: "team", capabilities: spec.capabilities };
        }
        return route.fulfill({ status: 201, json: { id: releaseID, revision: 4, scope: input.scope,
          state: input.scope === "enterprise" ? "pending" : "published" } });
      }
      if (path === "/api/projects") {
        assert.deepEqual(request.postDataJSON(), { team: "team", goal: "核对本期报价" });
        return route.fulfill({ status: 201, json: { id: projectID } });
      }
      if (path === projectPath + "/general-runs") {
        calls.push(request.postDataJSON());
        details.run = { id: "run", general: true, status: "running", studioAssistant: published, attempts: [] };
        return route.fulfill({ status: 202, json: { id: "run" } });
      }
    }
    if (path.endsWith("/session-events")) return route.fulfill({ contentType: "text/event-stream", body: ": connected\n\n" });
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "构建用户", role: "member", enabled: true },
      "/api/teams": [{ id: "team", name: "运营团队", joined: true, default: true }],
      "/api/studio": [{ id: draftID, revision: draft.revision, name: draft.spec.name, team: "team" }],
      "/api/studio/published": published ? [published] : [],
      "/api/general-tiers": ["default"],
      [draftPath]: draft,
      [draftPath + "/skills"]: [{ id: "skill", name: "报表编排 Skill", version: "v1" }],
      [draftPath + "/capabilities"]: [
        { id: "office", label: "表格与文档处理", enabled: spec.capabilities.includes("office"), available: true },
        { id: "knowledge", label: "企业知识库", enabled: spec.capabilities.includes("knowledge"), available: true },
      ],
      [projectPath]: details,
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/agents/studio");
  await page.getByRole("button", { name: "创建助手", exact: true }).click();
  await page.waitForURL("**/agents/studio/" + draftID);
  const personal = page.getByRole("button", { name: "保存自用" });
  await personal.waitFor();
  assert.equal(await personal.isDisabled(), true);
  await page.getByLabel("构建需求").fill("帮我做报价核对");
  await page.locator(".studio-dropzone input[type=file]").setInputFiles({
    name: "参考格式.csv", mimeType: "text/csv", buffer: Buffer.from("金额,币种\n25,CNY"),
  });
  await page.getByRole("button", { name: "发送构建需求" }).click();
  await page.getByText("已生成报价核对草稿。").waitFor();
  await page.getByRole("button", { name: "配置清单", exact: true }).click();
  await page.getByText("报表编排 Skill · v1").waitFor();
  await page.getByLabel("企业知识库", { exact: true }).click();
  await page.waitForFunction(() => Array.from(document.querySelectorAll(".studio-check")).some(
    label => label.textContent.trim() === "企业知识库" && label.querySelector("input")?.checked,
  ));
  await page.getByLabel("试聊消息").fill("检查格式");
  await page.getByRole("button", { name: "发送试聊" }).click();
  const answer = page.getByRole("textbox", { name: /试聊答复/ });
  await answer.focus();
  await page.getByLabel("这份试聊达到了预期").waitFor();
  await answer.evaluate(field => {
    field.setSelectionRange(0, 3);
    field.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
  });
  assert.deepEqual(await answer.evaluate(field => [field.selectionStart, field.selectionEnd]), [0, 3]);
  await page.getByLabel("调整意见").fill("以后金额列右对齐");
  await page.getByRole("button", { name: "预览修改" }).click();
  const diff = page.getByRole("region", { name: "指令修改对比" });
  await diff.locator("del").getByText("金额左对齐").waitFor();
  await diff.locator("ins").getByText("金额右对齐").waitFor();
  await diff.getByRole("button", { name: "应用修改" }).click();
  await page.getByText("已应用修改。可在新版本上再试一次。").waitFor();
  assert.ok(await page.getByRole("button", { name: "申请全企上架" }).isDisabled());
  await page.getByRole("button", { name: "发送试聊" }).click();
  await page.getByLabel("这份试聊达到了预期").check();
  await page.getByRole("button", { name: "申请全企上架" }).click();
  await page.getByText("已提交全企认证，管理员批准后即可使用。").waitFor();
  await personal.click();
  await page.getByText("已保存为个人私有助手").waitFor();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.getByRole("button", { name: "在对话中使用" }).click();
  await page.waitForURL("**/chat");
  await page.getByText("使用助手 报价核对助手").waitFor();
  await page.getByRole("textbox", { name: "消息", exact: true }).fill("核对本期报价");
  const dispatched = page.waitForResponse(response => response.url().endsWith("/general-runs"));
  await page.getByRole("button", { name: "发送消息" }).click();
  assert.equal((await dispatched).status(), 202);
  await page.waitForURL("**/chat/" + projectID);
  assert.deepEqual(calls, [
    { expected: 3, trial: "trial-3", start: 0, end: 3, feedback: "以后金额列右对齐" },
    { expected: 4, scope: "enterprise", externalAgents: [], trial: "trial-4" },
    { expected: 4, scope: "personal", externalAgents: [] },
    { expected: 1, goal: "核对本期报价", studioRelease: releaseID, sources: [] },
  ]);
  assert.deepEqual(errors, []);
  console.log("Studio built from a sample, refined a selected result, released a frozen version and used it in chat.");
} finally {
  await browser.close();
}

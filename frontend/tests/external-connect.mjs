import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 360, height: 780 },
  });
  const id = "3f576f7a-3e72-4dd1-82fc-a186b2302e09";
  const member = { id: "other", name: "示例同事" };
  const submitted = [];
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  let connection;
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/agents/adapters" && request.method() === "POST") {
      submitted.push(request.postDataJSON());
      connection = { id, revision: 1, name: "异步报告助手", phase: "draft", owner: true, template: "webhook", testersRevision: 1 };
      return route.fulfill({ status: 201, json: { id, revision: 1 } });
    }
    if (path === `/api/agents/${id}/validate`) {
      assert.ok(request.headers()["idempotency-key"]);
      submitted.push(request.postDataJSON());
      connection.activeTrial = "trial";
      return route.fulfill({ json: { id: "trial", status: "remote", output: "正在生成" } });
    }
    if (path === `/api/agents/${id}/evidence/trial/receipt`) {
      assert.deepEqual(request.postDataJSON(), { action: "check" });
      connection.phase = "beta";
      delete connection.activeTrial;
      return route.fulfill({ json: { status: "passed", output: "示例报告已返回" } });
    }
    if (path === `/api/agents/${id}/external-testers` && request.method() === "PUT") {
      assert.deepEqual(request.postDataJSON(), { revision: 1, expected: 1, members: [member.id] });
      connection.testers = [member.id];
      connection.testersRevision = 2;
      return route.fulfill({ json: { revision: 2 } });
    }
    if (path === `/api/agents/${id}/release-requests` && request.method() === "POST") {
      assert.deepEqual(request.postDataJSON(), { revision: 1, buildDigest: "test-build", evidenceDigest: "test-proof", previous: "" });
      connection.phase = "pending_verify";
      return route.fulfill({ json: { id: "release" } });
    }
    if (path === "/api/agents/admissions") return route.fulfill({ json: connection ? [connection] : [] });
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "接入人员", role: "member", enabled: true },
      "/api/teams": [{ id: "team", name: "测试团队", joined: true, default: true }],
      "/api/agents/adapter-templates": [
        { id: "webhook", name: "异步 Webhook", route: "", protocol: "async_webhook" },
        { id: "manus", name: "Manus", route: "/v2/task.create", protocol: "async_webhook" },
      ],
      [`/api/agents/${id}/external-testers`]: [member],
      [`/api/agents/${id}/builds/1`]: { build: { digest: "test-build" }, evidence: { digest: "test-proof", passed: true } },
    };
    return route.fulfill({ json: data[path] ?? [] });
  });
  await page.goto("/agents/connect");
  const templates = page.getByRole("group", { name: "接入方式" });
  await templates.getByRole("button", { name: "Webhook" }).click();
  assert.equal(await templates.getByRole("button").count(), 6);
  await page.getByRole("button", { name: "继续", exact: true }).click();
  await page.getByLabel("Agent 名称").fill("异步报告助手");
  await page.getByLabel("服务地址").fill("https://reports.example.test");
  await page.getByLabel("服务密钥（可选）").fill("local-test-credential");
  await page.getByLabel("提交路径").fill("/submit");
  await page.getByRole("button", { name: "保存并测试" }).click();
  await page.getByLabel("测试消息", { exact: true }).fill("返回一条示例报告");
  assert.equal(await page.getByRole("button", { name: "发送测试" }).isDisabled(), true);
  await page.getByLabel("同意发送这条测试消息，费用由服务方决定").check();
  await page.getByRole("button", { name: "发送测试" }).click();
  await page.getByRole("button", { name: "查询答复" }).waitFor();
  await page.reload();
  await page.getByRole("button", { name: "异步报告助手" }).click();
  await page.getByRole("button", { name: "查询答复" }).click();
  await page.getByText("示例报告已返回").waitFor();
  await page.getByRole("button", { name: "选择测试成员" }).click();
  await page.getByLabel(member.name).check();
  await page.getByRole("button", { name: "保存测试组" }).click();
  await page.getByText("测试组已保存").waitFor();
  await page.getByRole("button", { name: "申请全企认证" }).click();
  await page.getByText("待认证", { exact: true }).waitFor();
  assert.equal(submitted[0].adapter.webhook.submit.path, "/submit");
  assert.equal(submitted[0].key, "local-test-credential");
  assert.deepEqual(submitted[1], { revision: 1, input: "返回一条示例报告", admissionTest: true, authorizeOneCall: true });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await page.getByRole("button", { name: "在对话中使用" }).click();
  await page.waitForURL("**/chat");
  assert.equal(await page.getByLabel("外部 Agent", { exact: true }).inputValue(), id + "/1");
  assert.deepEqual(errors, []);
  console.log("External connection wizard configured, tested, shared and submitted a fixture Agent.");
} finally {
  await browser.close();
}

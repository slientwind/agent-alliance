import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179", viewport: { width: 390, height: 844 } });
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const admin = { id: "admin", name: "管理员", email: "admin@example.test", role: "admin", enabled: true };
  const employee = { id: "employee", name: "林诀", email: "employee@example.test", role: "member", enabled: true, accountRevision: "4",
    teams: [{ id: "sales", name: "运营团队" }, { id: "stock", name: "仓库团队" }] };
  const successor = { id: "successor", name: "王云", email: "successor@example.test", role: "member", enabled: true };
  const team = "/api/spaces/space-stock";
  const events = team + "/event-triggers";
  const rules = "/api/admin/governance/dlp/rules";
  const evalRuns = team + "/eval-suites/suite/runs";
  let trigger;
  let handoff;
  let rule;
  let evaluation;
  await page.route("**/api/**", route => {
    const request = route.request(), url = new URL(request.url()), pathname = url.pathname;
    if (request.method() === "PUT" && pathname.startsWith(events + "/")) {
      trigger = { id: pathname.split("/").at(-1), ...request.postDataJSON(), revision: trigger ? 2 : 1 };
      return route.fulfill({ json: { id: trigger.id, revision: trigger.revision,
        ...(trigger.revision === 1 || trigger.rotate ? { webhookPath: "/api/webhooks/events/test-signed-path", secret: "fixture-signing-key" } : {}) } });
    }
    if (pathname === "/api/admin/members/employee/departure-handoff") {
      assert.equal(url.searchParams.get("mode"), "transfer");
      const teamId = url.searchParams.get("teamId");
      return route.fulfill({ json: { mode: "transfer", teamId, accountRevision: "4", requiresSuccessor: true,
        projects: teamId === "stock" ? 3 : 1, agents: 1, drafts: 1, rules: 2, automations: 1,
        personal: { projects: 4, drafts: 2, collections: 2, oboGrants: 1 },
        shared: { collections: 1, documents: 2, connectors: 2, businessBindings: 2, writePolicies: 1, events: 2, approvals: 2 },
        successors: teamId === "stock" ? [successor] : [] } });
    }
    if (request.method() === "PATCH" && pathname === "/api/admin/members/employee") {
      handoff = request.postDataJSON();
      employee.accountRevision = "5";
      employee.teams = employee.teams.filter(team => team.id !== handoff.teamId);
      return route.fulfill({ status: 204 });
    }
    if (request.method() === "PUT" && pathname.startsWith(rules + "/")) {
      rule = { id: pathname.split("/").at(-1), ...request.postDataJSON(), revision: rule ? 2 : 1 };
      return route.fulfill({ json: { revision: rule.revision } });
    }
    if (pathname === "/api/admin/governance/dlp/inspect") {
      assert.deepEqual(request.postDataJSON(), { sample: "内测字段", field: "cost_floor", sensitivity: "" });
      return route.fulfill({ json: { findings: [{ kind: "floor_price", sensitivity: "secret" }] } });
    }
    if (request.method() === "POST" && pathname === evalRuns) {
      evaluation = request.postDataJSON();
      return route.fulfill({ status: 202, json: { id: "eval-1" } });
    }
    if (pathname.endsWith("-events")) return route.fulfill({ contentType: "text/event-stream", body: ": heartbeat\n\n" });
    const now = "2026-11-25T00:00:00Z";
    const fixture = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": admin,
      "/api/admin/members": [admin, employee, successor],
      "/api/admin/members/employee/responsibilities": { finalProjects: 3, reviewVersions: 4, delegations: 1 },
      "/api/teams": [{ id: "stock", name: "仓库团队", joined: true, default: true }, { id: "sales", name: "运营团队", joined: true }],
      "/api/spaces": [{ id: "space-stock", kind: "team", name: "仓库团队", teamId: "stock", role: "space_admin" }],
      [events]: trigger ? [trigger] : [],
      [events + "/" + trigger?.id + "/hits"]: [{ id: "hit", status: "completed", firstAt: now, lastAt: now, readyAt: now, hits: 4, projectId: "task" }],
      [team + "/members"]: [admin, successor],
      [team + "/business/connectors"]: [{ id: "erp", name: "库存 ERP", enabled: true }],
      "/api/studio/published": [{ id: "assistant", name: "库存助手", team: "stock", scope: "team", revision: 1 }],
      [rules]: rule ? [{ ...rule, match: undefined }] : [],
      "/api/admin/models/providers": [{ id: "provider", name: "测试模型", kind: "openai", enabled: true,
        models: [{ id: "candidate", name: "候选", enabled: true, capabilities: ["text"] }] }],
      [team + "/eval-suites"]: [{ id: "suite", name: "月度回归", caseCount: 2 }],
      [evalRuns]: evaluation ? [{ id: "eval-1", kind: evaluation.kind, state: "completed", report: { baselinePassRate: 1, candidatePassRate: 0.5,
        baselineLatencyMs: 250, candidateLatencyMs: 480, caseCount: 2, cases: [] } }] : [],
    };
    return route.fulfill({ json: fixture[pathname] ?? [] });
  });

  await page.goto("/automations");
  const webhook = page.getByRole("region", { name: "业务事件 Webhook 触发器" });
  await webhook.getByRole("button", { name: "新增 Webhook 触发器" }).click();
  const eventForm = webhook.getByRole("form", { name: "配置 Webhook 触发器" });
  await eventForm.getByLabel("触发器名称").fill("低库存告警");
  await eventForm.getByLabel("业务来源").selectOption("erp");
  await eventForm.getByLabel("事件筛选（CEL）").fill("event.quantity < 5");
  await eventForm.getByLabel("传入任务的字段").fill("id, quantity");
  await eventForm.getByLabel("执行助手").selectOption("assistant");
  await eventForm.getByRole("button", { name: "添加通知对象" }).click();
  await eventForm.getByLabel("成员 1").selectOption(successor.id);
  await eventForm.getByLabel("渠道 1").selectOption("dingtalk");
  await eventForm.getByLabel("允许对所选来源发起 L2 写回审批").check();
  await eventForm.getByRole("button", { name: "保存触发器" }).click();
  const signature = webhook.getByRole("region", { name: "Webhook 签名参数" });
  assert.match(await signature.getByLabel("Webhook 地址").inputValue(), /\/api\/webhooks\/events\/test-signed-path$/);
  await signature.getByRole("button", { name: "关闭签名参数" }).click();
  await webhook.getByRole("button", { name: "查看命中记录" }).click();
  await webhook.getByRole("cell", { name: "已完成" }).waitFor();
  await webhook.getByRole("cell", { name: "4", exact: true }).waitFor();
  assert.deepEqual(trigger.notifyTargets, [{ memberId: successor.id, channel: "dingtalk" }]);
  assert.deepEqual(trigger.eventFields, ["id", "quantity"]);
  assert.equal(trigger.allowWritePreview, true);

  await page.goto("/admin/members");
  await page.getByRole("button", { name: /林诀 · employee@example.test/ }).click();
  const member = page.getByRole("region", { name: "成员资料" });
  await member.getByLabel("变更类型").selectOption("transfer");
  await member.getByLabel("移出的团队").selectOption("sales");
  await member.getByText("暂无符合条件的接手人。").waitFor();
  assert.equal(await member.getByRole("button", { name: "确认变更" }).isDisabled(), true);
  await member.getByLabel("移出的团队").selectOption("stock");
  await member.getByRole("region", { name: "个人资产封存" }).getByText(/4 个私有项目/).waitFor();
  await member.getByRole("region", { name: "团队资产与职责移交" }).getByText(/3 个项目/).waitFor();
  await member.getByRole("combobox", { name: "接手人", exact: true }).selectOption(successor.id);
  await member.getByLabel("变更原因").fill("原仓库团队调岗");
  await member.getByLabel("管理员当前密码").fill("fixture-password");
  await member.getByRole("button", { name: "确认变更" }).click();
  await member.waitFor({ state: "detached" });
  assert.deepEqual(handoff, { expected: "4", role: "member", enabled: true, employment: "transfer", teamId: "stock",
    successor: successor.id, reason: "原仓库团队调岗", password: "fixture-password" });

  await page.getByRole("navigation", { name: "企业管理导航" }).getByRole("button", { name: "治理与回归体检" }).click();
  const dlp = page.getByRole("region", { name: "DLP 脱敏规则" });
  await dlp.getByRole("button", { name: "新增脱敏规则" }).click();
  const dlpForm = dlp.getByRole("form", { name: "编辑脱敏规则" });
  await dlpForm.getByLabel("规则名称").fill("底价保护");
  await dlpForm.getByLabel("识别类型").selectOption("floor_price");
  await dlpForm.getByLabel("密级").selectOption("secret");
  await dlpForm.getByLabel("字段名").fill("cost_floor");
  await dlpForm.getByRole("button", { name: "保存脱敏规则" }).click();
  await dlp.getByRole("button", { name: "编辑规则 底价保护" }).waitFor();
  assert.equal(rule.match, "cost_floor");
  await dlp.getByText("检测样例", { exact: true }).click();
  await dlp.getByLabel("测试字段名").fill("cost_floor");
  await dlp.getByLabel("用于检测的样例").fill("内测字段");
  await dlp.getByRole("button", { name: "运行脱敏检测" }).click();
  await dlp.getByText("底价字段 · 严格保密 · 1 处").waitFor();

  const suite = page.getByRole("region", { name: "黄金回归体检" });
  await suite.getByLabel("候选模型").selectOption("candidate");
  await suite.getByRole("button", { name: "运行回归体检" }).click();
  await suite.getByText("100% → 50%").waitFor();
  assert.deepEqual(evaluation, { kind: "model", baselineTier: "default", candidateModelId: "candidate" });
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}

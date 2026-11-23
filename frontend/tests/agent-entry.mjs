import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: origin,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: dir,
  QUICK_ENDPOINT: endpoint,
} = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL: origin });
  assert.ok(
    (
      await admin.request.post("/api/login", {
        data: { email: "review@sample.invalid", password },
      })
    ).ok(),
  );
  const invite = await (
    await admin.request.post("/api/admin/invitations", { data: {} })
  ).json();
  const p = await browser.newPage({
    baseURL: origin,
    viewport: { width: 390, height: 844 },
  });
  assert.ok(
    (
      await p.request.post("/api/register", {
        data: {
          token: invite.token,
          name: "Agent 使用者",
          email: "agent-entry@sample.invalid",
          password,
        },
      })
    ).ok(),
  );
  const team = (
    await (
      await admin.request.post("/api/admin/teams", {
        data: { name: "业务助手组" },
      })
    ).json()
  ).id;
  const who = await (await p.request.get("/api/me")).json();
  assert.equal(
    (
      await admin.request.put(
        "/api/admin/teams/" + team + "/members/" + who.id,
        { data: { joined: true, reason: "使用业务助手" } },
      )
    ).status(),
    204,
  );
  const resource = await admin.request.post("/api/resources", {
    data: {
      team,
      name: "业务助手文本资源",
      endpoint,
      model: "fixture",
      key: "synthetic",
    },
  });
  assert.equal(resource.status(), 201);
  const model = (await resource.json()).id;
  const description = "从访谈记录整理客户需求，并列出原文依据";
  await p.goto("/");
  await p.getByRole("button", { name: "我的工作区" }).click();
  assert.equal(
    await p.getByRole("button", { name: "团队首次配置" }).count(),
    0,
  );
  assert.equal(await p.getByRole("button", { name: "企业管理" }).count(), 0);
  await p.getByRole("button", { name: "关闭", exact: true }).click();
  const launch = p.getByRole("button", { name: "选择或创建 Agent" });
  await launch.click();
  const panel = p.getByRole("dialog", { name: "Agent", exact: true });
  await panel.getByRole("button", { name: "创建 Agent" }).click();
  await panel.getByLabel("希望 Agent 完成什么任务？").fill(description);
  const options = panel.locator("details").filter({ has: p.locator("select") });
  if (!(await options.evaluate((e) => e.open)))
    await options.locator("summary").click();
  const select = panel.getByLabel("文本模型");
  const option = select.locator('option[value^="' + model + '/"]').first();
  await select.selectOption(await option.getAttribute("value"));
  const create = p.waitForResponse(
    (r) => r.url().endsWith("/api/agents") && r.request().method() === "POST",
  );
  await panel.getByRole("button", { name: "创建并准备试运行" }).click();
  const receipt = await create;
  assert.equal(receipt.status(), 201);
  const draft = await receipt.json();
  await panel.getByLabel("试运行输入").fill("客户希望导出每周的需求清单");
  await panel.getByLabel("结果应包含").fill("RECEIPT");
  await panel.getByRole("checkbox").check();
  await panel.getByRole("button", { name: "运行示例" }).click();
  await panel.getByRole("button", { name: "申请发布此版本", exact: true }).waitFor();
  assert.equal(
    await panel.getByRole("button", { name: "发布已验证版本" }).count(),
    0,
  );
  assert.ok(
    (
      await admin.request.post("/api/agents/" + draft.id + "/publish", {
        data: { revision: 1 },
      })
    ).ok(),
  );
  await panel.getByRole("button", { name: "关闭", exact: true }).click();
  await launch.click();
  const alternate = "请根据客户访谈提取需求，并注明原文";
  const discoveryWrites = [];
  const trackWrite = (req) => {
    if (
      new URL(req.url()).pathname.startsWith("/api/") &&
      !["GET", "HEAD"].includes(req.method())
    )
      discoveryWrites.push(req.method() + " " + new URL(req.url()).pathname);
  };
  p.on("request", trackWrite);
  await panel.getByLabel("搜索 Agent").fill(alternate);
  const choice = panel.getByRole("button", { name: new RegExp(description) });
  await choice.waitFor();
  await choice.getByText(/匹配搜索词/).waitFor();
  await panel.screenshot({ path: dir + "/agent-picker-mobile.png" });
  const countBeforeReuse = (await (await p.request.get("/api/agents")).json())
    .length;
  await panel.getByRole("button", { name: "创建 Agent", exact: true }).click();
  assert.equal(
    await panel.getByLabel("希望 Agent 完成什么任务？").inputValue(),
    alternate,
  );
  await panel.getByText(/可直接使用的相似 Agent/).click();
  const similar = panel
    .locator(".similar-agents > div")
    .filter({ hasText: description });
  await similar.getByText(/任务词重合/).waitFor();
  await panel.screenshot({ path: dir + "/agent-description-reuse.png" });
  await similar.getByRole("button", { name: "使用此 Agent" }).click();
  assert.equal(
    await p.getByRole("textbox", { name: "消息", exact: true }).inputValue(),
    alternate,
  );
  assert.equal(
    (await (await p.request.get("/api/agents")).json()).length,
    countBeforeReuse,
  );
  p.off("request", trackWrite);
  assert.deepEqual(discoveryWrites, []);
  await p
    .getByRole("textbox", { name: "消息", exact: true })
    .fill("从本次访谈生成培训材料");
  await p
    .locator('input[type="file"]')
    .last()
    .setInputFiles({
      name: "访谈.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("员工希望有独立查询入口。"),
    });
  await p.getByRole("button", { name: "发送消息" }).click();
  await p.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const project = new URL(p.url()).hash.slice(1),
    path = "/api/projects/" + project;
  const before = await (await p.request.get(path)).json();
  assert.equal(before.team, team);
  assert.equal(before.run, null);
  assert.equal(before.plan.nodes.length, 1);
  assert.equal(before.plan.nodes[0].agent, draft.id);
  assert.equal(before.plan.sources[0].version, 1);
  await p.getByLabel(/授权本次 1/).check();
  await p.getByRole("button", { name: "确认并执行" }).click();
  await p.getByRole("button", { name: "审核此版本" }).click();
  await p.getByRole("button", { name: "确认并固定以上版本" }).click();
  await p.waitForFunction(() => document.body.textContent.includes("已交付"));
  const delivered = await (await p.request.get(path)).json();
  assert.equal(delivered.stage, "delivered");
  await p.getByRole("button", { name: "新对话", exact: true }).click();
  await launch.click();
  await choice.click();
  await p
    .getByRole("textbox", { name: "消息", exact: true })
    .fill("整理另一条简短需求");
  await p.getByRole("button", { name: "发送消息" }).click();
  await p.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const second = await (
    await p.request.get("/api/projects/" + new URL(p.url()).hash.slice(1))
  ).json();
  assert.equal(second.plan.nodes[0].revision, 1);
  assert.equal(second.run, null);
  const legacy = await p.request.post("/api/projects", {
    data: { goal: "沿用单助手请求", team, agent: draft.id, agentRevision: 1 },
  });
  assert.equal(legacy.status(), 201);
  const legacyProject = await (
    await p.request.get("/api/projects/" + (await legacy.json()).id)
  ).json();
  assert.deepEqual(
    legacyProject.plan.nodes.map((n) => [n.agent, n.revision]),
    [[draft.id, 1]],
  );
  assert.equal(legacyProject.run, null);
  const previous = (await (await p.request.get("/api/projects")).json()).length;
  const denied = await p.request.post("/api/projects", {
    data: {
      goal: "过期选择",
      team: second.team,
      agent: draft.id,
      agentRevision: 999,
    },
  });
  assert.equal(denied.status(), 422);
  assert.equal(
    (await (await p.request.get("/api/projects")).json()).length,
    previous,
  );
  const defaultTeam = (await (await p.request.get("/api/teams")).json()).find(
    (t) => t.default && t.joined,
  );
  const otherModel = await admin.request.post("/api/resources", {
    data: {
      team: defaultTeam.id,
      name: "访谈核对模型",
      endpoint,
      model: "fixture",
      key: "synthetic",
    },
  });
  assert.equal(otherModel.status(), 201);
  const auditModel = (await otherModel.json()).id;
  const reviewer = await p.request.post("/api/agents", {
    data: {
      name: "访谈依据核对助手",
      instructions: "核对上一步整理出的访谈依据 COMPOSE_AUDIT_42",
      resource: auditModel,
      resourceRevision: 1,
    },
  });
  assert.equal(reviewer.status(), 201);
  const audit = await reviewer.json();
  const trial = await p.request.post("/api/agents/" + audit.id + "/validate", {
    data: {
      revision: 1,
      input: "核对一条访谈依据",
      expected: "RECEIPT",
      authorizeOneCall: true,
    },
  });
  assert.equal((await trial.json()).status, "passed");
  assert.ok(
    (
      await admin.request.post("/api/agents/" + audit.id + "/publish", {
        data: { revision: 1 },
      })
    ).ok(),
  );

  await p.getByRole("button", { name: "新对话", exact: true }).click();
  const goal = p.getByRole("textbox", { name: "消息", exact: true });
  await goal.fill("先整理访谈，再核对依据。");
  await launch.click();
  await panel.getByRole("button", { name: "组合 Agent" }).click();
  const seq = panel.getByRole("region", { name: "协作顺序" });
  const add = async (term) => {
    await panel.getByLabel("搜索 Agent").fill(term);
    await panel
      .locator("button.agent-choice")
      .filter({ hasText: term })
      .click();
  };
  await add("访谈依据核对助手");
  await seq.getByText(/^本次组合由.+承担费用$/).waitFor();
  assert.equal(await seq.getByLabel("项目归属").count(), 0);
  assert.equal(await seq.locator("li small").count(), 0);
  await add(description);
  assert.equal(
    await seq.locator("li small").filter({ hasText: "承担费用" }).count(),
    2,
  );
  const ownership = seq.getByLabel("项目归属");
  await ownership.selectOption(team);
  await seq.getByRole("button", { name: "将第 2 步提前" }).click();
  assert.equal(await ownership.inputValue(), team);
  await add("访谈依据核对助手");
  await seq.getByRole("button", { name: "移除第 3 步" }).click();
  assert.deepEqual(await seq.locator("li strong").allTextContents(), [
    description,
    "访谈依据核对助手",
  ]);
  await seq.getByRole("button", { name: "使用此组合" }).click();
  assert.equal(await goal.inputValue(), "先整理访谈，再核对依据。");
  await p.getByRole("button", { name: "调整组合" }).click();
  await seq.getByRole("button", { name: "使用此组合" }).click();
  const expectedOrder = [draft.id, audit.id];
  for (const attach of [false, true]) {
    if (attach) {
      await p.getByRole("button", { name: "新对话", exact: true }).click();
      await goal.fill("整理附件里的访谈记录，核对依据并交付。");
      await p
        .locator('input[type="file"]')
        .last()
        .setInputFiles({
          name: "样例.txt",
          mimeType: "text/plain",
          buffer: Buffer.from("访谈编号 17，申请人希望保留完整原话。"),
        });
      await launch.click();
      await panel.getByRole("button", { name: "组合 Agent" }).click();
      await add(description);
      await add("访谈依据核对助手");
      await ownership.selectOption(team);
      await seq.screenshot({ path: dir + "/agent-sequence-mobile.png" });
      await seq.getByRole("button", { name: "使用此组合" }).click();
    }
    await p.getByRole("button", { name: "发送消息" }).click();
    await p.getByRole("heading", { name: "协作方案 v1" }).waitFor();
    const id = new URL(p.url()).hash.slice(1);
    const read = async () =>
      await (await p.request.get("/api/projects/" + id)).json();
    const proposed = await read();
    assert.equal(proposed.team, team);
    assert.equal(proposed.run, null);
    assert.deepEqual(
      proposed.plan.nodes.map((n) => n.agent),
      expectedOrder,
    );
    assert.deepEqual(
      proposed.plan.nodes.map((n) => n.revision),
      [1, 1],
    );
    assert.deepEqual(
      proposed.plan.nodes.map((n) => n.dependsOn),
      [[], [1]],
    );
    assert.deepEqual(
      proposed.plan.sources.map((s) => s.version),
      attach ? [1] : [],
    );
    if (!attach) continue;
    await p.getByLabel(/授权本次 2/).check();
    await p.getByRole("button", { name: "确认并执行" }).click();
    for (let i = 0; i < 2; i++)
      await p.getByRole("button", { name: "审核此版本" }).first().click();
    await p.getByRole("button", { name: "确认并固定以上版本" }).click();
    await p.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
    const result = await read();
    assert.equal(result.stage, "delivered");
    assert.equal(result.costSummary.calls, 2);
    assert.deepEqual(
      result.costDetails.map((c) => c.payer),
      ["业务助手组", defaultTeam.name],
    );
    assert.equal(result.artifacts[1].body, "RECEIPT 已核对上一步回执");
  }
  // A temporary Agent belongs to its own team. Inspecting, using, or removing it
  // must not silently replace the destination of the member's ordinary briefs.
  await p.getByRole("button", { name: "新对话", exact: true }).click();
  await goal.fill("整理本周访谈待办");
  assert.equal(await p.getByRole("button", { name: /项目选项/ }).count(), 0);
  await p.getByRole("button", { name: "我的工作区" }).click();
  await p.getByText("协作与授权", { exact: true }).click();
  await p.getByRole("button", { name: "更改项目归属", exact: true }).click();
  await p.getByLabel("项目所属团队").selectOption(team);
  await launch.click();
  await add("访谈依据核对助手");
  await p.locator(".picked-agent").getByRole("button", { name: "移除", exact: true }).click();
  await p.getByRole("button", { name: /项目选项.*业务助手组/ }).waitFor();
  assert.equal(await goal.inputValue(), "整理本周访谈待办");
  await launch.click();
  await add("访谈依据核对助手");
  const submitDestination = async () => {
    const posted = p.waitForResponse(r => r.request().method() === "POST" && r.url().endsWith("/api/projects"));
    await p.getByRole("button", { name: "发送消息" }).click();
    const response = await posted;
    assert.equal(response.status(), 201, await response.text());
    const project = (await response.json()).id;
    await p.waitForURL(url => url.hash === "#" + project);
    return (await p.request.get("/api/projects/" + project)).json();
  };
  const selectedProject = await submitDestination();
  assert.equal(selectedProject.team, defaultTeam.id);
  assert.equal(selectedProject.plan.nodes[0].agent, audit.id);
  await p.getByRole("button", { name: "新对话", exact: true }).click();
  await goal.fill("汇总下次培训的问题");
  await p.getByRole("button", { name: /项目选项.*业务助手组/ }).waitFor();
  const ordinaryProject = await submitDestination();
  assert.equal(ordinaryProject.team, team);
  assert.equal(ordinaryProject.run, null);
  const total = (await (await p.request.get("/api/projects")).json()).length;
  for (const invalid of [
    { agents: expectedOrder, agentRevisions: [1] },
    { agents: expectedOrder, agentRevisions: [1, 999] },
    {
      agent: draft.id,
      agentRevision: 1,
      agents: expectedOrder,
      agentRevisions: [1, 1],
    },
  ]) {
    assert.equal(
      (
        await p.request.post("/api/projects", {
          data: { goal: "失效组合", team, ...invalid },
        })
      ).status(),
      422,
    );
  }
  const revoke = await admin.request.put(
    "/api/admin/teams/" + team + "/members/" + who.id,
    { data: { joined: false, reason: "结束访谈任务" } },
  );
  assert.equal(revoke.status(), 204);
  const lostStep = await p.request.post("/api/projects", {
    data: {
      goal: "权限已变化的组合",
      team: defaultTeam.id,
      agents: [audit.id, draft.id],
      agentRevisions: [1, 1],
    },
  });
  assert.equal(lostStep.status(), 422);
  assert.equal(
    (await (await p.request.get("/api/projects")).json()).length,
    total,
  );
  await writeFile(
    dir + "/agent-entry.json",
    JSON.stringify(
      {
        input: "synthetic",
        runtime: ["Chromium", "Go", "PostgreSQL", "Temporal", "Eino"],
        provider: "local HTTPS fixture",
        createdBy: "member",
        publishedBy: "admin",
        draftRevision: 1,
        documentVersion: 1,
        deliveredVersion: delivered.artifacts[0].version,
        discovery: {
          query: alternate,
          publishedRevision: draft.revision,
          retainedBrief: true,
          writes: discoveryWrites,
        },
        singleAgentSteps: before.plan.nodes.length,
        temporaryAgent: { agentProject: selectedProject.team, nextBrief: ordinaryProject.team, retainedExplicitDestination: true },
        legacySingleAccepted: true,
        combination: {
          steps: 2,
          separatelyBilledTeams: 2,
          reordered: true,
          pinnedWithAndWithoutAttachment: true,
          consumedPreviousOutput: true,
          authorizedCalls: 2,
          reviewedAndDelivered: true,
          invalidSelectionsRolledBack: true,
          revokedSecondStepRejected: true,
        },
        runBeforeConfirmation: second.run,
        invalidSelectionRolledBack: true,
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

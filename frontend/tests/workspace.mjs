import { verifyDeliveryEditions } from "./delivery-editions.mjs";
import { checkTeamRules } from "./team-rules.mjs";
import { checkImprovements } from "./improvements.mjs";
import { checkCandidates } from "./candidates.mjs";
import { checkPreferences } from "./preferences.mjs";
import { checkBudgetInbox } from "./budget-inbox.mjs";
import { checkRevisionDrafts } from "./revision-drafts.mjs";
import { checkPlanDrafts } from "./plan-drafts.mjs";
import { reviewInbox } from "./review-inbox.mjs";
import { checkResourceBudget } from "./resource-budgets.mjs";
import { reviewIncrease } from "./budget-requests.mjs";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { chromium } from "playwright";
(async () => {
  const browser = await chromium.launch();
  try {
    const workspace = await browser.newContext({
      baseURL: process.env.SMOKE_ORIGIN,
      viewport: { width: 1360, height: 850 },
    });
    const p = await workspace.newPage();
    const errors = [];
    p.on("pageerror", (e) => errors.push(String(e)));
    await p.goto(process.env.SMOKE_ORIGIN);
    await p.getByLabel("邮箱").fill("review@sample.invalid");
    await p
      .getByLabel("密码", { exact: true })
      .fill(process.env.BROWSER_PASSWORD);
    await p.getByRole("button", { name: "登录", exact: true }).click();
    const verifyPalette = async () => {
      const results = await p.evaluate(() =>
        Array.from(document.querySelectorAll("body *"))
          .filter((el) => el.getClientRects().length)
          .flatMap((el) => {
            const s = getComputedStyle(el);
            return [s.color, s.backgroundColor, s.borderTopColor, s.stroke];
          }),
      );
      for (const value of results) {
        if (!value.startsWith("rgb")) continue;
        const [r, g, b] = value
          .match(/[0-9.]+/g)
          .slice(0, 3)
          .map(Number);
        assert.ok(Math.max(r, g, b) - Math.min(r, g, b) <= 2, value);
      }
    };
    await p.getByRole("heading", { name: "新对话", exact: true }).waitFor();
    await verifyPalette();
    await p.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/desktop-chat.png",
    });
    await checkPlanDrafts(workspace);
    const msg = p.getByRole("textbox", { name: "消息", exact: true });
    await msg.fill("请为团队准备工作简报");
    await p.getByRole("button", { name: "发送消息" }).click();
    await p.getByRole("heading", { name: "请为团队准备工作简报" }).waitFor();
    const projectCosts = p
      .locator("details.card")
      .filter({ has: p.locator(":scope > summary", { hasText: "项目费用" }) })
      .first();
    await projectCosts.locator("summary").first().click();
    await projectCosts.getByText("调整预算", { exact: true }).click();
    await projectCosts.getByLabel(/金额（CNY/).fill("0");
    await projectCosts.getByLabel("调整原因").fill("暂停本项目付费调用");
    await projectCosts.getByRole("button", { name: "确认预算调整" }).click();
    await projectCosts.getByText("项目已达到预算停止阈值。").waitFor();
    await projectCosts.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/project-budget.png",
    });
    await projectCosts.locator("summary").first().click();
    const pick = p.getByLabel("添加执行步骤");
    const writer = await pick.getByText(/Writer/).getAttribute("value"),
      checker = await pick.getByText(/Checker/).getAttribute("value");
    for (const agent of [writer, checker, writer, checker])
      await pick.selectOption(agent);
    const steps = p.locator(".plan-steps > li");
    await steps.nth(2).locator("summary").click();
    await steps
      .nth(2)
      .getByLabel(/引用步骤 2/)
      .uncheck();
    await steps.nth(3).locator("summary").click();
    await steps
      .nth(3)
      .getByLabel(/引用步骤 2/)
      .check();
    await p.getByRole("button", { name: "保存待确认方案" }).click();
    await p.getByRole("heading", { name: "协作方案 v1" }).waitFor();
    await p
      .getByRole("heading", { name: "协作方案 v1" })
      .scrollIntoViewIfNeeded();
    await p.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/desktop-plan.png",
    });
    const plan = await p
      .getByRole("heading", { name: "协作方案 v1" })
      .locator("..")
      .innerText();
    assert.match(plan, /4 次金额未知/);
    assert.match(plan, /可独立开始/);
    assert.match(plan, /引用步骤 2、3/);
    assert.match(plan, /最终确认人/);
    assert.match(plan, /文本 · 可在线修改/);
    await p.getByText("拟定协作方案", { exact: true }).click();
    await p.getByLabel("交付目标与确认要求").fill("尚未提交的第二份简报");
    await p.getByText(/本次执行采用已保存的方案 v1/).waitFor();
    await p.getByLabel(/授权本次 4/).check();
    await p.getByRole("button", { name: "确认并执行" }).click();
    await p
      .getByText("项目预算不足，需预算负责人追加或调整方案", { exact: true })
      .first()
      .waitFor();
    await projectCosts.locator("summary").first().click();
    await p.getByText(/项目已离开方案编辑阶段/).waitFor();
    await p.getByText("尚未提交的第二份简报", { exact: true }).waitFor();
    assert.equal(
      await p.getByRole("button", { name: "保存待确认方案" }).count(),
      0,
    );
    await p.getByRole("button", { name: "丢弃本页草稿", exact: true }).click();
    await reviewIncrease(browser, p, projectCosts);
    await projectCosts.getByText(/项目额度 ¥5.000001/).waitFor();
    await projectCosts.locator("summary").first().click();
    const cards = p.locator("article.card");
    await cards.nth(3).waitFor();
    await verifyPalette();
    assert.ok(
      await cards
        .first()
        .locator(".review-state")
        .evaluate((el) => Number(getComputedStyle(el).fontWeight) >= 650),
    );
    assert.ok(
      await cards
        .first()
        .locator(".preserve")
        .first()
        .evaluate((el) => Number(getComputedStyle(el).fontWeight) <= 500),
    );
    await reviewInbox(p, cards);
    for (let i = 1; i < 4; i++)
      await cards.nth(i).getByRole("button", { name: "审核此版本" }).click();
    await p.getByRole("button", { name: "确认并固定以上版本" }).waitFor();
    const feedback = cards.nth(0).locator("details.feedback");
    await feedback.locator(":scope > summary").click();
    await feedback.getByLabel("反馈类型").selectOption("change");
    await feedback.getByLabel("位置（选填）").fill("第 1 段");
    await feedback.getByLabel("反馈内容").fill("核对简报标题");
    await feedback.getByRole("button", { name: "提交反馈" }).click();
    await feedback.getByText("已要求修改", { exact: true }).waitFor();
    assert.equal(
      await p.getByRole("button", { name: "确认并固定以上版本" }).count(),
      0,
    );
    await cards.nth(0).getByRole("button", { name: "修改当前稿" }).click();
    const edit = p.locator("form.card");
    assert.match(await edit.innerText(), /步骤 4/);
    assert.doesNotMatch(await edit.innerText(), /步骤 3/);
    await edit.locator("textarea").fill("修订后的简报");
    await edit.getByLabel("修改原因").fill("业务团队更正");
    await edit.getByRole("button", { name: "保存新版本" }).click();
    await cards.nth(0).getByRole("heading", { name: /v2/ }).waitFor();
    assert.deepEqual(
      await cards
        .locator("h2")
        .allTextContents()
        .then((names) => names.map((n) => n.slice(-2))),
      ["v2", "v2", "v1", "v2"],
    );
    assert.deepEqual(await cards.locator(".review-state").allTextContents(), [
      "待审核",
      "待审核",
      "已审核",
      "待审核",
    ]);
    await feedback.getByLabel("处理方式").selectOption("submit");
    await feedback.getByLabel("处理依据").fill("新版已调整标题");
    await feedback.getByRole("button", { name: "记录决定" }).click();
    await feedback.getByText("待审核人复核", { exact: true }).waitFor();
    await p.getByRole("button", { name: "打开对话历史" }).click();
    await p
      .getByRole("dialog")
      .getByRole("button", { name: /复核修改/ })
      .click();
    await p.waitForFunction(() =>
      document.activeElement?.id.startsWith("feedback-"),
    );
    await feedback.getByLabel("处理方式").selectOption("resolve");
    await feedback.getByLabel("处理依据").fill("已对照简报核实");
    await feedback.getByRole("button", { name: "记录决定" }).click();
    await feedback.getByText("已确认解决", { exact: true }).waitFor();
    const remoteFeedback = cards.nth(3).locator("details.feedback");
    await remoteFeedback.locator(":scope > summary").click();
    await remoteFeedback.getByLabel("反馈类型").selectOption("change");
    await remoteFeedback.getByLabel("反馈内容").fill("复核交接清单");
    await remoteFeedback.getByRole("button", { name: "提交反馈" }).click();
    await remoteFeedback.getByText(/交给 Checker 修改/).click();
    await remoteFeedback.getByLabel(/授权负责此成果/).check();
    await remoteFeedback.getByRole("button", { name: "启动局部修改" }).click();
    await remoteFeedback.getByText("待审核人复核", { exact: true }).waitFor();
    await cards.nth(3).getByRole("heading", { name: /v3/ }).waitFor();
    assert.match(
      await cards.nth(2).locator(".review-state").innerText(),
      /已审核/,
    );
    await remoteFeedback.getByLabel("处理方式").selectOption("resolve");
    await remoteFeedback.getByLabel("处理依据").fill("已核对 Agent 修订");
    await remoteFeedback.getByRole("button", { name: "记录决定" }).click();
    await remoteFeedback.getByText("已确认解决", { exact: true }).waitFor();
    for (const i of [0, 1, 3])
      await cards.nth(i).getByRole("button", { name: "审核此版本" }).click();
    await cards.first().getByRole("button", { name: "修改当前稿" }).click();
    await cards.first().getByLabel("修订正文").fill("仅在本页的交付后备忘");
    await p.getByText(/本页还有未提交的修订草稿/).waitFor();
    const confirmation = p.getByRole("form", { name: "最终交付清单" });
    assert.equal(await confirmation.locator("li").count(), 4);
    await confirmation.getByText("给接收人的交付说明（选填）").click();
    await confirmation.getByLabel("成果用途").fill("用于跨部门评审");
    await confirmation
      .getByLabel("使用说明")
      .fill("先读简报，再核对交接清单。");
    await confirmation.getByLabel("已接受的使用限制").fill("仅供内部评审");
    await p.getByRole("button", { name: "确认并固定以上版本" }).click();
    await p.getByText("下载交付清单与成果正文").waitFor();
    const receipt = p
      .locator("details")
      .filter({ has: p.getByText("查看已确认的交付清单", { exact: true }) })
      .last();
    await receipt.locator("summary").click();
    await receipt.getByText("仅供内部评审", { exact: false }).waitFor();
    assert.equal(await receipt.locator("li").count(), 4);
    const manifest = await (
      await workspace.request.get(
        "/api/projects/" + new URL(p.url()).hash.slice(1) + "/delivery",
      )
    ).json();
    assert.equal(manifest.handoff.purpose, "用于跨部门评审");
    assert.deepEqual(
      manifest.items.map((i) => i.dependencies.map((d) => [d.step, d.version])),
      [
        [],
        [[1, 2]],
        [],
        [
          [2, 2],
          [3, 1],
        ],
      ],
    );
    await receipt.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/delivery-handoff.png",
    });
    await cards
      .first()
      .getByText(/项目已结束审核/)
      .waitFor();
    assert.equal(
      await cards.first().getByLabel("修订正文").inputValue(),
      "仅在本页的交付后备忘",
    );
    assert.ok(await cards.first().getByLabel("修订正文").isDisabled());
    await cards
      .first()
      .getByRole("button", { name: "放弃这份修订草稿" })
      .click();
    await verifyDeliveryEditions(p, manifest);
    const diagnostics = p
      .getByText("运行记录与授权", { exact: true })
      .locator("..");
    await diagnostics.locator(":scope > summary").click();
    // The newest run is a scoped fourth-step revision.
    await diagnostics.getByText("步骤 4 · 已完成", { exact: true }).waitFor();
    await diagnostics.getByLabel("选择运行").selectOption({ index: 1 });
    await diagnostics.getByText("步骤 1 · 已完成", { exact: true }).waitFor();
    await diagnostics.getByLabel("筛选步骤").selectOption("4");
    await diagnostics.getByText("前置步骤 2、3", { exact: false }).waitFor();
    await diagnostics
      .getByText("管理本次运行的诊断授权", { exact: true })
      .click();
    async function traceGrant(scope, action) {
      await diagnostics.getByLabel("诊断范围").selectOption(scope);
      await diagnostics.getByLabel("授权操作").selectOption(action);
      await diagnostics.getByLabel("诊断授权原因").fill("核对合成样例");
      await diagnostics.getByRole("button", { name: "保存诊断授权" }).click();
    }
    await traceGrant("content", "grant");
    await diagnostics.getByText(/Reviewer · 业务正文节选 · 至/).waitFor();
    await traceGrant("export", "grant");
    await diagnostics
      .getByRole("button", { name: "导出脱敏结构与日志", exact: true })
      .waitFor();
    await diagnostics.getByLabel("筛选步骤").selectOption("4");
    await diagnostics
      .getByRole("button", { name: "查看此步骤的正文节选" })
      .click();
    await diagnostics.getByText("RECEIPT remote OK", { exact: true }).waitFor();
    const filePromise = p.waitForEvent("download");
    await diagnostics
      .getByRole("button", { name: "导出含此步骤正文的记录" })
      .click();
    const file = await filePromise;
    const payload = JSON.parse(await readFile(await file.path(), "utf-8"));
    assert.equal(payload.content.step, 4);
    assert.equal(payload.trace.spans.length, 4);
    await traceGrant("content", "revoke");
    await diagnostics
      .getByText(/Reviewer · 业务正文节选 · 至/)
      .waitFor({ state: "detached" });
    assert.equal(
      await diagnostics.getByText("RECEIPT remote OK", { exact: true }).count(),
      0,
    );
    console.log(
      "Browser: revision and original run traces, DAG dependencies, scoped viewing/export and removal after revocation OK",
    );
    const projectId = new URL(p.url()).hash.slice(1);
    const created = await p.request.post("/api/services", {
      data: { name: "分支复用", instructions: "核对简报", project: projectId },
    });
    assert.equal(created.status(), 201);
    const service = await created.json();
    const started = await p.request.post(
      "/api/services/" + service.id + "/chat",
      {
        data: { input: "第二批简报", version: 1, authorizeCalls: 4 },
        headers: { "Idempotency-Key": "graph-repeat" },
      },
    );
    assert.equal(started.status(), 202);
    const reused = await (
      await p.request.get("/api/projects/" + (await started.json()).project)
    ).json();
    assert.deepEqual(
      reused.plan.nodes.map((n) => n.dependsOn),
      [[], [1], [], [2, 3]],
    );
    console.log(
      "Browser: four-step branch plan, service reuse, isolated revision and final approval OK",
    );
    await p.getByRole("button", { name: "我的工作区" }).click();
    await p.getByRole("button", { name: "企业管理", exact: true }).click();
    await p.getByRole("button", { name: "用户与监控", exact: true }).click();
    await p.getByRole("heading", { name: "运行监控" }).waitFor();
    const monitor = p.getByRole("region", { name: "运行监控" });
    await monitor.getByLabel("统计周期").selectOption("7");
    const app = monitor.getByLabel("发起成员或应用");
    await app.selectOption({ label: "External desk（应用）" });
    await monitor.getByText("正式交付批次", { exact: true }).waitFor();
    const actual = await (
      await p.request.get(
        "/api/admin/usage?days=7&member=" + (await app.inputValue()),
      )
    ).json();
    for (const [label, key] of [
      ["执行完成", "completed"],
      ["成果版本审核通过", "reviewedVersions"],
      ["正式交付批次", "deliveredEditions"],
    ]) {
      const number = monitor
        .getByLabel("工作成果统计")
        .locator("dt", { hasText: label })
        .locator("..")
        .locator("dd");
      assert.equal(Number(await number.innerText()), actual[0][key]);
    }
    await monitor.getByText("统计口径").click();
    await monitor.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/management-outcomes.png",
    });
    await p.getByRole("button", { name: "高级设置", exact: true }).click();
    await checkResourceBudget(p);
    await verifyPalette();
    await p.getByRole("button", { name: "关闭", exact: true }).click();
    await p.getByRole("button", { name: "打开对话历史" }).click();
    await p.getByRole("button", { name: "新对话", exact: true }).last().click();
    await p
      .getByRole("textbox", { name: "消息", exact: true })
      .fill("DRAFT_GAP_PROBE 制作会议时程");
    await p.getByRole("button", { name: "发送消息" }).click();
    await p.getByText("根据对话推荐方案或直接回答").click();
    await p.getByLabel("规划助手模型").selectOption({ label: "Stub TLS v1" });
    await p.getByLabel(/授权一次规划调用/).check();
    await p.getByRole("button", { name: "请助手规划" }).click();
    const gap = p
      .getByRole("heading", { name: "待补能力 · 会议时程" })
      .locator("..");
    await gap.getByLabel("补齐方式").selectOption("draft");
    assert.equal(await gap.getByLabel("草稿名称").inputValue(), "会议时程助手");
    await gap.getByLabel("使用模型").selectOption({ label: "Stub TLS v1" });
    await gap.getByRole("button", { name: "保存为待验证草稿" }).click();
    await gap.getByText("验证并发布此草稿").click();
    assert.ok(
      await p.getByRole("button", { name: "采用为待确认方案" }).isDisabled(),
    );
    await gap.getByLabel(/授权一次试运行/).check();
    const trialRoute = "**/agents/*/validate";
    let firstKey, firstTrial;
    await p.route(
      trialRoute,
      async (route) => {
        firstKey = route.request().headers()["idempotency-key"];
        const committed = await route.fetch();
        assert.equal(committed.status(), 200);
        firstTrial = (await committed.json()).id;
        await route.abort("connectionreset");
      },
      { times: 1 },
    );
    const lost = p.waitForEvent("requestfailed", (r) =>
      r.url().endsWith("/validate"),
    );
    await gap.getByRole("button", { name: "执行缺口试运行" }).click();
    await lost;
    const retried = p.waitForResponse((r) => r.url().endsWith("/validate"));
    await gap.getByRole("button", { name: "执行缺口试运行" }).click();
    const replay = await retried;
    assert.ok(firstKey && firstTrial);
    assert.equal(replay.request().headers()["idempotency-key"], firstKey);
    assert.equal((await replay.json()).id, firstTrial);
    await gap.getByRole("button", { name: "发布此能力修订" }).click();
    await p.getByRole("button", { name: "采用为待确认方案" }).click();
    await p.getByRole("heading", { name: "协作方案 v1" }).waitFor();
    assert.match(
      await p
        .getByRole("heading", { name: "协作方案 v1" })
        .locator("..")
        .innerText(),
      /会议时程助手/,
    );
    await p.getByRole("button", { name: "新对话", exact: true }).click();
    console.log(
      "Browser: planner capability gap, editable draft, trial evidence, authorized library publication and plan adoption OK",
    );
    await p.getByRole("button", { name: "我的工作区" }).click();
    await p.getByRole("button", { name: "团队能力库", exact: true }).click();
    const registry = p
      .getByText("注册已有 Agent Card", { exact: true })
      .locator("..");
    await registry.locator(":scope > summary").click();
    await registry.getByText("从公开地址读取 Card").click();
    await registry
      .getByLabel("Card 地址或服务根地址")
      .fill(process.env.PUBLIC_CARD_ORIGIN);
    await registry.getByRole("button", { name: "读取公开描述" }).click();
    await registry.getByText("已读取公开描述，请核对后保存草稿。").waitFor();
    const card = JSON.parse(
      await registry.getByLabel("Agent Card JSON").inputValue(),
    );
    assert.equal(card.name, "Receipt reviewer");
    assert.ok(card.capabilities);
    const chooser = registry.getByLabel("此修订的 JSONRPC 调用地址");
    assert.equal(await chooser.inputValue(), card.url);
    const alternate = process.env.PUBLIC_CARD_ORIGIN + "/secondary";
    await registry.getByLabel("Agent Card JSON").fill(
      JSON.stringify({
        ...card,
        name: "多接口验证",
        preferredTransport: "GRPC",
        url: process.env.PUBLIC_CARD_ORIGIN + "/grpc",
        additionalInterfaces: [
          { transport: "JSONRPC", url: alternate },
          { transport: "JSONRPC", url: card.url },
        ],
      }),
    );
    await chooser.selectOption(card.url);
    await registry.getByRole("button", { name: "保存外部草稿" }).click();
    await registry.getByText("草稿已登记，发布前需验证本修订。").waitFor();
    const multiCard = p.getByText("多接口验证", { exact: true }).locator("..");
    await multiCard.getByText("修订外部 Agent").click();
    assert.equal(
      await multiCard.getByLabel("此修订的 JSONRPC 调用地址").inputValue(),
      card.url,
    );
    const checks = multiCard
      .getByText("必要检查 · v1", { exact: true })
      .locator("..");
    await checks.locator(":scope > summary").click();
    await checks.getByRole("button", { name: "编辑验收清单" }).click();
    await checks.getByLabel("验收要求").fill("回执文字");
    await checks.getByLabel("团队共享测试输入").fill("Browser QA");
    await checks
      .getByRole("textbox", { name: "预期文本", exact: true })
      .fill("RECEIPT remote OK");
    await checks.getByLabel("文本判断方式").selectOption("exact");
    await checks
      .getByLabel("业务验收人邮箱（可选）")
      .fill("review@sample.invalid");
    await checks.getByRole("button", { name: "保存验收清单" }).click();
    await checks.getByLabel(/授权以共享样例/).check();
    await checks.getByRole("button", { name: "运行此检查" }).click();
    await checks.getByLabel("业务结论").selectOption("pass");
    assert.equal(
      await multiCard.getByRole("button", { name: "发布此版本" }).count(),
      0,
    );
    await checks.getByLabel("验收依据").fill("核对实际回执");
    await checks.getByRole("button", { name: "提交业务验收" }).click();
    await multiCard.getByRole("button", { name: "发布此版本" }).click();
    await multiCard.getByText(/已发布 v1/).waitFor();
    await checks.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/required-check.png",
    });
    await multiCard.getByText("试运行记录与回执", { exact: true }).click();
    await multiCard.getByText("标记证据失效", { exact: true }).click();
    await multiCard.getByLabel("失效原因").fill("样例需要更新");
    await multiCard.getByRole("button", { name: "确认标记失效" }).click();
    await checks.getByText("回执文字 · 未通过", { exact: true }).waitFor();
    await multiCard.getByText(/已发布 v1 · 需复验或恢复资源/).waitFor();
    console.log(
      "Browser: shared business example, exact match, named review and revision publication",
    );
    await p.getByRole("button", { name: "关闭", exact: true }).click();
    await p.goto(process.env.SMOKE_ORIGIN + "/#" + process.env.REMOTE_PROJECT);
    await p.reload();
    const pending = p.getByText(/待核对调用 · 调用记录/).locator("..");
    await pending.locator("summary").click();
    await pending
      .getByText("步骤 1 · 远端待补充输入", { exact: true })
      .waitFor();
    await pending.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/remote-receipt.png",
    });
    await pending.getByLabel("继续核对原因").fill("已在原任务补充资料");
    await pending.getByRole("button", { name: "再次核对原任务" }).click();
    await p
      .locator("article.card")
      .getByText("RECEIPT asynchronous BROWSER", { exact: true })
      .waitFor({ timeout: 35000 });
    await p.getByRole("button", { name: "审核此版本" }).click();
    await p.getByRole("button", { name: "确认并固定以上版本" }).click();
    await p.getByText("下载交付包（ZIP）").waitFor();
    console.log(
      "Browser: original external task checked after human input, actual receipt reviewed and delivered",
    );
    await p.goto(process.env.SMOKE_ORIGIN + "/#" + process.env.PREVIEW_PROJECT);
    await p.reload();
    const preview = p.locator("article.card details.file-preview").first();
    await preview.locator("summary").first().click();
    await preview
      .locator('canvas[data-rendered="true"]')
      .waitFor({ timeout: 40000 });
    await preview.getByText("本页可读取文字").click();
    assert.match(await preview.innerText(), /duty manager/);
    await p.getByText(/^项目资料 ·/).click();
    const slides = p
      .getByText("预览 slides.pptx", { exact: true })
      .locator("..");
    await slides.locator("summary").first().click();
    await slides.getByText("第 1 / 2 页").waitFor();
    await slides.getByRole("button", { name: "下一页" }).click();
    await slides.getByText("本页可读取文字").click();
    await slides.getByText(/Second page/).waitFor();
    const picture = p
      .getByText("预览 sample.png", { exact: true })
      .locator("..");
    await picture.locator("summary").click();
    await picture.locator("img").evaluate((el) => el.decode());
    assert.equal(
      await picture.locator("img").evaluate((el) => el.naturalWidth),
      1,
    );
    await p.getByRole("button", { name: "新对话", exact: true }).click();
    console.log(
      "Browser: PDF.js rendered the LibreOffice-converted revised handout and exposed its page text",
    );
    await p.setViewportSize({ width: 390, height: 844 });
    await p.getByRole("heading", { name: "新对话" }).waitFor();
    await verifyPalette();
    assert.ok(
      await p.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    await p.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/mobile-chat.png",
    });
    const openHistory = p.getByRole("button", { name: "打开对话历史" });
    await openHistory.click();
    const drawer = p.getByRole("dialog", { name: "对话历史" });
    await drawer.waitFor();
    await p.keyboard.press("Escape");
    await drawer.waitFor({ state: "hidden" });
    assert.ok(await openHistory.evaluate((el) => el === document.activeElement));
    await checkRevisionDrafts(browser, workspace);
    await checkBudgetInbox(browser, workspace);
    await checkCandidates(workspace);
    await checkPreferences(browser, workspace);
    await checkImprovements(browser, workspace);
    await checkTeamRules(workspace);
    assert.deepEqual(errors, []);
    console.log(
      "browser: real login, goal submission, project detail, admin navigation, history and mobile home OK",
    );
  } finally {
    await browser.close();
  }
})().catch((e) => {
  console.error(e);
  process.exit(1);
});

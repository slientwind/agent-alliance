import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const origin = process.env.SMOKE_ORIGIN,
  dir = process.env.VISUAL_EVIDENCE;
if (!origin || !dir) throw new Error("Set SMOKE_ORIGIN and VISUAL_EVIDENCE");
await mkdir(dir, { recursive: true });
const browser = await chromium.launch();
try {
  const results = [];
  for (const [width, height] of [
    [320, 740],
    [390, 844],
    [1440, 900],
  ]) {
    const page = await browser.newPage({
      baseURL: origin,
      viewport: { width, height },
    });
    if (width === 320)
      await page.addInitScript(() => Object.defineProperty(Intl, "Segmenter", {value: undefined}));
    const north = {
      id: "north",
      name: "华北区域运营团队",
      joined: true,
      default: false,
    };
    const south = {
      id: "south",
      name: "华南客户服务团队",
      joined: true,
      default: false,
    };
    let teams = [north, south],
      submissions = [],
      models = [],
      agents = [],
      modelDrafts = [],
      templateReads = [],
      templateSaves = [],
      role = "member";
    await page.route("**/api/**", (route) => {
      const req = route.request(),
        path = new URL(req.url()).pathname;
      if (path === "/api/projects" && req.method() === "POST") {
        submissions.push(req.postDataJSON());
        return route.fulfill({ status: 409, json: { error: "样例到此为止" } });
      }
      if (path === "/api/agents" && req.method() === "POST") {
        modelDrafts.push(req.postDataJSON());
        return route.fulfill({
          status: 201,
          json: { id: "cover-agent", revision: 1 },
        });
      }
      if (path === "/api/teams/north/setup") {
        templateReads.push(path);
        return route.fulfill({ json: {
          templates: [
            { slot: "brief", name: "简报助手", instructions: "汇总业务资料" },
            { slot: "cover", name: "封面模板", instructions: "生成活动封面" },
          ],
          state: {
            slots: { cover: {
              agent: "illustrator", revision: 3, published: 2, ready: true,
              verified: true, connected: true, name: "采购培训封面",
            } },
            projects: [], connections: [],
          },
        } });
      }
      if (path === "/api/teams/north/setup/brief" && req.method() === "PUT") {
        templateSaves.push(req.postDataJSON());
        return route.fulfill({ json: {} });
      }
      return route.fulfill({
        json:
          {
            "/api/instance": { initialized: true, name: "样例企业" },
            "/api/me": { id: "mock-member", name: "业务成员", role },
            "/api/teams": teams,
            "/api/resources": models,
            "/api/agents": agents,
            "/api/agents/cover-agent/checks/1": {
              version: 0,
              editable: true,
              items: [],
            },
          }[path] ?? [],
      });
    });
    async function focusRefresh() {
      await Promise.all([
        page.waitForResponse((r) => r.url().endsWith("/api/teams")),
        page.evaluate(() => window.dispatchEvent(new Event("focus"))),
      ]);
    }
    await page.clock.install();
    await page.goto("/");
    const select = page.getByLabel("项目所属团队"),
      text = page.getByRole("textbox", { name: "消息", exact: true }),
      send = page.getByRole("button", { name: "发送消息" });
    const projectOptions = page.getByRole("button", { name: /^项目选项/ });
    await text.waitFor();
    assert.equal(await select.count(), 0);
    assert.equal(await projectOptions.count(), 0);
    await text.fill("按地区核对销售额，审核后交付工作簿。");
    await page.locator('.composer input[type="file"]').setInputFiles({
      name: "地区销售额.csv",
      mimeType: "text/csv",
      buffer: Buffer.from("地区,销售额\n华南,20\n"),
    });
    const attachedSheet = page.getByRole("button", {name: "移除附件 地区销售额.csv"});
    // The first send asks for a destination without starting or billing a run.
    await send.click();
    await select.waitFor();
    assert.equal(await projectOptions.getAttribute("aria-expanded"), "true");
    assert.ok(await select.evaluate(el => el === document.activeElement));
    assert.equal(submissions.length, 0);
    assert.equal(await select.inputValue(), "");
    assert.ok(await send.isDisabled());
    await select.selectOption(south.id);
    assert.ok(await send.isEnabled());
    await projectOptions.click();
    assert.equal(await select.count(), 0);
    assert.ok(await send.isEnabled());
    assert.match(await text.inputValue(), /工作簿/);
    await projectOptions.click();
    assert.equal(await select.inputValue(), south.id);
    const metrics = await page.evaluate(() => {
      const el = document.querySelector(".composer"),
        r = el.getBoundingClientRect();
      return {
        area: (r.width * r.height) / (innerHeight * innerWidth),
        overflow:
          document.documentElement.scrollWidth > innerWidth ||
          el.scrollWidth > el.clientWidth,
      };
    });
    assert.ok(metrics.area > 0.65 && !metrics.overflow);
    await select.blur();
    if (width === 390)
      await page.screenshot({ path: dir + "/mobile-team.png" });
    teams = [north];
    if (width === 390) {
      await Promise.all([
        page.waitForResponse((r) => r.url().endsWith("/api/teams")),
        page.clock.fastForward(10500),
      ]);
    } else await focusRefresh();
    await select
      .locator("option", { hasText: "原团队已不可用" })
      .waitFor({ state: "attached" });
    assert.ok(await send.isDisabled());
    assert.match(await text.inputValue(), /工作簿/);
    assert.ok(await attachedSheet.isVisible());
    await select.selectOption(north.id);
    await Promise.all([
      page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/projects") && r.request().method() === "POST",
      ),
      send.click(),
    ]);
    assert.equal(submissions[0].team, "north");
    // A fresh session with one non-default team may create directly. With no
    // membership it may still read permitted projects, but cannot create one.
    await page.reload();
    await text.fill("只有一个营业团队");
    assert.equal(await projectOptions.count(), 0);
    await Promise.all([
      page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/projects") && r.request().method() === "POST",
      ),
      send.click(),
    ]);
    assert.equal(submissions[1].team, "north");
    teams = [];
    await page.reload();
    await page.getByText("请联系管理员开通可用的工作空间。").waitFor();
    await text.fill("等待加入团队");
    assert.ok(await send.isDisabled());
    await text.press("Enter");
    assert.equal(submissions.length, 2);
    const launch = page.getByRole("button", { name: "选择或创建 Agent" });
    const picker = page.getByRole("dialog", { name: "Agent", exact: true });
    async function startImageDraft() {
      await launch.click();
      await picker.getByRole("button", { name: "创建 Agent" }).click();
      await picker.getByLabel("成果类型").selectOption("png");
    }
    await startImageDraft();
    await picker.getByText("请联系管理员接入可使用的生图模型。").waitFor();
    assert.ok(
      await picker
        .getByRole("button", { name: "创建并准备试运行" })
        .isDisabled(),
    );
    assert.equal(
      await picker.getByRole("button", { name: "配置生图模型" }).count(),
      0,
    );
    assert.equal(modelDrafts.length, 0);
    teams = [north];
    models = [
      {
        id: "writing",
        revision: 2,
        operation: "text",
        name: "内部模型",
        team: north.id,
      },
      {
        id: "cover",
        revision: 3,
        operation: "png",
        name: "内部模型",
        team: north.id,
      },
      {
        id: "blocked",
        revision: 2,
        operation: "png",
        name: "已停凭据",
        team: north.id,
        credentialBlocked: true,
      },
    ];
    await page.reload();
    const goal = "为采购培训准备一张封面";
    await text.fill(goal);
    await startImageDraft();
    assert.equal(
      await picker.getByLabel("希望 Agent 完成什么任务？").inputValue(),
      goal,
    );
    await picker.getByRole("button", { name: "创建并准备试运行" }).click();
    await picker.getByLabel("样图要求").waitFor();
    assert.deepEqual(modelDrafts, [
      {
        name: goal,
        instructions: goal,
        resource: "cover",
        resourceRevision: 3,
      },
    ]);
    const pickerOverflow = await picker.evaluate(
      (el) => el.scrollWidth > el.clientWidth,
    );
    assert.equal(pickerOverflow, false);
    const cover = {
      id: "illustrator",
      name: "待审核的新名字",
      instructions: "草稿私有匹配词",
      revision: 3,
      published: 2,
      publishedReady: true,
      publishedOperation: "png",
      publishedName: "采购培训封面",
      publishedInstructions: goal,
      team: north.id,
      teamName: north.name,
    };
    const writer = {
      ...cover,
      id: "summarizer",
      publishedName: "文字助手",
      publishedInstructions: goal + "，为 PNG 图片撰写配文。",
      publishedOperation: "text",
    };
    agents = [
      cover,
      writer,
      { ...cover, id: "offline", publishedReady: false },
    ];
    await page.reload();
    await launch.click();
    const search = picker.getByLabel("搜索 Agent");
    await search.fill("ＰＮＧ");
    assert.deepEqual(await picker.locator(".agent-choice strong").allTextContents(), [cover.publishedName]);
    await search.fill("草稿私有匹配词");
    await picker.getByText("没有匹配的 Agent").waitFor();
    const query = "培训活动用的采购封面";
    await search.fill(query);
    const choices = picker.locator("button.agent-choice");
    assert.equal(await choices.count(), 2);
    assert.match(await choices.first().innerText(), /采购培训封面/);
    await choices.first().click();
    assert.equal(await text.inputValue(),query);
    assert.equal(modelDrafts.length,1);
    await page.reload();
    await launch.click();
    await picker.getByRole("button", {name:"组合 Agent"}).click();
    await search.fill(query);
    await choices.first().click();
    await search.fill(writer.publishedName);
    await choices.first().click();
    await picker.getByRole("button",{name:"使用此组合"}).click();
    assert.equal(await text.inputValue(),query);
    await page.locator(".picked-agent").getByText("2 个 Agent 依次协作").waitFor();
    await page.reload();
    const originalTask = "完成培训材料和会议议程";
    const requirement = "将培训流水按地区分组汇总成 Excel";
    await text.fill(originalTask);
    await launch.click();
    await search.fill(requirement);
    await picker.getByRole("button",{name:"创建 Agent"}).click();
    assert.equal(await picker.getByLabel("希望 Agent 完成什么任务？").inputValue(), requirement);
    await picker.getByLabel("成果类型").selectOption("aggregate");
    await picker.getByLabel("分组列", {exact:true}).fill("地区");
    await picker.getByLabel("金额列", {exact:true}).fill("实收");
    await picker.getByRole("button",{name:"返回 Agent 列表"}).click();
    await search.fill("Word");
    await picker.getByRole("button",{name:"继续未提交的配置"}).click();
    assert.equal(await picker.getByLabel("成果类型").inputValue(), "aggregate");
    assert.equal(await picker.getByLabel("分组列", {exact:true}).inputValue(), "地区");
    assert.equal(await picker.getByLabel("金额列", {exact:true}).inputValue(), "实收");
    assert.equal(await picker.getByLabel("希望 Agent 完成什么任务？").inputValue(), requirement);
    await picker.getByRole("button",{name:"返回 Agent 列表"}).click();
    await search.fill("ＰＮＧ");
    await picker.getByRole("button",{name:"创建 Agent",exact:true}).click();
    assert.equal(await picker.getByLabel("成果类型").inputValue(),"png");
    assert.equal(await picker.getByLabel("希望 Agent 完成什么任务？").inputValue(),originalTask);
    await picker.getByRole("button",{name:"关闭",exact:true}).click();
    assert.equal(await text.inputValue(),originalTask);
    await page.reload();
    await startImageDraft();
    const description = picker.getByLabel("希望 Agent 完成什么任务？");
    await description.fill("草稿私有匹配词");
    assert.equal(await picker.getByText(/可直接使用的相似 Agent/).count(), 0);
    await description.fill(query);
    await picker.getByText("可直接使用的相似 Agent（1）").click();
    await picker.getByText(/任务词重合/).waitFor();
    await picker.getByRole("button", { name: "使用此 Agent" }).click();
    assert.equal(await text.inputValue(), query);
    assert.equal(modelDrafts.length, 1);
    await page.reload();
    await launch.click();
    await picker.getByRole("button", { name: "组合 Agent" }).click();
    await picker.getByRole("button", { name: /文字助手/ }).click();
    await picker.getByRole("button", { name: "创建 Agent" }).click();
    await picker.getByLabel("成果类型").selectOption("png");
    await picker.getByLabel("希望 Agent 完成什么任务？").fill(goal);
    await picker.getByText("可直接使用的相似 Agent（1）").click();
    await picker.getByRole("button", { name: "加入当前组合" }).click();
    const sequence = picker.getByLabel("协作顺序");
    assert.deepEqual(await sequence.locator("li strong").allTextContents(), [
      "文字助手",
      "采购培训封面",
    ]);
    await sequence.getByRole("button", { name: "使用此组合" }).click();
    await text.fill("生成培训简报和封面");
    await Promise.all([
      page.waitForResponse(
        (r) =>
          r.url().endsWith("/api/projects") && r.request().method() === "POST",
      ),
      send.click(),
    ]);
    assert.deepEqual(submissions.at(-1).agents, [writer.id, cover.id]);
    assert.deepEqual(submissions.at(-1).agentRevisions, [2, 2]);
    teams = [north, south];
    agents = [writer, {...cover,publishedReady:false,id:"offline"}, cover];
    await page.reload();
    await text.fill(goal);
    assert.equal(await projectOptions.count(), 0);
    await page.getByRole("button", {name: "我的工作区"}).click();
    const workspace = page.getByRole("dialog", {name: "我的工作区"});
    assert.equal(await workspace.getByRole("button", {name: "企业管理"}).count(), 0);
    await workspace.getByText("协作与授权", {exact: true}).click();
    await workspace.getByRole("button", {name: "更改项目归属"}).click();
    assert.ok(await select.evaluate(el => el === document.activeElement));
    await select.selectOption(south.id);
    await launch.click();
    assert.equal(await picker.locator(".agent-choice strong").first().textContent(), "采购培训封面");
    await picker.locator(".agent-choice").first().getByText(/匹配需求中的/).waitFor();
    await picker.getByRole("button", { name: /采购培训封面/ }).click();
    assert.equal(await select.count(), 0);
    await page.locator(".picked-agent").getByText(north.name + "承担费用").waitFor();
    const writes = submissions.length;
    await page.locator(".picked-agent").getByRole("button", { name: "移除", exact: true }).click();
    await page.getByRole("button", { name: /项目选项.*华南客户服务团队/ }).waitFor();
    assert.equal(await text.inputValue(), goal);
    assert.equal(submissions.length, writes);
    await launch.click();
    await picker.getByRole("button", { name: /采购培训封面/ }).click();
    if (width === 390) {
      await text.blur();
      await page.screenshot({path:dir+"/mobile-agent-conversation.png"});
    }
    await Promise.all([
      page.waitForResponse(r => r.url().endsWith("/api/projects") && r.request().method() === "POST"),
      send.click(),
    ]);
    assert.equal(submissions.at(-1).team, north.id);
    assert.deepEqual(submissions.at(-1).agents, [cover.id]);
    teams = [north];
    await focusRefresh();
    await page.locator(".picked-agent").getByRole("button", { name: "移除", exact: true }).click();
    await select.locator("option", {hasText: "原团队已不可用"}).waitFor({state: "attached"});
    assert.ok(await send.isDisabled());
    assert.equal(await text.inputValue(), goal);
    await page.reload();
    await text.fill("保留本次简报需求");
    await page.getByRole("button", {name:"我的工作区"}).click();
    await page.getByRole("button", {name:"团队能力库", exact:true}).click();
    const library = page.getByRole("dialog");
    await library.getByText("归属团队 · "+north.name, {exact:true}).waitFor();
    assert.equal(await library.locator("#library-agent-offline").getByRole("button",{name:/开始对话/}).count(),0);
    await library.getByRole("button",{name:"使用文字助手 v2 开始对话"}).click();
    assert.equal(await text.inputValue(),"保留本次简报需求");
    await page.locator(".picked-agent").getByText("文字助手 · v2").waitFor();
    assert.equal(await select.count(),0);
    agents = [
      {...writer, id:"ledger", publishedOperation:"aggregate", publishedName:"流水汇总",
        publishedInstructions:"按网点和地区分组汇总流水金额，交付 Excel 和小计。"},
      {...writer, id:"rows", publishedOperation:"xlsx", publishedName:"设备逐行清单",
        publishedInstructions:"把设备编号逐行写入 Excel 清单，保留前导零。"},
      {...writer, id:"manual", publishedOperation:"docx", publishedName:"会议议程",
        publishedInstructions:"把培训议程写成 Word 文档。"},
      {...writer, id:"reader", publishedName:"文档提要",
        publishedInstructions:"阅读 Word 和 Excel 文件，返回文本摘要。"},
      {...writer, id:"retired", publishedReady:false, publishedOperation:"xlsx", publishedName:"旧版清单"},
    ];
    await page.reload();
    await launch.click();
    const queries = [];
    for (const [query, expected, exactSet] of [
      ["对各地区网点的流水做金额汇总", "流水汇总"],
      ["请把有前导零的设备编号逐行整理成清单", "设备逐行清单"],
      ["Ｗｏｒｄ", "会议议程", ["会议议程"]],
      ["Excel", null, ["流水汇总","设备逐行清单"]],
      ["火星地层年代", null, []],
    ]) {
      await search.fill(query);
      const names = await picker.locator(".agent-choice strong").allTextContents();
      if (expected) assert.equal(names[0],expected,query);
      if (exactSet) assert.deepEqual([...names].sort(), [...exactSet].sort(),query);
      queries.push({query,results:names});
    }
    agents = [
      cover,
      { ...cover, id: "offline", publishedName: "不可用的旧助手", publishedReady: false },
      { ...cover, id: "outsider", team: "other", publishedName: "未授权的助手" },
    ];
    teams = [north, south];
    await page.reload();
    const recommended = page.getByRole("group", { name: "适合这项任务的 Agent" });
    await text.fill("火星地层年代");
    assert.equal(await recommended.count(), 0);
    const coverBrief = "请用采购培训封面制作活动封面";
    await text.fill(coverBrief);
    await page.locator('.composer input[type="file"]').setInputFiles({
      name: "采购参考.md",
      mimeType: "text/markdown",
      buffer: Buffer.from("活动主题：采购培训"),
    });
    const useCover = recommended.getByRole("button", { name: /采购培训封面/ });
    await useCover.waitFor();
    assert.equal(await recommended.getByRole("button").count(), 1);
    assert.match(await useCover.innerText(), new RegExp(north.name));
    const beforeSuggested = submissions.length;
    if (width === 390) {
      await text.blur();
      await page.screenshot({ path: dir + "/mobile-agent-recommendation.png" });
    }
    await useCover.click();
    await page.locator(".picked-agent").getByText("采购培训封面 · v2").waitFor();
    assert.equal(submissions.length, beforeSuggested);
    assert.equal(await select.count(), 0);
    assert.equal(await text.inputValue(), coverBrief);
    await page.getByText("采购参考.md", { exact: true }).waitFor();
    await page.getByRole("button", { name: "移除附件 采购参考.md" }).click();
    await Promise.all([
      page.waitForResponse(r => r.url().endsWith("/api/projects") && r.request().method() === "POST"),
      send.click(),
    ]);
    assert.equal(submissions.at(-1).team, north.id);
    assert.deepEqual(submissions.at(-1).agentRevisions, [2]);
    await page.reload();
    await text.fill(coverBrief);
    await launch.click();
    await picker.getByRole("searchbox", { name: "搜索 Agent" }).waitFor();
    if (width === 390) {
      await picker.screenshot({ path: dir + "/mobile-agent-picker.png" });
    }
    await picker.getByRole("button", { name: "内置 Agent 模板" }).click();
    const templates = page.getByRole("dialog", { name: "内置 Agent 模板" });
    const scope = templates.getByLabel("模板归属及费用承担方");
    await scope.waitFor();
    assert.equal(await scope.inputValue(), "");
    assert.equal(templateReads.length, 0);
    await scope.selectOption(north.id);
    const brief = templates.getByRole("article").filter({ hasText: "简报助手" });
    await brief.getByText("使用 内部模型 v2").waitFor();
    assert.equal(await templates.getByLabel("绑定文本模型").count(), 0);
    assert.equal(await templates.getByText(/规划助手默认模型|接入团队模型/).count(), 0);
    await brief.getByRole("button", { name: "按模板创建草稿" }).click();
    await templates.getByRole("button", { name: "用于当前对话" }).click();
    assert.deepEqual(templateSaves, [{ resource: "writing", revision: 2 }]);
    assert.equal(await text.inputValue(), coverBrief);
    await page.locator(".picked-agent").getByText("采购培训封面 · v2").waitFor();
    assert.equal(submissions.length, beforeSuggested + 1);
    role = "admin";
    teams = [{ ...north, default: true }, south];
    agents = [{ ...cover, team: south.id, teamName: south.name }];
    await page.reload();
    await text.fill(coverBrief);
    await useCover.click();
    await page.locator(".picked-agent").getByText(south.name + "承担费用").waitFor();
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "企业管理", exact: true }).click();
    const enterprise = page.getByRole("dialog", { name: "企业管理" });
    const advanced = enterprise.getByRole("button", { name: "高级设置", exact: true });
    const resourceSection = enterprise.getByRole("button", { name: "模型与 Agent", exact: true });
    assert.equal(await enterprise.getByText("组织与团队成员").count(), 0);
    assert.equal(await enterprise.getByRole("region", { name: "运行监控" }).count(), 0);
    await enterprise.getByLabel("资源名称", { exact: true }).fill("待补的模型");
    await advanced.click();
    await enterprise.getByText("组织与团队成员", { exact: true }).waitFor();
    assert.equal(await advanced.getAttribute("aria-pressed"), "true");
    if (width === 390)
      await enterprise.screenshot({ path: dir + "/mobile-enterprise-advanced.png" });
    await resourceSection.click();
    await enterprise.getByText("组织与团队成员", { exact: true }).waitFor({ state: "hidden" });
    assert.equal(await enterprise.getByLabel("资源名称", { exact: true }).inputValue(), "待补的模型");
    assert.ok(await enterprise.evaluate(el => el.scrollWidth <= el.clientWidth));
    if (width === 390)
      await enterprise.screenshot({ path: dir + "/mobile-enterprise-resources.png" });
    const usagePage = enterprise.getByRole("button", {name: "用户与监控", exact: true});
    await usagePage.click();
    await enterprise.getByLabel("用量统计团队").waitFor();
    await enterprise.getByLabel("成员所属团队").waitFor();
    await resourceSection.click();
    await page.getByRole("button", { name: "团队首次配置", exact: true }).click();
    const setup = page.getByRole("dialog", { name: "团队首次配置" });
    await setup.getByText(/接入团队模型/).waitFor();
    assert.equal(await setup.getByLabel("配置团队").inputValue(), north.id);
    const primary = { ...north, default: true };
    teams = [primary];
    await page.reload();
    await text.fill("本周工作摘要");
    assert.equal(await projectOptions.count(), 0);
    const directSend = page.waitForResponse(r => r.url().endsWith("/api/projects") && r.request().method() === "POST");
    await send.click();
    await directSend;
    assert.equal(submissions.at(-1).team, primary.id);
    assert.equal(await select.count(), 0);
    await page.getByRole("button", {name: "我的工作区"}).click();
    await workspace.getByText("协作与授权", {exact: true}).click();
    assert.equal(await workspace.getByRole("button", {name: "更改项目归属"}).count(), 0);
    await workspace.getByRole("button", {name: "企业管理", exact: true}).click();
    await Promise.all([
      page.waitForResponse(r => r.url().endsWith("/api/teams")),
      usagePage.click(),
    ]);
    await enterprise.locator('[aria-label="工作成果统计"]').waitFor();
    assert.equal(await enterprise.getByLabel("用量统计团队").count(), 0);
    assert.equal(await enterprise.getByLabel("成员所属团队").count(), 0);
    results.push({
      width,
      height,
      nativeWordSegmentation: width !== 320,
      queries,
      composerArea: metrics.area,
      horizontalOverflow: metrics.overflow,
      checks: [
        "choose a destination only at send when no default exists; no request before choice",
        "withdrawn choice blocks send and preserves the message and attachment",
        "only non-default team automatically selected",
        "project scope is initially out of the composer toolbar and opens on demand from the workspace or sending",
        "zero memberships cannot submit",
        "published Agent relevant to the brief ranks first with matching words; interaction requires explicit choice",
        "library shows a shared team once; choosing the published revision preserves the conversation draft",
        "choosing a published Agent resolves its team and shows its payer before submitting",
        "removing a temporary Agent restores the ordinary destination and the draft; revoked destination requires a new choice",
        "missing image access disables creation and hides administrator action",
        "same-named resources use the correct PNG revision; blocked credentials excluded",
        "goal carried into draft; image wizard fits viewport",
        "only matching ready published output offered; direct reuse keeps new description",
        "paraphrased search ranks published title matches, ignores draft-only words and carries the task into an empty composer",
        "combining Agents keeps the initial task wording while seeking later steps by name",
        "creating a specific capability preserves the wider task; returning from search retains the unfinished description and output choice",
        "adding a similar Agent preserves previous steps and pins published revisions",
        "inline suggestions show only matching, ready Agents in joined teams and never launch until explicit send",
        "templates live in the Agent picker; ambiguous scope is explicit and the sole usable text model is pinned",
        "a ready template returns to the original draft without opening enterprise configuration or launching a task",
        "administrator setup uses its default team independently of the Agent selected for the current conversation",
        "enterprise management opens on models and agents; organization requires the advanced view and switching preserves model drafts",
        "one default team sends directly; only multi-team administration shows team filters; ordinary members cannot open enterprise management",
      ],
    });
    await page.close();
  }
  await writeFile(
    dir + "/conversation-team-mock.json",
    JSON.stringify(
      { evidence: "Chromium / mock API / no live execution", results },
      null,
      2,
    ) + "\n",
  );
} finally {
  await browser.close();
}

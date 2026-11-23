import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: origin,
  BROWSER_PASSWORD: password,
  GRANT_PROJECT: project,
  GRANT_AGENT: agent,
  VISUAL_EVIDENCE: evidence,
} = process.env;
const browser = await chromium.launch();
try {
  async function open(email, hash = "") {
    const page = await browser.newPage({
      baseURL: origin,
      viewport: { width: 1280, height: 850 },
    });
    page.setDefaultTimeout(12000);
    assert.ok(
      (
        await page.request.post("/api/login", { data: { email, password } })
      ).ok(),
    );
    await page.goto("/" + hash);
    return page;
  }
  const provider = await open("review@sample.invalid", "#" + project);
  await provider.getByText(/^项目协作 ·/).click();
  await provider.getByText("跨团队能力授权", { exact: true }).click();
  const form = provider.getByRole("form", { name: "提议跨团队能力" });
  await form.getByLabel("提供给本项目的能力").selectOption(agent + "/1");
  await form
    .getByLabel("可向项目成员展示的用途")
    .fill("将团队培训讲义整理为可审核的摘要");
  await form.getByRole("button", { name: "提交授权申请" }).click();
  await provider.getByLabel("处理依据").waitFor();
  const payer = await open("grant-payer@sample.invalid");
  const borrower = await open("grant-requester@sample.invalid", "#" + project);
  const queuePath = "/api/project-agent-grants/inbox";
  const grantedRows = async (page) => {
    const response = await page.request.get(queuePath);
    assert.equal(response.status(), 200);
    return (await response.json()).filter((g) => g.agent === agent);
  };
  assert.equal((await grantedRows(borrower)).length, 0);
  const assigned = await grantedRows(payer);
  assert.deepEqual(
    assigned.map((g) => g.project),
    [process.env.GRANT_AUXILIARY, project],
  );
  assert.ok(
    assigned.every(
      (g) => g.projectName === null && g.canBudget && !g.canResource,
    ),
  );
  const grant = assigned[1];
  const member = await (await payer.request.get("/api/me")).json();
  const admin = await (await provider.request.get("/api/me")).json();
  const delegate = async (from, to) => {
    const result = await provider.request.put(
      "/api/teams/" + process.env.GRANT_TEAM + "/budget-owner",
      { data: { previous: from, member: to, reason: "轮换费用确认人" } },
    );
    assert.equal(result.status(), 204);
  };
  async function drawer(page) {
    await page.getByRole("button", { name: "打开对话历史" }).click();
    const panel = page.getByRole("dialog");
    await panel.getByRole("button", { name: "我的待办" }).click();
    await panel.getByLabel("待办类型").selectOption("grant");
    return panel;
  }
  const funds = await drawer(payer);
  await funds.getByRole("searchbox").fill(project);
  const item = funds
    .locator("details.grant-detail")
    .filter({ hasText: project });
  await item.locator("summary").click();
  assert.equal(
    await funds.getByRole("button", { name: "打开所属项目" }).count(),
    0,
  );
  await delegate(member.id, admin.id);
  const obsolete = await payer.request.post(
    "/api/project-agent-grants/" + grant.id,
    { data: { action: "budget", expected: 1, reason: "原预算负责人处理" } },
  );
  assert.equal(obsolete.status(), 403);
  await funds.getByRole("button", { name: "刷新授权待办" }).click();
  await funds.getByText("没有匹配的授权待办").waitFor();
  const resources = await drawer(provider);
  const resourceItem = resources
    .locator("details.grant-detail")
    .filter({ hasText: "将团队培训讲义整理为可审核的摘要" });
  await resourceItem.getByText("资源授权、费用确认", { exact: true }).waitFor();
  await delegate(admin.id, member.id);
  await resources.getByRole("button", { name: "刷新授权待办" }).click();
  await resourceItem.getByText("资源授权", { exact: true }).waitFor();
  await resourceItem.locator("summary").click();
  assert.equal(
    await resourceItem.getByRole("button", { name: "打开所属项目" }).count(),
    1,
  );
  assert.equal(
    await resourceItem
      .getByRole("button", { name: "确认承担上述调用费用" })
      .count(),
    0,
  );
  await resourceItem
    .getByLabel("处理依据")
    .fill("限此项目使用已验证的模型修订");
  await resourceItem
    .getByRole("button", { name: "授权此修订使用绑定资源" })
    .click();
  await resourceItem.waitFor({ state: "detached" });
  const stale = await payer.request.post(
    "/api/project-agent-grants/" + grant.id,
    { data: { action: "budget", expected: 1, reason: "旧版本处理" } },
  );
  assert.equal(stale.status(), 409);
  await funds.getByRole("button", { name: "刷新授权待办" }).click();
  await item.locator("summary").click();
  for (const width of [320, 390, 1440]) {
    await payer.setViewportSize({ width, height: 844 });
    assert.ok(await funds.evaluate((el) => el.scrollWidth <= el.clientWidth));
    if (width === 390)
      await funds.screenshot({ path: evidence + "/grant-inbox-mobile.png" });
  }
  await item.getByLabel("处理依据").fill("由提供方团队承担一次调用");
  await item.getByRole("button", { name: "确认承担上述调用费用" }).click();
  await funds.getByText("已保存授权决定。").waitFor();
  await item.waitFor({ state: "detached" });
  assert.deepEqual(
    (await grantedRows(payer)).map((g) => g.project),
    [process.env.GRANT_AUXILIARY],
  );
  assert.equal(
    (await grantedRows(provider)).some((g) => g.project === project),
    false,
  );
  await writeFile(
    evidence + "/grant-inbox.json",
    JSON.stringify(
      {
        input: "synthetic",
        projects: 2,
        roles: ["project-only", "resource admin", "budget owner", "dual role"],
        projectTitleRedacted: true,
        revokedDecisionStatus: obsolete.status(),
        staleVersionStatus: stale.status(),
        viewports: [320, 390, 1440],
        resourceAndBudgetDecidedSeparately: true,
      },
      null,
      2,
    ),
  );
  await borrower.reload();
  await borrower.getByText(/^项目协作 ·/).click();
  await borrower.getByText("跨团队能力授权", { exact: true }).click();
  const card = borrower
    .locator("details")
    .filter({
      has: borrower.locator("summary", { hasText: /^跨团队能力授权$/ }),
    })
    .last();
  await card.getByText("已授权本项目调用", { exact: true }).waitFor();
  await card.screenshot({ path: evidence + "/project-agent-grant.png" });
  await borrower.setViewportSize({ width: 390, height: 844 });
  assert.ok(
    await card.evaluate(
      (el) =>
        el.getBoundingClientRect().width <= innerWidth &&
        el.scrollWidth <= el.clientWidth,
    ),
  );
  await card.screenshot({ path: evidence + "/project-agent-grant-mobile.png" });
  await borrower.setViewportSize({ width: 1280, height: 850 });
  await borrower.getByLabel("添加执行步骤").selectOption(agent);
  await borrower
    .getByLabel("交付目标与确认要求")
    .fill("PROJECT_GRANT_HELD_867 生成一份培训摘要，审核后交付");
  await borrower.getByRole("button", { name: "保存待确认方案" }).click();
  await borrower.getByRole("heading", { name: "协作方案 v1" }).waitFor();
} finally {
  await browser.close();
}

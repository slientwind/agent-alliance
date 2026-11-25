import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: origin,
  BROWSER_PASSWORD: password,
  PEER_TEAM: team,
  PEER_ENDPOINT: endpoint,
  PEER_PROJECT: project,
  VISUAL_EVIDENCE: dir,
} = process.env;
const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: origin,
    viewport: { width: 1280, height: 850 },
  });
  page.setDefaultTimeout(18000);
  const budget = !!process.env.PEER_BUDGET;
  const email = budget
    ? "grant-payer@sample.invalid"
    : project
      ? "grant-requester@sample.invalid"
      : "review@sample.invalid";
  assert.ok(
    (await page.request.post("/api/login", { data: { email, password } })).ok(),
  );
  await page.goto(project ? "/#" + project : "/");
  if (budget) {
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByText("协作与授权", { exact: true }).click();
    await page.getByText("我可处理的跨团队能力授权", { exact: true }).click();
    const grant = page.locator(".grant-detail").filter({
      has: page.getByRole("heading", {
        name: "异步核对助手 · v1",
        exact: true,
      }),
    });
    await grant.getByText(/每轮核对窗口 900 秒，查询间隔 1 秒/).waitFor();
    await grant.getByText("费用未知，实际金额无法封顶").waitFor();
    await grant
      .getByLabel("处理依据")
      .fill("已核对原任务的查询、取消和未知费用范围");
    await grant.screenshot({ path: dir + "/borrowed-finance.png" });
    await grant.getByRole("button", { name: "确认承担上述调用费用" }).click();
    await grant.getByText("已授权本项目调用", { exact: true }).waitFor();
  } else if (project) {
    const cards = page.locator(".card").filter({
      has: page.getByRole("button", { name: "审核此版本", exact: true }),
    });
    for (let i = 0; i < 2; i++) {
      const btn = cards
        .first()
        .getByRole("button", { name: "审核此版本", exact: true });
      await Promise.all([
        page.waitForResponse(
          (r) =>
            r.url().endsWith("/review") &&
            r.request().method() === "POST" &&
            r.ok(),
        ),
        btn.click(),
      ]);
    }
    const final = page.getByRole("form", { name: "最终交付清单" });
    await final.getByRole("button", { name: "确认并固定以上版本" }).waitFor();
    assert.equal(await final.locator("li").count(), 2);
    await final.screenshot({ path: dir + "/borrowed-review.png" });
    await final.getByRole("button", { name: "确认并固定以上版本" }).click();
    await page.getByText("下载交付包（ZIP）").waitFor();
  } else {
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "团队能力库", exact: true }).click();
    async function draft(title) {
      const summary = page.getByText(title, { exact: true });
      await summary.click();
      return summary.locator("..").locator("form").first();
    }
    async function save(form, button, route) {
      const [response] = await Promise.all([
        page.waitForResponse(
          (r) =>
            r.url().endsWith("/api/" + route) &&
            r.request().method() === "POST",
        ),
        form.getByRole("button", { name: button }).click(),
      ]);
      assert.ok(response.ok(), await response.text());
      return (await response.json()).id;
    }
    const tool = await draft("启用内置工具助手");
    const temporary = process.env.PEER_TEMPORARY;
    async function removeAccess(memberPage) {
      const { id } = await (await memberPage.request.get("/api/me")).json();
      assert.ok(
        (
          await page.request.put(
            "/api/admin/teams/" + temporary + "/members/" + id,
            { data: { joined: false, reason: "已退出临时准备组" } },
          )
        ).ok(),
      );
      await Promise.all([
        memberPage.waitForResponse(
          async (r) =>
            r.url().endsWith("/api/teams") &&
            r.ok() &&
            !(await r.json()).some((t) => t.id === temporary && t.joined),
        ),
        memberPage.evaluate(() => window.dispatchEvent(new Event("focus"))),
      ]);
    }
    const toolTeam = tool.getByLabel("所属团队");
    await toolTeam.selectOption(temporary);
    await tool.getByLabel("名称", { exact: true }).fill("借用核算助手");
    await tool.getByLabel("能力描述").fill("核算已通过审核的地区成交额");
    assert.equal(await toolTeam.inputValue(), temporary);
    await removeAccess(page);
    await toolTeam.locator('option[value=""]').waitFor({ state: "attached" });
    assert.equal(await toolTeam.inputValue(), "");
    assert.equal(
      await toolTeam.evaluate((el) => el.validity.valueMissing),
      true,
    );
    assert.equal(
      await tool.getByLabel("名称", { exact: true }).inputValue(),
      "借用核算助手",
    );
    await toolTeam.selectOption(team);
    await tool.locator('select[name="op"]').selectOption("aggregate");
    await tool.getByText("分组汇总字段").click();
    for (const [field, val] of [
      ["分组列", "region"],
      ["金额列", "amount"],
      ["筛选列", "status"],
      ["保留值", "ok"],
    ])
      await tool.getByLabel(field).fill(val);
    const toolID = await save(tool, "保存工具草稿", "agents/tools");
    const card = await draft("注册已有 Agent Card");
    await card.getByLabel("所属团队").selectOption(team);
    await card.getByLabel("Agent Card JSON").fill(
      JSON.stringify({
        protocolVersion: "0.3.0",
        preferredTransport: "JSONRPC",
        url: endpoint,
        version: "1",
        name: "异步核对助手",
        description: "核对已汇总的地区成交额",
        skills: [{ id: "check", name: "核对" }],
      }),
    );
    await card
      .getByLabel("独立访问凭据（可选）")
      .fill("synthetic-validation-key");
    await card.getByText("执行与回执核对参数").click();
    await card.getByLabel("查询间隔（秒）").fill("1");
    const cardID = await save(card, "保存外部草稿", "agents/cards");
    const owner = await browser.newPage({ baseURL: origin });
    assert.ok(
      (
        await owner.request.post("/api/login", {
          data: { email: "grant-requester@sample.invalid", password },
        })
      ).ok(),
    );
    await owner.goto("/");
    const teamSelect = owner.getByLabel("项目所属团队");
    await owner.getByRole("button", { name: "我的工作区" }).click();
    await owner.getByText("协作与授权", { exact: true }).click();
    await owner.getByRole("button", { name: "更改项目归属", exact: true }).click();
    await teamSelect.waitFor();
    assert.equal(await teamSelect.inputValue(), "");
    await teamSelect.selectOption(temporary);
    const message = owner.getByRole("textbox", { name: "消息", exact: true });
    await message.fill("跨团队核算并核对地区金额");
    await removeAccess(owner);
    await teamSelect
      .locator("option", { hasText: "原团队已不可用" })
      .waitFor({ state: "attached" });
    assert.ok(
      await owner.getByRole("button", { name: "发送消息" }).isDisabled(),
    );
    assert.equal(await message.inputValue(), "跨团队核算并核对地区金额");
    await teamSelect.selectOption(process.env.PEER_RECIPIENT);
    const [created] = await Promise.all([
      owner.waitForResponse(
        (r) =>
          r.url().endsWith("/api/projects") && r.request().method() === "POST",
      ),
      owner.getByRole("button", { name: "发送消息" }).click(),
    ]);
    assert.ok(created.ok(), await created.text());
    const projectID = (await created.json()).id;
    await writeFile(
      dir + "/peer-registration.json",
      JSON.stringify(
        { tool: toolID, external: cardID, team, project: projectID },
        null,
        2,
      ),
    );
  }
} finally {
  await browser.close();
}

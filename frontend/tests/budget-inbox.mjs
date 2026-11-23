import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkBudgetInbox(browser, workspace) {
  const origin = process.env.SMOKE_ORIGIN;
  const finance = await browser.newContext({
    baseURL: origin,
    viewport: { width: 1360, height: 850 },
  });
  const manager = workspace.request,
    delegated = finance.request;
  async function value(r, status) {
    assert.equal(r.status(), status, await r.text());
    return status === 204 ? undefined : r.json();
  }
  try {
    const page = await finance.newPage();
    await page.goto("/");
    await page.getByLabel("邮箱").fill("participant@sample.invalid");
    await page
      .getByLabel("密码", { exact: true })
      .fill(process.env.BROWSER_FINANCE_PASSWORD);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.getByRole("heading", { name: "新对话", exact: true }).waitFor();
    const reviewer = await value(await delegated.get("/api/me"), 200);
    const admin = await value(await manager.get("/api/me"), 200);
    const assign = async (team, previous, member) =>
      value(
        await manager.put("/api/teams/" + team + "/budget-owner", {
          data: { previous, member, reason: "调配审批职责" },
        }),
        204,
      );
    async function prepare(teamName, title, isMember, amount) {
      const { id: team } = await value(
        await manager.post("/api/admin/teams", { data: { name: teamName } }),
        201,
      );
      await value(
        await manager.put(
          "/api/admin/teams/" + team + "/members/" + reviewer.id,
          { data: { joined: true, reason: "预算试点" } },
        ),
        204,
      );
      await assign(team, "", reviewer.id);
      const { id: project } = await value(
        await manager.post("/api/projects", { data: { goal: title, team } }),
        201,
      );
      const base = "/api/projects/" + project;
      if (isMember)
        await value(
          await manager.post(base + "/participants", {
            data: {
              expected: 1,
              member: reviewer.id,
              reason: "共同核对项目",
              review: false,
            },
          }),
          204,
        );
      await value(
        await delegated.put(base + "/budget", {
          data: {
            expected: 0,
            limit: "10",
            warningPercent: 80,
            reason: "试点预算",
          },
        }),
        204,
      );
      const { targets } = await value(
        await manager.get(base + "/budget-requests"),
        200,
      );
      const target = targets.find((t) => t.scope === "project");
      const { id } = await value(
        await manager.post(base + "/budget-requests", {
          data: {
            team,
            scope: "project",
            amount,
            expected: target.revision,
            reason: "补充一份成果",
            work: "复核材料",
            alternative: "减少范围",
          },
          headers: { "Idempotency-Key": project },
        }),
        200,
      );
      return { team, project, id, title };
    }
    const a = await prepare(
      "财务走查一组",
      "经营分析草稿保密标题",
      false,
      "2.125001",
    );
    const b = await prepare(
      "财务走查二组",
      "设备培训公开给项目成员",
      true,
      "3.100002",
    );
    const items = async (client) =>
      value(await client.get("/api/budget-inbox"), 200);
    const inbox = (await items(delegated)).filter((t) =>
      [a.id, b.id].includes(t.id),
    );
    assert.equal(inbox.length, 2);
    assert.equal(inbox.find((t) => t.id === a.id).projectTitle, null);
    assert.equal(inbox.find((t) => t.id === b.id).projectTitle, b.title);
    assert.ok(
      !(await items(manager)).some((t) => t.id === a.id || t.id === b.id),
    );
    assert.equal(
      (await manager.get("/api/budget-inbox/" + a.id)).status(),
      404,
    );
    assert.equal(
      (await delegated.get("/api/projects/" + a.project)).status(),
      404,
    );
    await page.getByRole("button", { name: "打开对话历史" }).click();
    const drawer = page.getByRole("dialog", { name: "对话历史" });
    await drawer.getByRole("button", { name: "我的待办" }).click();
    await drawer.getByLabel("待办类型").selectOption("budget");
    const search = drawer.getByRole("searchbox", { name: "搜索预算待办" });
    await search.fill("财务走查");
    await drawer.getByRole("button", { name: /追加 ¥2.125001/ }).waitFor();
    await drawer.getByRole("button", { name: /追加 ¥3.100002/ }).waitFor();
    assert.equal(await drawer.getByText(a.title, { exact: false }).count(), 0);
    await drawer.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/budget-inbox.png",
    });
    await search.fill("设备培训");
    assert.equal(
      await drawer.getByRole("button", { name: /追加 ¥2.125001/ }).count(),
      0,
    );
    const withdraw = await manager.post(
      "/api/budget-requests/" + b.id + "/decision",
      { data: { action: "withdraw", expected: 0, reason: "按原计划执行" } },
    );
    assert.equal(withdraw.status(), 204);
    await drawer.getByRole("button", { name: /追加 ¥3.100002/ }).click();
    await drawer.getByText("已撤回 · 追加 ¥3.100002").waitFor();
    assert.equal(
      await drawer.getByRole("button", { name: "批准追加" }).count(),
      0,
    );
    await drawer.getByRole("button", { name: "打开所属项目" }).click();
    await page.getByRole("heading", { name: b.title, exact: true }).waitFor();
    await page.getByRole("button", { name: "打开对话历史" }).click();
    await drawer.getByText("没有匹配的预算申请").waitFor();
    await search.fill("财务走查");
    await drawer.getByRole("button", { name: /追加 ¥2.125001/ }).click();
    await drawer.getByText(/批准后 ¥12.125001/).waitFor();
    assert.equal(
      await drawer.getByRole("button", { name: "打开所属项目" }).count(),
      0,
    );
    await assign(a.team, reviewer.id, admin.id);
    const denied = page.waitForResponse(
      (r) => r.url().endsWith("/decision") && r.request().method() === "POST",
    );
    await drawer.getByLabel("处理依据").fill("过期待办");
    await drawer.getByRole("button", { name: "批准追加" }).click();
    assert.equal((await denied).status(), 403);
    await drawer.getByText(/申请已不可见/).waitFor();
    assert.equal(
      await drawer.getByText("补充一份成果", { exact: true }).count(),
      0,
    );
    assert.ok(!(await items(delegated)).some((t) => t.id === a.id));
    assert.ok((await items(manager)).some((t) => t.id === a.id));
    await assign(a.team, admin.id, reviewer.id);
    await drawer.getByRole("button", { name: "刷新预算待办" }).click();
    await drawer.getByText(/批准后 ¥12.125001/).waitFor();
    const old = await value(
      await delegated.get("/api/budget-inbox/" + a.id),
      200,
    );
    await value(
      await delegated.put("/api/projects/" + a.project + "/budget", {
        data: {
          expected: old.revision,
          limit: "11",
          warningPercent: 80,
          reason: "额度先经另一窗口调整",
        },
      }),
      204,
    );
    await drawer.getByLabel("处理依据").fill("旧额度的处理意见");
    const changed = page.waitForResponse(
      (r) => r.url().endsWith("/decision") && r.request().method() === "POST",
    );
    await drawer.getByRole("button", { name: "批准追加" }).click();
    assert.equal((await changed).status(), 409);
    await drawer.getByText(/批准后 ¥13.125001/).waitFor();
    assert.equal(await drawer.getByLabel("处理依据").inputValue(), "");
    await drawer.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/budget-inbox-recheck.png",
    });
    await drawer.getByLabel("处理依据").fill("已核对金额和承担团队");
    await drawer.getByRole("button", { name: "批准追加" }).click();
    await drawer.getByText("已批准 · 追加 ¥2.125001").waitFor();
    await drawer.getByRole("button", { name: "返回预算待办" }).click();
    await drawer.getByText("没有匹配的预算申请").waitFor();
    const final = await value(
      await delegated.get("/api/budget-inbox/" + a.id),
      200,
    );
    assert.equal(final.applied, "13.125001");
    assert.equal(final.canResume, false);
    await writeFile(
      process.env.VISUAL_EVIDENCE + "/budget-inbox.json",
      JSON.stringify(
        {
          input: "synthetic",
          teams: inbox.length,
          unsharedTitle: inbox.find((t) => t.id === a.id).projectTitle,
          withdrawn: (
            await value(await delegated.get("/api/budget-inbox/" + b.id), 200)
          ).state,
          afterTransfer: 403,
          staleLimit: 409,
          approved: final.state,
          applied: final.applied,
        },
        null,
        2,
      ) + "\n",
    );
    console.log(
      "Browser: two-team budget inbox, project privacy, search, withdrawal, reassignment, stale decision and exact approval OK",
    );
  } finally {
    await finance.close();
  }
}

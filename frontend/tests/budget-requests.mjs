import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function reviewIncrease(browser, applicant, costs) {
  const finance = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN });
  try {
    await finance.goto(process.env.SMOKE_ORIGIN);
    await finance.getByLabel("邮箱").fill("participant@sample.invalid");
    await finance
      .getByLabel("密码", { exact: true })
      .fill(process.env.BROWSER_FINANCE_PASSWORD);
    await finance.getByRole("button", { name: "登录", exact: true }).click();
    await finance
      .getByRole("heading", { name: "新对话", exact: true })
      .waitFor();
    const member = await (await finance.request.get("/api/me")).json();
    const teams = await (await applicant.request.get("/api/teams")).json();
    const team = teams.find((t) => t.default);
    const assign = async (from, to) => {
      const result = await applicant.request.put(
        "/api/teams/" + team.id + "/budget-owner",
        { data: { member: to, previous: from, reason: "预算审批走查" } },
      );
      assert.equal(result.status(), 204);
    };
    await assign(team.budgetOwner, member.id);
    await costs.getByText("预算追加申请", { exact: true }).click();
    await costs.getByText("填写追加申请", { exact: true }).click();
    await costs.getByLabel("关联预算阻塞任务").selectOption({ index: 1 });
    await costs.getByLabel("追加金额（CNY）").fill("5.000001");
    await costs.getByLabel("额度不足原因").fill("需要制作本次简报");
    await costs.getByLabel("剩余工作与新增需求").fill("完成四个已规划步骤");
    await costs.getByLabel("可选替代方案").fill("沿用现有人工摘要");
    const project = new URL(applicant.url()).hash.slice(1);
    const run = await costs.getByLabel("关联预算阻塞任务").inputValue();
    const path = "/api/projects/" + project;
    await applicant
      .getByRole("button", { name: "新对话", exact: true })
      .click();
    await applicant
      .getByRole("heading", { name: "新对话", exact: true })
      .waitFor();
    const before = await (await applicant.request.get(path)).json();
    const changed = await finance.request.put(path + "/budget", {
      data: {
        expected: before.budget.revision,
        limit: before.budget.limit,
        warningPercent: before.budget.warningPercent === 70 ? 75 : 70,
        reason: "核对项目切换期间的预算变更",
      },
    });
    assert.ok(changed.ok());
    await applicant.goBack();
    await costs.getByText(/有预算申请草稿/).click();
    await costs.getByText("预算追加申请", { exact: true }).click();
    await costs.getByText("填写追加申请", { exact: true }).click();
    assert.equal(await costs.getByLabel("关联预算阻塞任务").inputValue(), run);
    assert.equal(
      await costs.getByLabel("追加金额（CNY）").inputValue(),
      "5.000001",
    );
    assert.equal(
      await costs.getByLabel("额度不足原因").inputValue(),
      "需要制作本次简报",
    );
    assert.equal(
      await costs.getByLabel("剩余工作与新增需求").inputValue(),
      "完成四个已规划步骤",
    );
    assert.equal(
      await costs.getByLabel("可选替代方案").inputValue(),
      "沿用现有人工摘要",
    );
    assert.ok(
      await costs.getByRole("button", { name: "提交预算申请" }).isDisabled(),
    );
    const conflict = costs
      .getByRole("status")
      .filter({ hasText: "预算或任务已更新" });
    await conflict.scrollIntoViewIfNeeded();
    await conflict.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/budget-draft-return.png",
    });
    await costs.getByRole("button", { name: "已核对当前预算与任务" }).click();
    const records = path + "/budget-requests";
    const receipts = [];
    const keys = [];
    const intercept = async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      const response = await route.fetch();
      assert.equal(response.status(), 200);
      receipts.push((await response.json()).id);
      keys.push(route.request().headers()["idempotency-key"]);
      if (receipts.length === 1) return route.abort("failed");
      return route.fulfill({ response });
    };
    await applicant.route("**" + records, intercept);
    await costs.getByRole("button", { name: "提交预算申请" }).click();
    await costs.getByText(/尚未收到明确回执/).waitFor();
    await costs.getByRole("button", { name: "提交预算申请" }).click();
    await costs.getByText("已提交预算申请。", { exact: true }).waitFor();
    await applicant.unroute("**" + records, intercept);
    assert.equal(receipts.length, 2);
    assert.ok(receipts[0] && receipts[0] === receipts[1]);
    assert.ok(keys[0] && keys[0] === keys[1]);
    const registered = await (await applicant.request.get(records)).json();
    assert.equal(registered.items.length, 1);
    assert.equal(registered.items[0].run, run);
    assert.equal(registered.items[0].id, receipts[0]);
    assert.equal(await costs.getByLabel("额度不足原因").inputValue(), "");
    assert.equal(await costs.getByText(/有预算申请草稿/).count(), 0);
    await costs.getByText("待处理 · 追加 ¥5.000001").waitFor();
    const submitted = costs
      .getByText("需要制作本次简报", { exact: true })
      .locator("..");
    assert.equal(
      await submitted.getByRole("button", { name: "批准追加" }).count(),
      0,
    );
    await finance.getByRole("button", { name: "我的工作区" }).click();
    await finance.getByText("协作与授权", { exact: true }).click();
    await finance.getByText(/^预算管理 · 1 项待审批$/).click();
    await finance.getByText("预算审批与记录", { exact: true }).click();
    const item = finance
      .getByText("需要制作本次简报", { exact: true })
      .locator("..");
    await item.getByText(/批准后 ¥5.000001/).waitFor();
    await item.getByLabel("处理依据").fill("已核对用途与额度");
    await item.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/budget-decision.png",
    });
    await item.getByRole("button", { name: "批准追加" }).click();
    await item.getByText("已批准 · 追加 ¥5.000001").waitFor();
    await costs.getByRole("button", { name: "刷新申请记录" }).click();
    await submitted.getByText("已批准 · 追加 ¥5.000001").waitFor();
    await submitted.getByText(/已核对用途与额度/).waitFor();
    await submitted.getByRole("button", { name: "复查并继续原任务" }).click();
    await costs.getByText("已提交原任务恢复请求。").waitFor();
    await writeFile(
      process.env.VISUAL_EVIDENCE + "/budget-draft-integration.json",
      JSON.stringify(
        {
          input: "synthetic",
          runtime: ["Chromium", "Go API", "PostgreSQL", "Temporal"],
          project,
          run,
          receipt: receipts[0],
          submissions: keys.length,
          savedRequests: registered.items.length,
          originalRunRestored: true,
          staleRevisionRequiredConfirmation: true,
          sameKeyAfterLostResponse: true,
          decision: "approved",
          appliedLimit: "5.000001",
          resumeRequested: true,
        },
        null,
        2,
      ),
    );
    await assign(member.id, team.budgetOwner);
  } finally {
    await finance.close();
  }
}

import assert from "node:assert/strict";
export async function checkTeamRules(context) {
  const page = await context.newPage();
  try {
    await page.goto("/#" + process.env.RULE_PROJECT);
    await page.getByText("方案采用的团队规则 · 1").click();
    await page.getByText(/v1 · 规则已到期、停用或当前无权查看/).waitFor();
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByText("协作与授权", { exact: true }).click();
    await page.getByRole("button", { name: "团队规则", exact: true }).click();
    const panel = page.getByRole("dialog");
    await panel.getByText("起草团队规则").click();
    const form = panel
      .getByText("起草团队规则", { exact: true })
      .locator("..")
      .locator("form");
    const body = "培训材料须标明适用岗位";
    await form.getByLabel("规则正文").fill(body);
    await form.getByRole("button", { name: "保存草稿" }).click();
    const item = panel.locator("article").filter({ hasText: body });
    await item.getByRole("button", { name: "发布 v1" }).click();
    await item.getByText("已发布 v1", { exact: true }).waitFor();
    await item.getByText("复查已发布 v1").click();
    await item.getByLabel("复查依据").fill("已向培训负责人核实");
    await item.getByRole("button", { name: "确认复查" }).click();
    await item
      .getByRole("button", { name: "确认复查", disabled: true })
      .waitFor();
    await item.getByRole("button", { name: "编辑草稿" }).click();
    await item.getByLabel("规则正文").fill("培训材料须标明适用岗位及学习目标");
    await item.getByRole("button", { name: "保存草稿" }).click();
    await item.getByText("草稿 v2", { exact: true }).waitFor();
    assert.equal(await item.getByText(body, { exact: true }).count(), 1);
    await item.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/team-rule.png",
    });
    await item.getByRole("button", { name: "停用已发布规则" }).click();
    await item.getByText(/已停用 ·/).waitFor();
    console.log(
      "Browser: pinned rule access, publication, review, separate new draft and deactivation",
    );
  } finally {
    await page.close();
  }
}

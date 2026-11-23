import assert from "node:assert/strict";

export async function checkResourceBudget(page) {
  await page.getByText("预算管理", { exact: true }).click();
  const teams = await (await page.request.get("/api/teams")).json();
  const ownDefault = teams.find((t) => t.default);
  assert.equal(
    await page
      .getByRole("combobox", { name: "管理团队", exact: true })
      .inputValue(),
    ownDefault.id,
    "预算管理优先打开本人负责的默认团队",
  );
  await page.getByText(/^资源额度 · /).click();
  const resource = page
    .getByText("Shared spend resource", { exact: true })
    .locator("..");
  await resource.getByText("调整预算", { exact: true }).click();
  await resource.getByLabel(/累计调用次数上限/).fill("5");
  await resource.getByLabel("调整原因").fill("已批准下一次复验");
  await resource.getByRole("button", { name: "确认预算调整" }).click();
  await resource.getByText(/累计调用 4 次 · 次数上限 5/).waitFor();
  await resource.getByText("调整预算", { exact: true }).click();
  const detail = await page.context().newPage();
  try {
    await detail.goto(
      process.env.SMOKE_ORIGIN + "/#" + process.env.RESOURCE_PROJECT,
    );
    const costs = detail.getByText(/^项目费用 ·/).locator("..");
    await costs.locator("summary").first().click();
    await costs.getByText("预算追加申请", { exact: true }).click();
    await costs.getByText("填写追加申请", { exact: true }).click();
    const target = costs.getByLabel("追加到");
    const option = await target
      .getByText(/Shared spend resource · 资源次数/)
      .getAttribute("value");
    await target.selectOption(option);
    await costs.getByLabel("追加调用次数").fill("2");
    await costs.getByLabel("额度不足原因").fill("需要两次资源复核");
    await costs.getByLabel("剩余工作与新增需求").fill("两份简报");
    await costs.getByLabel("可选替代方案").fill("人工校验");
    await costs.getByRole("button", { name: "提交预算申请" }).click();
    const item = costs
      .getByText("需要两次资源复核", { exact: true })
      .locator("..");
    await item.getByText("待处理 · 追加 2 次", { exact: true }).waitFor();
    await item.getByText(/当前额度 5 次 · 批准后 7 次/).waitFor();
    assert.equal(await item.getByText(/¥5.000000/).count(), 0);
    await item.getByLabel("处理依据").fill("按次数核准");
    await item.getByRole("button", { name: "批准追加" }).click();
    await item.getByText("已批准 · 追加 2 次", { exact: true }).waitFor();
    await item.getByText(/批准后额度 7 次/).waitFor();
    await item.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/resource-approval.png",
    });
  } finally {
    await detail.close();
  }
}

import assert from "node:assert/strict";
export async function checkCandidates(context) {
  const page = await context.newPage();
  try {
    await page.goto("/#" + process.env.CANDIDATE_PROJECT);
    const previous = await (
      await page.request.get("/api/projects/" + process.env.CANDIDATE_PROJECT)
    ).json();
    assert.equal(previous.suggestion.status, "stale");
    await page.getByText(/规划期间项目、授权或候选条件已变化/).waitFor();
    await page.getByText("根据对话推荐方案或直接回答", { exact: true }).click();
    await page
      .getByLabel("规划助手模型")
      .selectOption({ label: "Stub TLS v1" });
    assert.equal(await page.getByLabel(/授权一次规划调用/).isChecked(), false);
    await page.getByLabel(/授权一次规划调用/).check();
    const next = page.waitForRequest(
      (r) => r.url().endsWith("/suggestions") && r.method() === "POST",
    );
    await page.getByRole("button", { name: "请助手规划" }).click();
    assert.ok(
      (await next)
        .headers()
        ["idempotency-key"].endsWith(previous.suggestion.id),
    );
    await page.getByText(/规划助手建议 · 适合以文本交付/).waitFor();
    const recommended = page
      .getByText("核对推荐版本与替代能力", { exact: true })
      .locator("..");
    await recommended.locator(":scope > summary").click();
    await recommended
      .locator("article")
      .filter({ hasText: "培训摘要助手" })
      .getByText("本次推荐修订", { exact: true })
      .waitFor();
    await recommended.locator(":scope > summary").click();
    const editor = page
      .getByText("拟定协作方案", { exact: true })
      .locator("..");
    if ((await editor.getAttribute("open")) === null)
      await editor.locator(":scope > summary").click();
    const panel = editor
      .getByText("比较候选能力与验证依据", { exact: true })
      .locator("..");
    await panel.locator(":scope > summary").click();
    await panel.getByLabel("任务或验收关键词").fill("培训要点覆盖");
    await panel.getByRole("button", { name: "更新比较" }).click();
    const pick = panel.locator("article").first();
    await pick.getByText("描述命中已通过的验收项", { exact: true }).waitFor();
    assert.match(await pick.innerText(), /培训摘要助手 · v1/);
    await pick.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/candidate-proof.png",
    });
    await pick.getByRole("button", { name: "加入方案步骤" }).click();
    await panel.getByLabel("这一步的成果格式").selectOption("xlsx");
    const mismatch = panel
      .locator("article")
      .filter({ hasText: "培训摘要助手" });
    await mismatch.getByText(/成果格式不符/).waitFor();
    assert.equal(
      await mismatch.getByRole("button", { name: "加入方案步骤" }).count(),
      0,
    );
    await editor.getByRole("button", { name: "保存待确认方案" }).click();
    await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "团队能力库", exact: true }).click();
    const form = page
      .getByRole("heading", { name: "描述一个新 Agent" })
      .locator("..");
    await form.getByLabel("名称", { exact: true }).fill("培训摘要助手");
    const duplicate = form.locator(".similar-agents");
    await duplicate.locator(":scope > summary").click();
    await duplicate.getByText(/名称相同/).waitFor();
    assert.match(await duplicate.innerText(), /新版名称仅供草稿 · 草稿 v2/);
    await duplicate.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/similar-capability.png",
    });
    await duplicate.getByRole("button", { name: "定位到库中条目" }).click();
    assert.equal(
      await page.evaluate(() => document.activeElement.id),
      "library-agent-" + process.env.CANDIDATE_AGENT,
    );
    await duplicate.getByRole("button", { name: "套用发布版任务说明" }).click();
    assert.equal(
      await form.getByLabel("任务、规则与输出要求").inputValue(),
      "整理培训讲义的摘要文本",
    );
    assert.equal(
      await form.getByLabel("名称", { exact: true }).inputValue(),
      "培训摘要助手 副本",
    );
    assert.equal(
      await form.locator('select[name="resource"]').inputValue(),
      "",
    );
    assert.equal(await form.getByLabel(/此版本专用凭据/).inputValue(), "");
  } finally {
    await page.close();
  }
}

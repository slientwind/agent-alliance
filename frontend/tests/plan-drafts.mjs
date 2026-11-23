import assert from "node:assert/strict";

export async function checkPlanDrafts(context) {
  const page = await context.newPage();
  const api = context.request;
  const goal = "设备借用培训：准备入职说明";
  const myBrief = "面向新入职员工，交付一份借用说明和核对清单。";
  try {
    const created = await api.post("/api/projects", { data: { goal } });
    assert.equal(created.status(), 201);
    const { id } = await created.json();
    const base = "/api/projects/" + id;
    const src = await api.post(base + "/sources?name=equipment.txt", {
      data: Buffer.from("借用前需要部门确认。"),
    });
    assert.ok(src.ok());
    const { id: source } = await src.json();
    const pref = await api.post("/api/preferences", {
      data: { body: "按办理顺序列步骤", enabled: true },
    });
    assert.ok(pref.ok());
    const { id: rule } = await pref.json();
    await page.goto("/#" + id);
    const brief = page.getByLabel("交付目标与确认要求");
    await brief.fill(myBrief);
    const pick = page.getByLabel("添加执行步骤");
    const writer = await pick.getByText(/Writer/).getAttribute("value");
    const checker = await pick.getByText(/Checker/).getAttribute("value");
    await pick.selectOption(writer);
    await pick.selectOption(checker);
    await pick.selectOption(writer);
    const steps = page.locator(".plan-steps li");
    await steps.nth(2).locator("summary").click();
    await steps
      .nth(2)
      .getByLabel(/引用步骤 1/)
      .check();
    await page.getByLabel(/引用资料 · equipment/).selectOption("1");
    await page.getByLabel(/个人偏好 · 按办理顺序/).selectOption("1");
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    await page
      .getByRole("textbox", { name: "消息", exact: true })
      .fill("另一个项目的独立目标");
    await page.getByRole("button", { name: "发送消息" }).click();
    await page.getByRole("heading", { name: "另一个项目的独立目标" }).waitFor();
    assert.equal(await brief.inputValue(), "另一个项目的独立目标");
    assert.equal(await steps.count(), 0);
    await page.goBack();
    await page.getByRole("heading", { name: "新对话", exact: true }).waitFor();
    await page.goBack();
    await page.getByRole("heading", { name: goal }).waitFor();
    assert.equal(await brief.inputValue(), myBrief);
    assert.equal(await steps.count(), 3);
    assert.equal(
      await page.getByLabel(/引用资料 · equipment/).inputValue(),
      "1",
    );
    assert.equal(
      await page.getByLabel(/个人偏好 · 按办理顺序/).inputValue(),
      "1",
    );
    const latest = async () => (await api.get(base)).json();
    const before = await latest();
    const competing = {
      expected: before.revision,
      brief: "另一窗口已保存的方案",
      agents: [checker],
      dependencies: [[]],
    };
    assert.ok((await api.post(base + "/plan", { data: competing })).ok());
    const submit = page.getByRole("button", { name: "保存待确认方案" });
    // Keep the stale form visible until its first submission checks the pinned revision.
    const denied = page.waitForResponse(
      (r) =>
        r.url().endsWith(base + "/plan") && r.request().method() === "POST",
    );
    await submit.click();
    assert.equal((await denied).status(), 409);
    await page.getByText("项目已更新，请核对后继续编辑").waitFor();
    assert.ok(await submit.isDisabled());
    assert.equal(await brief.inputValue(), myBrief);
    assert.equal((await latest()).plan.brief, competing.brief);
    await page
      .getByText("项目已更新，请核对后继续编辑")
      .evaluate((el) => el.parentElement.scrollIntoView({ block: "start" }));
    await page.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/plan-draft-conflict.png",
    });
    await page.getByLabel(/授权本次 1/).check();
    await page
      .getByRole("button", { name: "已核对，继续使用我的草稿" })
      .click();
    await submit.click();
    await page.getByRole("heading", { name: "协作方案 v2" }).waitFor();
    const { plan } = await latest();
    assert.equal(plan.brief, myBrief);
    assert.deepEqual(
      plan.nodes.map((n) => n.agent),
      [writer, checker, writer],
    );
    assert.deepEqual(
      plan.nodes.map((n) => n.dependsOn),
      [[], [1], [1, 2]],
    );
    assert.deepEqual(plan.sources, [{ id: source, version: 1 }]);
    assert.deepEqual(plan.preferences, [{ id: rule, revision: 1 }]);
    assert.equal(await page.getByLabel(/授权本次 3/).isChecked(), false);
    await page.getByText("拟定协作方案", { exact: true }).click();
    await brief.fill("准备放弃的修改");
    await page.getByRole("button", { name: "丢弃草稿，使用当前内容" }).click();
    assert.equal(await brief.inputValue(), myBrief);
    assert.equal((await latest()).planVersion, 2);
    console.log(
      "Browser: project-scoped plan drafts, dependency and resource pins, stale save rejection, explicit rebase/discard and fresh execution consent OK",
    );
  } finally {
    await page.close();
  }
}

import assert from "node:assert/strict";

export async function verifyDeliveryEditions(page, original) {
  const base = "/api/projects/" + new URL(page.url()).hash.slice(1);
  async function post(path, data) {
    const response = await page.request.post(base + path, { data });
    assert.ok(response.ok(), path + " " + (await response.text()));
  }
  const oldPackage = await (
    await page.request.get(base + "/delivery/package?edition=1")
  ).body();
  await page.getByText("发起交付后修订", { exact: true }).click();
  await page
    .getByLabel("需要修改的已交付成果")
    .selectOption(original.items[0].artifact);
  await page
    .getByLabel("修订原因", { exact: true })
    .last()
    .fill("第一份简报的日期需要修正");
  await page.getByRole("button", { name: "创建修订待办" }).click();
  await page
    .getByRole("button", { name: "创建修订待办" })
    .waitFor({ state: "detached" });
  const detail = async () => (await page.request.get(base)).json();
  const before = await detail();
  assert.equal(before.stage, "review");
  const f = before.artifacts[0].feedback.find(
    (f) => f.body === "第一份简报的日期需要修正",
  );
  const target = "/artifacts/" + before.artifacts[0].id;
  await post(target + "/revise", {
    version: 2,
    body: "业务简报 · 已核准新日期",
    reason: "修正发布日期",
  });
  const updated = await detail();
  assert.deepEqual(
    updated.artifacts.map((a) => a.approved),
    [false, false, true, false],
  );
  const choice = target + "/feedback/" + f.id;
  await post(choice, {
    version: 3,
    action: "submit",
    expected: 1,
    reason: "提交新日期",
  });
  await post(choice, {
    version: 3,
    action: "resolve",
    expected: 2,
    reason: "已核对变更",
  });
  for (const a of updated.artifacts.filter((a) => !a.approved))
    await post("/artifacts/" + a.id + "/review", { version: a.version });
  await page.reload();
  const confirm = page.getByRole("form", { name: "最终交付清单" });
  await confirm.getByText("给接收人的交付说明（选填）").click();
  assert.equal(
    await confirm.getByLabel("已接受的使用限制").inputValue(),
    original.handoff.limitations,
  );
  await confirm.getByLabel("使用说明").fill("请采用更新后的日期。");
  await confirm.getByRole("button", { name: "确认并固定以上版本" }).click();
  await page.getByLabel("已确认的交付批次").waitFor();
  const receipt = page
    .locator("details")
    .filter({ has: page.getByText("查看已确认的交付清单", { exact: true }) })
    .last();
  await receipt.locator(":scope > summary").click();
  await receipt.getByText("与第 1 次交付比较").waitFor();
  await receipt.getByText("沿用 1 项已审核版本").waitFor();
  await receipt.getByText("交付说明已调整 · 使用说明").waitFor();
  const changed = receipt.getByText(/步骤 1 .*正文或摘要变更/).locator("..");
  await changed.locator(":scope > summary").click();
  await changed.getByText("业务简报 · 已核准新日期", { exact: true }).waitFor();
  const priorText = await (
    await page.request.get(base + "/delivery?edition=1")
  ).json();
  await changed.getByText(priorText.items[0].body, { exact: true }).waitFor();
  assert.equal(
    await receipt
      .locator("summary")
      .filter({ hasText: "上游引用换版" })
      .count(),
    2,
  );
  await receipt.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/delivery-editions.png",
  });
  const second = await (
    await page.request.get(base + "/delivery?edition=2")
  ).json();
  assert.deepEqual(
    second.changes.map((c) => [c.step, c.from, c.to]),
    [
      [1, 2, 3],
      [2, 2, 3],
      [4, 3, 4],
    ],
  );
  await page.getByLabel("已确认的交付批次").selectOption("1");
  await receipt.locator(":scope > summary").click();
  await receipt
    .getByText(original.handoff.instructions, { exact: false })
    .waitFor();
  const url = await page
    .getByText("下载交付包（ZIP）", { exact: true })
    .getAttribute("href");
  assert.match(url, /edition=1$/);
  assert.deepEqual(await (await page.request.get(url)).body(), oldPackage);
}

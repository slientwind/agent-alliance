import assert from "node:assert/strict";

export async function reviewInbox(page, cards) {
  const project = new URL(page.url()).hash.slice(1);
  const detail = await (
    await page.request.get("/api/projects/" + project)
  ).json();
  const feedback = cards.first().locator("details.feedback");
  await feedback.locator(":scope > summary").click();
  await feedback.getByLabel("反馈内容").fill("只给这份成果的草稿");
  const composer = page.getByRole("textbox", { name: "消息", exact: true });
  await composer.fill("保留在原对话的消息");
  await page.locator("input[type=file][hidden]").setInputFiles({
    name: "当前任务.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("current task"),
  });
  await page.getByRole("button", { name: "新对话", exact: true }).click();
  assert.equal(await composer.inputValue(), "");
  await page.getByRole("button", { name: "打开对话历史" }).click();
  const drawer = page.getByRole("dialog", { name: "对话历史" });
  await drawer.getByRole("searchbox").fill("未命中的待办样例");
  await drawer.getByText("没有匹配的对话").waitFor();
  await drawer.getByRole("button", { name: "我的待办" }).click();
  await drawer.getByText("没有匹配的成果待办").waitFor();
  await drawer.getByRole("searchbox").fill(detail.title);
  const reviews = drawer.getByRole("button", { name: /审核成果/ });
  await reviews.nth(3).waitFor();
  assert.equal(await reviews.count(), 4);
  for (let i = 0; i < 4; i++)
    assert.match(
      await reviews.nth(i).innerText(),
      new RegExp("步骤 " + (i + 1)),
    );
  await drawer.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/review-inbox.png",
  });
  const r = await page.request.post(
    "/api/projects/" +
      project +
      "/artifacts/" +
      detail.artifacts[0].id +
      "/review",
    { data: { version: 1 } },
  );
  assert.equal(r.status(), 204);
  await reviews.first().click();
  await page.getByText(/这条待办已更新/).waitFor();
  assert.equal(await composer.inputValue(), "保留在原对话的消息");
  await page.getByText("当前任务.txt", { exact: true }).waitFor();
  await feedback.locator(":scope > summary").click();
  assert.equal(
    await feedback.getByLabel("反馈内容").inputValue(),
    "只给这份成果的草稿",
  );
  await page.goBack();
  await page.getByRole("heading", { name: "新对话", exact: true }).waitFor();
  await page.goForward();
  await page
    .getByRole("heading", { name: detail.title, exact: true })
    .waitFor();
  await page.getByRole("button", { name: "打开对话历史" }).click();
  await reviews.nth(2).waitFor();
  assert.equal(await reviews.count(), 3);
  await reviews.first().click();
  await page.waitForFunction(() =>
    document.activeElement?.id.startsWith("review-"),
  );
  assert.equal(
    await page.locator(":focus").getAttribute("id"),
    "review-" + detail.artifacts[1].id + "-1",
  );
  console.log(
    "Browser: scoped review inbox, stale version, task focus, draft and attachment isolation, back and forward OK",
  );
}

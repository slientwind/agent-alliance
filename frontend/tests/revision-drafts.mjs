import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";

export async function checkRevisionDrafts(browser, workspace) {
  const page = await workspace.newPage();
  const guest = await browser.newContext({ baseURL: process.env.SMOKE_ORIGIN });
  const api = workspace.request;
  const textId = process.env.TEXT_REVISION_PROJECT;
  const fileId = process.env.PREVIEW_PROJECT;
  const data = async (id) => (await api.get("/api/projects/" + id)).json();
  const refresh = (tab) =>
    tab.evaluate(() => window.dispatchEvent(new Event("focus")));
  const cards = page.locator("article.card");
  const editor = (card) => card.locator(".revision-editor");
  const save = (form) => form.getByRole("button", { name: "保存新版本" });
  try {
    await page.goto("/#" + textId);
    const before = await data(textId);
    const current = before.artifacts[1];
    const card = cards.nth(1);
    await card.getByRole("button", { name: "修改当前稿" }).click();
    const form = editor(card);
    await form.getByLabel("修订正文").fill("我的交接修订草稿");
    await form.getByLabel("修改原因").fill("业务复核");
    await cards.first().getByRole("button", { name: "修改当前稿" }).click();
    await editor(cards.first()).getByLabel("修订正文").fill("独立步骤的草稿");
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    await page.getByRole("heading", { name: "新对话", exact: true }).waitFor();
    await page.goto("/#" + fileId);
    await cards.first().getByRole("button", { name: "上传外部修订稿" }).click();
    const fileForm = editor(cards.first());
    const fileItem = (await data(fileId)).artifacts[0];
    const path = "/api/projects/" + fileId + "/artifacts/" + fileItem.id;
    const bytes = async (version) =>
      (await api.get(path + "/file?version=" + version)).body();
    const first = await bytes(1),
      second = await bytes(2);
    const attachment = {
      name: "修订讲义.docx",
      mimeType:
        "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
      buffer: second,
    };
    await fileForm.locator("input[type=file]").setInputFiles(attachment);
    await fileForm.getByLabel("新版内容摘要").fill("交接职责按新版核对");
    await fileForm.getByLabel("修改原因").fill("讲义审核");
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    await page.goto("/#" + textId);
    await form.getByLabel("修订正文").waitFor();
    assert.equal(
      await form.getByLabel("修订正文").inputValue(),
      "我的交接修订草稿",
    );
    assert.equal(
      await editor(cards.first()).getByLabel("修订正文").inputValue(),
      "独立步骤的草稿",
    );
    const endpoint =
      "/api/projects/" + textId + "/artifacts/" + current.id + "/revise";
    assert.equal(
      (
        await api.post(endpoint, {
          data: {
            version: current.version,
            body: "另一审核人的新版",
            reason: "核对日期",
          },
        })
      ).status(),
      204,
    );
    const staleReply = page.waitForResponse(
      (r) => r.url().endsWith(endpoint) && r.request().method() === "POST",
    );
    await save(form).click();
    assert.equal((await staleReply).status(), 409);
    await form.getByText("另一审核人的新版", { exact: true }).waitFor();
    assert.ok(await save(form).isDisabled());
    assert.equal(await form.getByLabel("修改原因").inputValue(), "业务复核");
    await form
      .getByText("另一审核人的新版", { exact: true })
      .evaluate((el) => el.parentElement.scrollIntoView({ block: "start" }));
    await page.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/text-revision-conflict.png",
    });

    await form
      .getByRole("button", { name: "已核对新版，继续编辑草稿" })
      .click();
    await form.getByLabel("修订正文").fill("另一审核人的新版，补充交接人。");
    await save(form).click();
    await form.waitFor({ state: "detached" });
    const after = await data(textId);
    assert.equal(after.artifacts[1].version, current.version + 2);
    assert.equal(after.artifacts[2].version, before.artifacts[2].version + 2);
    assert.equal(after.artifacts[0].version, before.artifacts[0].version);
    assert.equal(after.artifacts[0].approved, true);
    assert.equal(
      await editor(cards.first()).getByLabel("修订正文").inputValue(),
      "独立步骤的草稿",
    );
    await page.goto("/#" + fileId);
    await fileForm.getByText("已选文件 · 修订讲义.docx").waitFor();
    assert.equal(
      await fileForm.getByLabel("新版内容摘要").inputValue(),
      "交接职责按新版核对",
    );
    const upload = await api.post(path + "/file-revisions", {
      multipart: {
        version: "2",
        reason: "外部核对",
        summary: "另一份已审核讲义",
        file: { ...attachment, buffer: first },
      },
    });
    assert.equal(upload.status(), 201);
    await refresh(page);
    await fileForm.getByText("另一份已审核讲义", { exact: true }).waitFor();
    assert.ok(await save(fileForm).isDisabled());
    await fileForm
      .getByRole("button", { name: "已核对新版，重新选择修订文件" })
      .click();
    assert.ok(await save(fileForm).isDisabled());
    await fileForm
      .locator("input[type=file]")
      .setInputFiles({ ...attachment, name: "wrong.xlsx" });
    await fileForm.getByRole("alert").waitFor();
    assert.ok(await save(fileForm).isDisabled());
    await fileForm.locator("input[type=file]").setInputFiles(attachment);
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    await page.getByRole("heading", { name: "新对话", exact: true }).waitFor();
    await page.goBack();
    await fileForm.getByText("已选文件 · 修订讲义.docx").waitFor();
    await save(fileForm).click();
    await fileForm.waitFor({ state: "detached" });
    assert.deepEqual(await bytes(4), second);
    assert.deepEqual(await bytes(3), first);
    const gp = await guest.newPage();
    await gp.goto("/");
    await gp.getByLabel("邮箱").fill("participant@sample.invalid");
    await gp
      .getByLabel("密码", { exact: true })
      .fill(process.env.BROWSER_FINANCE_PASSWORD);
    await gp.getByRole("button", { name: "登录", exact: true }).click();
    await gp.getByRole("heading", { name: "新对话", exact: true }).waitFor();
    const changeRights = async (review) =>
      assert.equal(
        (
          await api.post("/api/projects/" + fileId + "/participants", {
            data: {
              expected: (await data(fileId)).revision,
              email: "participant@sample.invalid",
              review,
              reason: "演练审核转交",
            },
          })
        ).status(),
        204,
      );
    await changeRights(true);
    await gp.goto("/#" + fileId);
    const gCard = gp.locator("article.card").first();
    await gCard.getByRole("button", { name: "上传外部修订稿" }).click();
    const gForm = editor(gCard);
    await gForm.getByLabel("新版内容摘要").fill("撤权前的草稿");
    await gForm.getByLabel("修改原因").fill("职责转交");
    await gForm
      .locator("input[type=file]")
      .setInputFiles({ ...attachment, buffer: first });
    await changeRights(false);
    const refused = gp.waitForResponse(
      (r) =>
        r.url().endsWith("/file-revisions") && r.request().method() === "POST",
    );
    await save(gForm).click();
    assert.equal((await refused).status(), 403);
    await gForm.waitFor({ state: "detached" });
    await changeRights(true);
    await refresh(gp);
    await gCard.getByRole("button", { name: "上传外部修订稿" }).click();
    assert.equal(await gForm.getByLabel("新版内容摘要").inputValue(), "");
    assert.equal((await data(fileId)).artifacts[0].version, 4);
    const receipt = {
      input: "synthetic",
      browser: "Chromium",
      text: {
        project: textId,
        artifact: current.id,
        base: current.version,
        rejectedStatus: 409,
        independentVersions: [
          before.artifacts[0].version,
          after.artifacts[0].version,
        ],
        before: before.artifacts.map((a) => a.version),
        after: after.artifacts.map((a) => a.version),
      },
      office: {
        project: fileId,
        artifact: fileItem.id,
        base: fileItem.version,
        final: (await data(fileId)).artifacts[0].version,
        previousSha256: createHash("sha256")
          .update(await bytes(3))
          .digest("hex"),
        submittedSha256: createHash("sha256")
          .update(attachment.buffer)
          .digest("hex"),
        storedSha256: createHash("sha256")
          .update(await bytes(4))
          .digest("hex"),
      },
      permission: {
        revokedStatus: 403,
        restoredSummary: await gForm.getByLabel("新版内容摘要").inputValue(),
      },
    };
    await writeFile(
      process.env.VISUAL_EVIDENCE + "/revision-drafts.json",
      JSON.stringify(receipt, null, 2) + "\n",
    );
    console.log(
      "Browser: independent text and Office drafts, concurrent revisions, downstream review scope, explicit file reselection, exact bytes and reviewer revocation OK",
    );
  } finally {
    await page.close();
    await guest.close();
  }
}

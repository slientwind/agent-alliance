import assert from "node:assert/strict";

export async function checkPreferences(browser, administrator) {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN });
  try {
    await page.goto("/");
    await page.getByLabel("邮箱").fill("preference-owner@sample.invalid");
    await page
      .getByLabel("密码", { exact: true })
      .fill(process.env.PREFERENCE_BROWSER_PASSWORD);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "个人偏好", exact: true }).click();
    const pane = page.getByRole("dialog");
    await pane.getByLabel("新增显式偏好").fill("请在工作简报末尾列出负责人");
    await pane.getByRole("button", { name: "保存", exact: true }).click();
    const item = pane
      .locator("article")
      .filter({ hasText: "请在工作简报末尾列出负责人" });
    async function click(name) {
      await item.getByRole("button", { name, exact: true }).click();
    }
    await click("确认仍适用");
    await click("保存复查确认");
    await click("移入回收区");
    await click("确认移入回收区");
    await pane.getByText("偏好回收区 · 1", { exact: true }).click();
    await item.getByText("已删除", { exact: true }).waitFor();
    await click("恢复偏好");
    await click("确认恢复偏好");
    await click("停用");
    await item.getByRole("button", { name: "启用", exact: true }).waitFor();
    let release;
    const resume = new Promise((done) => {
      release = done;
    });
    const isEnable = (request) =>
      request.method() === "PATCH" &&
      new URL(request.url()).pathname.startsWith("/api/preferences/") &&
      request.postDataJSON()?.enabled === true;
    await page.route(
      "**/api/preferences/*",
      async (route) => {
        if (isEnable(route.request())) await resume;
        await route.continue();
      },
      { times: 1 },
    );
    const finished = page.waitForResponse((response) =>
      isEnable(response.request()),
    );
    try {
      await Promise.all([page.waitForRequest(isEnable), click("启用")]);
      assert.ok(
        await item
          .getByRole("button", { name: "移入回收区", exact: true })
          .isDisabled(),
      );
    } finally {
      release();
      await finished;
    }
    await click("移入回收区");
    await click("确认移入回收区");
    await click("立即清除正文");
    await click("确认立即清除正文");
    await pane.getByText("偏好回收区 · 0", { exact: true }).waitFor();
    assert.equal(await item.count(), 0);
    await pane.getByLabel("新增显式偏好").fill("每段先给出结论");
    await pane.getByRole("button", { name: "保存", exact: true }).click();
    await pane.getByText("每段先给出结论", { exact: true }).waitFor();
    const admin = await administrator.newPage();
    try {
      await admin.goto("/");
      await admin.getByRole("button", { name: "我的工作区" }).click();
      await admin
        .getByRole("button", { name: "企业管理", exact: true })
        .click();
      await admin.getByRole("button", { name: "用户与监控", exact: true }).click();
      const member = admin.getByRole("button", {
        name: /Preference Owner · preference-owner/,
      });
      async function change(value) {
        await member.click();
        const form = admin.locator("form").filter({
          has: admin.getByRole("heading", { name: "修改 Preference Owner" }),
        });
        await form.getByLabel("变更类型").selectOption(value);
        await form.getByLabel("变更原因").fill("合成离职与返聘走查");
        await form
          .getByLabel("管理员当前密码")
          .fill(process.env.BROWSER_PASSWORD);
        await form.getByRole("button", { name: "确认变更" }).click();
        await form.waitFor({ state: "detached" });
      }
      await change("depart");
      assert.equal((await page.request.get("/api/me")).status(), 401);
      await change("return");
    } finally {
      await admin.close();
    }
    await page.reload();
    await page.getByLabel("邮箱").fill("preference-owner@sample.invalid");
    await page
      .getByLabel("密码", { exact: true })
      .fill(process.env.PREFERENCE_BROWSER_PASSWORD);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "个人偏好", exact: true }).click();
    await pane.getByText("偏好回收区 · 1", { exact: true }).click();
    const isolated = pane.locator("article").filter({ hasText: "离职隔离" });
    await isolated
      .getByRole("button", { name: "恢复偏好", exact: true })
      .click();
    assert.equal(
      await pane.getByText("每段先给出结论", { exact: true }).count(),
      0,
    );
    await isolated.screenshot({
      path: process.env.VISUAL_EVIDENCE + "/preference-recovery.png",
    });
    await isolated
      .getByLabel("验证本人密码")
      .fill(process.env.PREFERENCE_BROWSER_PASSWORD);
    await isolated
      .getByRole("button", { name: "确认恢复偏好", exact: true })
      .click();
    await pane.getByText("每段先给出结论", { exact: true }).waitFor();
    console.log(
      "Browser: review, recycle, clear, member departure and individually authenticated recovery OK",
    );
  } finally {
    await page.close();
  }
}

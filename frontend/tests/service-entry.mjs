import { checkServiceAccess } from "./service-access.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkServiceEntry({
  browser,
  baseURL,
  admin,
  member,
  team,
  project,
  documentAgent,
  password,
  out,
}) {
  const name = "培训资料成稿",
    example = "请根据本次培训资料整理一份可审核的 Word 简报。";
  const source = {
    name,
    instructions: "整理封闭培训的工作简报，依次核对资料并生成 Word 文档。",
    example,
  };
  const publisher = await admin.newPage();
  await publisher.goto("/");
  await publisher.getByRole("button", { name: "我的工作区" }).click();
  await publisher.getByRole("button", { name: "服务与渠道" }).click();
  await publisher.getByText("发布可复用服务", { exact: true }).click();
  async function publish(target, capability) {
    if (target) await publisher.getByLabel("发布目标").selectOption(target);
    await publisher.getByLabel("服务名称").fill(source.name);
    await publisher.getByLabel("能力来源").selectOption(capability);
    await publisher.getByLabel("对外服务要求").fill(source.instructions);
    await publisher.getByLabel(/使用示例/).fill(source.example);
    const submitted = publisher.waitForResponse(
      (r) =>
        r.url().endsWith("/api/services") && r.request().method() === "POST",
    );
    await publisher
      .getByRole("button", { name: target ? "确认发布新版本" : "发布固定版本" })
      .click();
    const response = await submitted;
    assert.equal(response.status(), 201, await response.text());
    return response.json();
  }
  const { id } = await publish("", "agent/" + documentAgent),
    entry = "/?service=" + id,
    path = "/api/services/" + id,
    descriptor = path + "/entry";
  const visitor = await browser.newContext({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  const page = await visitor.newPage(),
    errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  try {
    assert.equal((await visitor.request.get(descriptor)).status(), 401);
    await page.goto(entry);
    await page.getByLabel("邮箱").fill("staff@sample.invalid");
    await page.getByLabel("密码", { exact: true }).fill(password);
    await page.getByRole("button", { name: "登录", exact: true }).click();
    await page.getByRole("heading", { name }).waitFor();
    await page.getByRole("button", { name: "使用发布者示例" }).click();
    const msg = page.getByLabel("向服务提出任务");
    assert.equal(await msg.inputValue(), example);
    assert.ok(
      await page.getByRole("button", { name: "确认并启动对话" }).isDisabled(),
    );
    const reader = await (await member.request.get("/api/me")).json();
    const membership = "/api/admin/teams/" + team + "/members/" + reader.id;
    async function grant(joined) {
      const resp = await admin.request.put(membership, {
        data: { joined, reason: "网页服务受众核验" },
      });
      assert.equal(resp.status(), 204, await resp.text());
    }
    await grant(false);
    assert.equal((await visitor.request.get(descriptor)).status(), 404);
    assert.equal(
      (
        await visitor.request.post(path + "/chat", {
          headers: { "Idempotency-Key": crypto.randomUUID() },
          data: { version: 1, input: "无授权访问", authorizeCalls: 1 },
        })
      ).status(),
      404,
    );
    await grant(true);
    const upgrade = await publish(id, "project/" + project);
    assert.equal(upgrade.version, 2);
    await publisher.close();
    await page.getByRole("checkbox").check();
    await page.getByRole("button", { name: "确认并启动对话" }).click();
    await page.getByText(/服务已换版/).waitFor();
    await page.getByRole("checkbox", { name: /3 次调用/ }).waitFor();
    assert.equal(await page.getByRole("checkbox").isChecked(), false);
    const goal = "为本周的培训资料成稿，准备给运营组审核。";
    const newProject = (
      await (
        await member.request.post("/api/projects", { data: { goal } })
      ).json()
    ).id;
    await page.goto("/#" + newProject);
    await page.getByText(/可复用的团队服务 · /).click();
    await page.getByRole("link", { name: name + " · v2" }).click();
    await page.getByRole("button", { name: "带入原对话的首条需求" }).click();
    assert.equal(await msg.inputValue(), goal);
    const file = {
      name: "活动安排.md",
      mimeType: "text/markdown",
      buffer: Buffer.from("TRAINING_WEB_427：运营组周五核对场地。"),
    };
    await page.getByLabel("附资料").setInputFiles(file);
    await page.getByRole("checkbox").check();
    await page.screenshot({
      path: out + "/service-entry-mobile.png",
      fullPage: true,
    });
    let key, body, accepted;
    await page.route(
      "**" + path + "/chat",
      async (route) => {
        const request = route.request();
        key = request.headers()["idempotency-key"];
        body = request.postDataJSON();
        const response = await route.fetch();
        assert.equal(response.status(), 202, await response.text());
        accepted = await response.json();
        await route.abort("failed");
      },
      { times: 1 },
    );
    await page.getByRole("button", { name: "确认并启动对话" }).click();
    await page.getByRole("alert").waitFor();
    const retry = page.waitForRequest(
      (req) => req.url().endsWith(path + "/chat") && req.method() === "POST",
    );
    await page.getByRole("button", { name: "确认并启动对话" }).click();
    assert.equal((await retry).headers()["idempotency-key"], key);
    await page.waitForURL("**/#" + accepted.project);
    const meta = await (await member.request.get(descriptor)).json();
    assert.equal(meta.recent.length, 1);
    assert.equal(meta.recent[0].id, accepted.project);
    assert.deepEqual(
      (await (await admin.request.get(descriptor)).json()).recent,
      [],
    );
    const diff = await member.request.post(path + "/chat", {
      headers: { "Idempotency-Key": key },
      data: {
        ...body,
        attachment: {
          ...body.attachment,
          data: Buffer.from("不同内容").toString("base64"),
        },
      },
    });
    assert.equal(diff.status(), 409);
    const reviewer = await admin.newPage();
    await reviewer.goto("/#" + accepted.project);
    for (let i = 0; i < 3; i++)
      await reviewer
        .getByRole("button", { name: "审核此版本" })
        .first()
        .click();
    assert.equal(
      await page.getByRole("button", { name: "确认并固定以上版本" }).count(),
      0,
    );
    const base = "/api/projects/" + accepted.project,
      runProject = await (await member.request.get(base)).json();
    assert.equal(runProject.plan.sources.length, 1);
    assert.ok(runProject.artifacts[0].body.includes("TRAINING_WEB_427"));
    assert.equal(runProject.owner, reader.id);
    await reviewer.getByRole("button", { name: "确认并固定以上版本" }).click();
    await reviewer.getByRole("link", { name: /下载交付包/ }).waitFor();
    await reviewer.close();
    await page.goto(entry);
    await page.getByText(/我在此服务发起的任务/).click();
    await page.getByText("v2 · 已交付").waitFor();
    await page.getByRole("link", { name: goal }).click();
    const link = page.getByRole("link", { name: /下载交付包/ });
    const archive = await member.request.get(await link.getAttribute("href"));
    assert.equal(archive.status(), 200);
    await writeFile(out + "/service-entry-delivery.zip", await archive.body());
    await grant(false);
    assert.equal(
      (
        await visitor.request.post(path + "/chat", {
          headers: { "Idempotency-Key": key },
          data: body,
        })
      ).status(),
      404,
    );
    await grant(true);
    await checkServiceAccess({ admin, member, id, name, out });
    assert.deepEqual(errors, []);
    await writeFile(
      out + "/service-entry.json",
      JSON.stringify(
        {
          input: "synthetic",
          sourceFormat: "Markdown",
          sourceRefIncluded: true,
          loginRedirect: true,
          memberRecommendation: true,
          reconfirmedUpgrade: [1, 2],
          responseLossReplay: true,
          memberTasks: meta.recent.length,
          reviewedVersions: runProject.artifacts.map((a) => a.version),
          project: accepted.project,
          revokedReplayDenied: true,
        },
        null,
        2,
      ),
    );
  } finally {
    await visitor.close();
  }
}

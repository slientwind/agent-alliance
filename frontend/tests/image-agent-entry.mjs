import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  QUICK_ENDPOINT: endpoint,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: dir,
} = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const page = await browser.newPage({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  const email = "visual-author@sample.invalid";
  assert.equal(
    (
      await admin.request.post("/api/login", {
        data: { email: "review@sample.invalid", password },
      })
    ).status(),
    200,
  );
  const invite = await (
    await admin.request.post("/api/admin/invitations", { data: {} })
  ).json();
  assert.equal(
    (
      await page.request.post("/api/register", {
        data: { token: invite.token, name: "制图成员", email, password },
      })
    ).status(),
    201,
  );
  const teams = await (await page.request.get("/api/teams")).json();
  const team = teams.find((t) => t.joined && t.default);
  // Use a distinct team to exercise model selection in populated installations.
  const isolated = await (
    await admin.request.post("/api/admin/teams", {
      data: { name: "制图工作室" },
    })
  ).json();
  const who = await (await page.request.get("/api/me")).json();
  assert.equal(
    (
      await admin.request.put(
        "/api/admin/teams/" + isolated.id + "/members/" + who.id,
        { data: { joined: true, reason: "验证图片制作" } },
      )
    ).status(),
    204,
  );
  assert.ok(team);
  const resource = await admin.request.post("/api/resources", {
    data: {
      team: isolated.id,
      name: "合成生图资源",
      operation: "png",
      endpoint,
      model: "synthetic-image",
      key: "synthetic-image-key",
      estimate: "0",
    },
  });
  assert.equal(resource.status(), 201);
  const { id: model } = await resource.json();
  const goal = "为团队培训制作一张几何构图封面";
  await page.goto("/");
  const composer = page.getByRole("textbox", { name: "消息", exact: true });
  await composer.fill(goal);
  const launch = page.getByRole("button", { name: "选择或创建 Agent" });
  await launch.click();
  const panel = page.getByRole("dialog", { name: "Agent", exact: true });
  await panel.getByRole("button", { name: "创建 Agent" }).click();
  assert.equal(
    await panel.getByLabel("希望 Agent 完成什么任务？").inputValue(),
    goal,
  );
  await panel.getByLabel("成果类型").selectOption("png");
  const models = panel.getByLabel("生图模型");
  const details = panel
    .locator("details")
    .filter({ has: page.getByText("名称与模型", { exact: true }) });
  if (!(await details.evaluate((el) => el.open)))
    await details.locator("summary").click();
  await models.selectOption(model + "/1");
  const create = page.waitForResponse(
    (r) => r.url().endsWith("/api/agents") && r.request().method() === "POST",
  );
  await panel.getByRole("button", { name: "创建并准备试运行" }).click();
  const created = await create;
  assert.equal(created.status(), 201);
  const { id } = await created.json(),
    path = "/api/agents/" + id;
  await panel.getByLabel("样图要求").fill("使用圆形作为培训封面的主要图案");
  await panel.getByLabel("看图时要确认什么？").fill("圆形图案清晰可见");
  assert.equal(await panel.getByLabel("看图验收人").inputValue(), email);
  await panel.getByLabel("看图验收人").fill("unknown@sample.invalid");
  await panel.getByRole("button", { name: "保存样图要求" }).click();
  await panel.getByText("业务验收人须为当前团队的有效成员").waitFor();
  assert.equal(
    await panel.getByLabel("看图时要确认什么？").inputValue(),
    "圆形图案清晰可见",
  );
  const approver = await (await admin.request.get("/api/me")).json();
  await panel.getByLabel("看图验收人").fill(approver.email);
  await panel.getByRole("button", { name: "保存样图要求" }).click();
  await panel.getByText("共享样例 · 使用圆形作为培训封面的主要图案").waitFor();
  assert.equal(
    (await (await page.request.get(path + "/evidence")).json()).length,
    0,
  );
  const colleague = await browser.newContext({ baseURL });
  const anotherInvite = await (
    await admin.request.post("/api/admin/invitations", { data: {} })
  ).json();
  assert.equal(
    (
      await colleague.request.post("/api/register", {
        data: {
          token: anotherInvite.token,
          name: "同团队成员",
          email: "visual-peer@sample.invalid",
          password,
        },
      })
    ).status(),
    201,
  );
  const peer = await (await colleague.request.get("/api/me")).json();
  assert.equal(
    (
      await admin.request.put(
        "/api/admin/teams/" + isolated.id + "/members/" + peer.id,
        { data: { joined: true, reason: "检查同团队成员视图" } },
      )
    ).status(),
    204,
  );
  const sameTeamDraft = (
    await (await colleague.request.get("/api/agents")).json()
  ).find((a) => a.id === id);
  assert.ok(sameTeamDraft);
  assert.equal(sameTeamDraft.imageDraft, undefined);
  await colleague.close();
  const consent = panel.getByLabel(/授权以共享样例试运行一次/);
  await consent.check();
  await panel.getByRole("button", { name: "生成样图" }).click();
  await panel.getByText(/技术检查通过.*待业务验收/).waitFor();
  assert.equal(
    (
      await admin.request.post(path + "/publish", { data: { revision: 1 } })
    ).status(),
    409,
  );
  await panel.getByText("预览 image.png", { exact: true }).click();
  await page.waitForFunction(
    () => document.querySelector('img[alt="image.png"]')?.naturalWidth === 320,
  );
  const img = panel.getByRole("img", { name: "image.png" });
  assert.equal(await img.evaluate((el) => el.naturalHeight), 192);
  await img.scrollIntoViewIfNeeded();
  await page.screenshot({ path: dir + "/image-agent-sample-mobile.png" });
  assert.equal(await panel.getByLabel("业务结论").count(), 0);
  await panel.getByRole("button", { name: "关闭", exact: true }).click();
  await launch.click();
  await panel.getByText(/我的生图草稿与验收/).click();
  await panel.getByRole("button", { name: new RegExp(goal) }).click();
  await panel.getByText(/技术检查通过.*待业务验收/).waitFor();
  assert.equal(
    await panel.getByRole("button", { name: "发布已验证版本" }).count(),
    0,
  );
  const trial = (await (await page.request.get(path + "/checks/1")).json())
    .items[0].latestTrial;
  assert.ok(trial);
  assert.equal(
    (
      await page.request.post(path + "/evidence/" + trial + "/review", {
        data: { passed: true, reason: "代验收" },
      })
    ).ok(),
    false,
  );
  assert.equal(
    (
      await page.request.post(path + "/publish", { data: { revision: 1 } })
    ).status(),
    403,
  );
  await admin.goto("/");
  await admin.getByRole("button", { name: "选择或创建 Agent" }).click();
  const review = admin.getByRole("dialog", { name: "Agent", exact: true });
  await review.getByText(/我的生图草稿与验收/).click();
  await review.getByRole("button", { name: new RegExp(goal) }).click();
  await review.getByText("预览 image.png", { exact: true }).click();
  await admin.waitForFunction(
    () => document.querySelector('img[alt="image.png"]')?.naturalWidth === 320,
  );
  const membership =
    "/api/admin/teams/" + isolated.id + "/members/" + approver.id;
  async function verifyMembership(joined) {
    assert.equal(
      (
        await admin.request.put(membership, {
          data: { joined, reason: "复核样图访问权限" },
        })
      ).status(),
      204,
    );
    await Promise.all([
      admin.waitForResponse((r) => r.url().endsWith("/api/teams")),
      admin.evaluate(() => window.dispatchEvent(new Event("focus"))),
    ]);
  }
  await verifyMembership(false);
  await review.getByText("当前无法访问这份 Agent 草稿。").waitFor();
  assert.equal(await review.getByRole("img", { name: "image.png" }).count(), 0);
  assert.equal(await review.getByLabel("业务结论").count(), 0);
  assert.equal(
    (await admin.request.get(path + "/evidence/" + trial + "/file")).status(),
    404,
  );
  assert.equal(
    (
      await admin.request.post(path + "/evidence/" + trial + "/review", {
        data: { passed: true, reason: "已撤权" },
      })
    ).ok(),
    false,
  );
  await verifyMembership(true);
  await review.getByText("预览 image.png", { exact: true }).click();
  await admin.waitForFunction(
    () => document.querySelector('img[alt="image.png"]')?.naturalWidth === 320,
  );
  await review.getByLabel("业务结论").selectOption("pass");
  await review.getByLabel("验收依据").fill("已打开样图，圆形轮廓清晰。");
  await review.getByRole("button", { name: "提交业务验收" }).click();
  await review.getByRole("button", { name: "发布已验证版本" }).click();
  await review.getByLabel("搜索 Agent").waitFor();
  assert.equal(
    (await (await page.request.get("/api/agents")).json()).find(
      (a) => a.id === id,
    ).imageDraft,
    undefined,
  );
  await panel.getByRole("button", { name: "关闭", exact: true }).click();
  assert.equal(await composer.inputValue(), goal);
  const beforeReuse = (await (await page.request.get("/api/agents")).json())
    .length;
  await launch.click();
  await panel.getByRole("button", { name: "创建 Agent" }).click();
  await panel.getByLabel("成果类型").selectOption("png");
  await panel.getByText(/可直接使用的相似 Agent/).click();
  const reuse = panel.getByRole("group", {name: goal + " v1 · 制图工作室", exact:true})
    .getByRole("button", { name: "使用此 Agent" });
  await reuse.scrollIntoViewIfNeeded();
  await page.screenshot({ path: dir + "/image-agent-reuse-mobile.png" });
  await reuse.click();
  assert.equal(await composer.inputValue(), goal);
  assert.equal(
    (await (await page.request.get("/api/agents")).json()).length,
    beforeReuse,
  );
  assert.equal(
    (await (await page.request.get(path + "/evidence")).json()).length,
    1,
  );
  await page.getByRole("button", { name: "发送消息" }).click();
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const project = "/api/projects/" + new URL(page.url()).hash.slice(1);
  const read = async () => (await page.request.get(project)).json();
  assert.equal((await read()).plan.nodes[0].operation, "png");
  await page.getByLabel(/授权本次 1/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  await page.getByRole("button", { name: "审核此版本" }).click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  await page.waitForFunction(() =>
    document.body.textContent.includes("已交付"),
  );
  const delivered = await read();
  assert.equal(delivered.stage, "delivered");
  assert.equal(delivered.artifacts[0].file, "image.png");
  const download = page.waitForEvent("download");
  await page.getByText("下载交付包（ZIP）", { exact: true }).click();
  const zip = dir + "/image-agent-delivery.zip";
  await (await download).saveAs(zip);
  const unpack = (name) => execFileSync("unzip", ["-p", zip, name]);
  const manifest = JSON.parse(unpack("manifest.json"));
  assert.equal(manifest.items.length, 1);
  const item = manifest.items[0];
  const png = unpack(item.path);
  assert.equal(item.version, delivered.artifacts[0].version);
  assert.equal(item.sha256, createHash("sha256").update(png).digest("hex"));
  assert.equal(png.subarray(0, 8).toString("hex"), "89504e470d0a1a0a");
  assert.deepEqual([png.readUInt32BE(16), png.readUInt32BE(20)], [320, 192]);
  const checks = await (await page.request.get(path + "/checks/1")).json();
  assert.equal(checks.items[0].verdict, true);
  await writeFile(
    dir + "/image-agent-entry.json",
    JSON.stringify(
      {
        inputs: "synthetic, local image provider",
        agent: id,
        project: delivered.id,
        version: 1,
        image: [320, 192],
        packageSHA256: item.sha256,
        sameTeamPeerHasPersonalShortcut: false,
        reusedPublishedAgentDuringCreation: true,
        separateReviewer: true,
        revokedReviewerFileStatus: 404,
        businessVerdict: checks.items[0].verdict,
        memberPublishStatus: 403,
        stage: delivered.stage,
        originalGoalRetained: true,
      },
      null,
      2,
    ) + "\n",
  );
} finally {
  await browser.close();
}

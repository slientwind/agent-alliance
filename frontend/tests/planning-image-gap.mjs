import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkPlanningImage(
  member,
  admin,
  out,
  resource,
  word,
  real,
) {
  const mode = real ? "local" : "fixture",
    stem = out + "/planning-image-gap-" + mode;
  const get = async (client, path) => {
    const r = await client.get(path);
    assert.ok(r.ok(), await r.text());
    return r.json();
  };
  const post = async (client, path, data) => {
    const r = await client.post(path, { data });
    assert.ok(r.ok(), await r.text());
    return r.status() === 204 ? null : r.json();
  };
  const planning = (await get(member.request, "/api/resources")).find(
    (m) => m.id === resource,
  );
  const reviewer = await get(admin.request, "/api/me");
  const template = await post(admin.request, "/api/resources", {
    team: planning.team,
    name: "项目样图模型 " + mode,
    operation: "png",
    model: "synthetic-image",
    endpoint: process.env.QUICK_ENDPOINT,
    key: "synthetic-image-key",
    estimate: "0",
  });
  const goal =
    "制作两个独立成果：把附件中的培训安排原样排成 Word，再生成一张几何图案 PNG 封面，圆形放在中央。";
  const project = await post(member.request, "/api/projects", {
    team: planning.team,
    goal: (real ? "" : "MULTI_FILE_SCOPE ") + goal,
  });
  const path = "/api/projects/" + project.id;
  const source = await member.request.post(
    path + "/sources?name=" + encodeURIComponent("培训安排.txt"),
    { data: Buffer.from("09:15 开场\n10:30 交流") },
  );
  assert.ok(source.ok(), await source.text());
  const pin = (await source.json()).id;
  const receipt = await member.request.post(path + "/suggestions", {
    headers: { "Idempotency-Key": crypto.randomUUID() },
    data: {
      expected: (await get(member.request, path)).revision,
      resource,
      revision: 1,
      sources: [{ id: pin, version: 1 }],
      authorizeOneCall: true,
      requiredOutputs: ["png", "docx"],
    },
  });
  const { status, result, id } = await receipt.json();
  assert.equal(status, "ready", JSON.stringify(result));
  const gaps = result.outputGaps || {};
  const label = Object.keys(gaps).find((k) => gaps[k] === "png");
  assert.ok(label, JSON.stringify(result));
  await member.goto("/#" + project.id);
  const card = member.getByRole("region", { name: "能力补齐 · " + label });
  await card.getByLabel("补齐方式").selectOption("image");
  const imageModels = (await get(member.request, "/api/resources")).filter(
    (m) =>
      m.operation === "png" &&
      m.team === planning.team &&
      !m.credentialBlocked &&
      !m.budgetBlocked,
  );
  assert.ok(imageModels.some((m) => m.id === template.id));
  if (imageModels.length === 1) {
    await card
      .getByText(new RegExp("使用 项目样图模型 " + mode + " v1"))
      .waitFor();
    assert.equal(await card.getByText("此 Agent 的专用凭据").count(), 0);
  } else {
    const switchModel = card.getByRole("button", { name: "更换生图模型" });
    if (await switchModel.isVisible()) await switchModel.click();
    await card
      .getByLabel("生图模型", { exact: true })
      .selectOption(template.id + "/1");
  }
  await card.getByLabel("草稿名称").fill("培训封面制作 " + mode);
  await card
    .getByLabel("输入、处理规则与交付要求")
    .fill("依据业务要求生成一张培训封面 PNG，使用简单几何图形。");
  await card
    .getByLabel("样图要求", { exact: true })
    .fill("蓝灰背景，正中央有一个清晰的圆。");
  await card.getByLabel("看图时要确认什么？").fill("可辨识的圆形");
  await card.getByRole("heading").scrollIntoViewIfNeeded();
  await member.screenshot({ path: stem + "-draft.png" });
  await card.getByText("样图验收 · 我", { exact: true }).click();
  const before = (await get(member.request, "/api/agents")).length;
  const withReviewer = async (email) => {
    await card.getByLabel("看图验收人").fill(email);
    const pending = member.waitForResponse(
      (r) => r.url().includes(id + "/gaps/") && r.request().method() === "PUT",
    );
    await card.getByRole("button", { name: "保存为待验证草稿" }).click();
    return pending;
  };
  const denied = await withReviewer("absent@sample.invalid");
  assert.equal(denied.status(), 422);
  const withoutImageCheck = await member.request.put(denied.url(), {
    data: { ...denied.request().postDataJSON(), mode: "draft" },
  });
  assert.equal(withoutImageCheck.status(), 422);
  assert.equal((await get(member.request, "/api/agents")).length, before);
  const bound = await withReviewer(reviewer.email);
  assert.ok(bound.ok(), await bound.text());
  const agent = (await bound.json()).agent,
    item = "/api/agents/" + agent;
  const same = await member.request.put(bound.url(), {
    data: bound.request().postDataJSON(),
  });
  assert.equal(same.status(), 409);
  assert.equal((await get(member.request, "/api/agents")).length, before + 1);
  assert.equal((await get(member.request, path)).costSummary.calls, 1);
  const suite = (await get(member.request, item + "/checks/1")).items;
  assert.equal(suite.length, 1);
  assert.equal(suite[0].reviewer, reviewer.email);
  assert.equal(suite[0].name, "可辨识的圆形");
  assert.equal(suite[0].latestTrial, null);
  await member.reload();
  await card.getByText("验证并发布此草稿", { exact: true }).click();
  assert.equal(
    await card.getByRole("button", { name: "执行缺口试运行" }).count(),
    0,
  );
  const consent = card.getByLabel(/授权以共享样例试运行一次/);
  await consent.check();
  await card.getByRole("button", { name: "生成样图" }).click();
  await card.getByText(/技术检查通过 · 待业务验收/).waitFor();
  const projectAfterTrial = await get(member.request, path);
  assert.equal(projectAfterTrial.costSummary.calls, 2);
  assert.equal(
    projectAfterTrial.suggestion.bindings.find((b) => b.agent === agent).ready,
    false,
  );
  const latest = (await get(member.request, item + "/checks/1")).items[0]
    .latestTrial;
  assert.equal(
    (
      await admin.request.post(item + "/publish", { data: { revision: 1 } })
    ).status(),
    409,
  );
  assert.ok(
    !(
      await member.request.post(item + "/evidence/" + latest + "/review", {
        data: { passed: true, reason: "代验收" },
      })
    ).ok(),
  );
  await admin.goto("/");
  await admin.getByRole("button", { name: "选择或创建 Agent" }).click();
  const drawer = admin.getByRole("dialog", { name: "Agent", exact: true });
  await drawer.getByText(/我的生图草稿与验收/).click();
  await drawer
    .getByRole("button", { name: new RegExp("培训封面制作 " + mode) })
    .click();
  await drawer.getByText("预览 image.png", { exact: true }).click();
  await admin.waitForFunction(
    () => document.querySelector('img[alt="image.png"]')?.naturalWidth === 320,
  );
  await drawer.screenshot({ path: stem + "-review.png" });
  await drawer.getByLabel("业务结论").selectOption("pass");
  await drawer.getByLabel("验收依据").fill("已查看样图，几何圆形清晰可见。");
  await drawer.getByRole("button", { name: "提交业务验收" }).click();
  await drawer
    .getByText(/业务验收通过/)
    .first()
    .waitFor();
  const publication = admin.waitForResponse(
    (r) =>
      r.url().endsWith(item + "/publish") && r.request().method() === "POST",
  );
  await drawer.getByRole("button", { name: "发布已验证版本" }).click();
  assert.ok((await publication).ok());
  await drawer.getByLabel("搜索 Agent").waitFor();
  await member.reload();
  await card.getByRole("heading", { name: "已补齐 · " + label }).waitFor();
  await card.getByText("更换能力", { exact: true }).click();
  await card.getByLabel("补齐方式").selectOption("image");
  await card.getByLabel("草稿名称").fill("几何图形设计");
  await card
    .getByLabel("输入、处理规则与交付要求")
    .fill("为学员培训绘制封面，画出几何图形。");
  await card.getByText(/可直接使用的相似 Agent/).click();
  const alternative = card
    .locator(".similar-agents > div")
    .filter({ hasText: "培训封面制作 " + mode });
  await alternative.getByText(/任务词重合/).waitFor();
  await alternative.getByRole("button", { name: "使用此 Agent" }).click();
  assert.equal(await card.getByLabel("已发布能力").inputValue(), agent);
  const rebind = member.waitForResponse(
    (r) => r.url() === bound.url() && r.request().method() === "PUT",
  );
  await card.getByRole("button", { name: "绑定所选版本" }).click();
  assert.ok((await rebind).ok());
  assert.equal((await get(member.request, "/api/agents")).length, before + 1);
  const reused = await get(member.request, path);
  assert.equal(reused.costSummary.calls, 2);
  const pinned = reused.suggestion.bindings.find(
    (b) => b.slot === result.missing.indexOf(label),
  );
  assert.ok(pinned?.ready);
  assert.equal(pinned.agent, agent);
  assert.equal(pinned.version, 1);
  for (const [gap, format] of Object.entries(gaps)) {
    if (format !== "docx") continue;
    const other = member.getByRole("region", { name: "能力补齐 · " + gap });
    await other.getByLabel("补齐方式").selectOption("reuse");
    await other.getByLabel("已发布能力").selectOption(word);
    await other.getByRole("button", { name: "绑定所选版本" }).click();
    await other.getByRole("heading", { name: "已补齐 · " + gap }).waitFor();
  }
  await member.getByLabel("各文件分别使用已选原件制作").check();
  await member.getByRole("button", { name: "采用为待确认方案" }).click();
  await member.getByLabel(/授权本次/).check();
  await member.getByRole("button", { name: "确认并执行" }).click();
  for (let i = 0; i < 2; i++)
    await member.getByRole("button", { name: "审核此版本" }).first().click();
  await member.getByRole("button", { name: "确认并固定以上版本" }).click();
  const link = member.getByRole("link", { name: "下载交付包（ZIP）" });
  await link.waitFor();
  const archive = await member.request.get(await link.getAttribute("href"));
  assert.ok(archive.ok());
  await writeFile(stem + ".zip", await archive.body());
  const delivered = await get(member.request, path);
  assert.equal(delivered.stage, "delivered");
  assert.equal(delivered.costSummary.calls, 4);
  await writeFile(
    stem + ".json",
    JSON.stringify(
      {
        mode,
        project: project.id,
        agent,
        trial: latest,
        run: delivered.run.id,
        sampleReviewer: reviewer.id,
        finalReviewer: delivered.reviewer,
        plan: delivered.plan.nodes,
        calls: delivered.costSummary.calls,
        imageProvider: "local HTTPS protocol fixture",
        reusedImage: {
          agent: pinned.agent,
          version: pinned.version,
          callsAfterReuse: reused.costSummary.calls,
        },
      },
      null,
      2,
    ),
  );
}

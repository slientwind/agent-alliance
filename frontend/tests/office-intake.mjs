import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  QUICK_ENDPOINT: endpoint,
  OFFICE_INPUTS: documents,
  OFFICE_FIXTURES: samples,
  VISUAL_EVIDENCE: evidence,
} = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const member = await browser.newPage({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  async function post(page, endpoint, data) {
    const response = await page.request.post("/api/" + endpoint, { data });
    assert.ok(response.ok(), await response.text());
    return response.status() === 204 ? null : response.json();
  }
  await post(admin, "login", { email: "review@sample.invalid", password });
  const model = await post(admin, "resources", {
    name: "文档核对模型",
    model: "fixture",
    endpoint,
    key: "synthetic",
  });
  const agent = await post(admin, "agents", {
    name: "访谈资料核对助手",
    instructions: "核对附件中的访谈回执",
    resource: model.id,
    resourceRevision: 1,
  });
  assert.equal(
    (
      await post(admin, "agents/" + agent.id + "/validate", {
        revision: 1,
        input: "verify",
        expected: "RECEIPT",
        authorizeOneCall: true,
      })
    ).status,
    "passed",
  );
  await post(admin, "agents/" + agent.id + "/publish", { revision: 1 });
  const invite = await post(admin, "admin/invitations", {});
  await post(member, "register", {
    email: "office-user@sample.invalid",
    name: "业务用户",
    token: invite.token,
    password,
  });
  await member.goto("/");
  await member.getByRole("button", { name: "选择或创建 Agent" }).click();
  await member.getByRole("button", { name: /访谈资料核对助手/ }).click();
  assert.equal(await member.getByLabel("项目所属团队").count(), 0);
  await member
    .getByRole("textbox", { name: "消息", exact: true })
    .fill("整理这份访谈材料，给我一条可审核的回执。");
  await member
    .locator('.composer input[type="file"]')
    .setInputFiles(join(documents, "访谈记录.docx"));
  await member.getByRole("button", { name: "移除附件 访谈记录.docx" }).click();
  assert.equal(await member.locator(".file-chip").count(), 0);
  assert.ok(
    await member
      .getByRole("textbox", { name: "消息", exact: true })
      .inputValue(),
  );
  await member
    .locator('.composer input[type="file"]')
    .setInputFiles(join(documents, "访谈记录.docx"));
  await member
    .locator(".composer")
    .screenshot({ path: join(evidence, "agent-composer-mobile.png") });
  const saved = member.waitForResponse(
    (r) =>
      r.url().includes("/sources?name=") && r.request().method() === "POST",
  );
  await member.getByRole("button", { name: "发送消息" }).click();
  assert.equal((await saved).status(), 201);
  await member.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const base = "projects/" + new URL(member.url()).hash.slice(1);
  const [attachment] = await (
    await member.request.get("/api/" + base + "/sources")
  ).json();
  assert.equal(attachment.extraction, "ready");
  const getProject = async () =>
    (await member.request.get("/api/" + base)).json();
  assert.equal((await getProject()).plan.sources[0].id, attachment.id);
  const options = member.getByRole("region", { name: "费用与协作" });
  assert.equal(await options.locator(":scope > details[open]").count(), 0);
  assert.ok(
    await options.evaluate((el) =>
      Boolean(
        document.querySelector(".thread h2").compareDocumentPosition(el) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ),
    ),
  );
  await member.getByText(/项目资料 · 1/).click();
  await member.getByText("在资料中查找依据").click();
  await member.getByLabel("问题或关键词").fill("访谈回执");
  await member.getByRole("button", { name: "查找原文" }).click();
  const hit = member
    .locator(".source-search article")
    .filter({ hasText: "source72" })
    .first();
  await hit.locator("small").filter({ hasText: "段落 2" }).waitFor();
  assert.deepEqual(
    await (
      await member.request.get(await hit.getByRole("link").getAttribute("href"))
    ).body(),
    await readFile(join(documents, "访谈记录.docx")),
  );
  await hit.screenshot({ path: join(evidence, "office-citation.png") });
  await member.getByLabel(/授权本次 1/).check();
  await member.getByRole("button", { name: "确认并执行" }).click();
  await member.getByRole("button", { name: "审核此版本" }).click();
  await member.getByRole("button", { name: "确认并固定以上版本" }).click();
  await member.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
  assert.equal((await getProject()).artifacts[0].body, "RECEIPT source72");
  await member.getByText("与方案原文对照", { exact: true }).click();
  const compare = member.getByRole("region", { name: "原句对照" });
  await compare
    .locator("article")
    .filter({ hasText: "source72" })
    .getByText(/段落 2/)
    .waitFor();
  await compare.getByText("访谈回执 source72",{exact:true}).waitFor();
  assert.equal(
    await compare.getByRole("button", { name: "起草核对意见" }).count(),
    0,
  );
  assert.deepEqual(
    await (
      await member.request.get(
        await compare
          .getByRole("link", { name: "打开此版资料" })
          .getAttribute("href"),
      )
    ).body(),
    await readFile(join(documents, "访谈记录.docx")),
  );
  await compare.screenshot({
    path: join(evidence, "office-source-comparison.png"),
  });
  const formats = [];
  for (const [dir, file, query, label] of [
    [samples, "sales.xlsx", "12.50", "销售!D2"],
    [documents, "访谈摘要.pptx", "下一步由采购组确认交期", "幻灯片 2"],
    [samples, "training-doc.pdf", "核对设备编号", "PDF 第 1 页"],
  ]) {
    const original = await readFile(join(dir, file));
    const item = await post(
      member,
      base + "/sources?name=" + encodeURIComponent(file),
      original,
    );
    assert.equal(item.extraction, "ready", file);
    const response = await member.request.get("/api/" + base + "/retrieval", {
      params: { q: query },
    });
    const hits = (await response.json()).result.hits;
    assert.ok(
      hits.some((h) => h.id === item.id && h.location === label && h.exact),
      file,
    );
    formats.push({ file, label });
  }
  for (const [file, expected] of [
    ["scan.pdf", "empty"],
    ["large.docx", "limited"],
  ]) {
    const item = await post(
      member,
      base + "/sources?name=" + file,
      await readFile(join(documents, file)),
    );
    assert.equal(item.extraction, expected);
    assert.equal(item.readable, false);
  }
  const damaged = Buffer.from("synthetic invalid Office file");
  const invalid = await post(
    member,
    base + "/sources?name=broken.docx",
    damaged,
  );
  assert.equal(invalid.extraction, "invalid");
  assert.equal(invalid.readable, false);
  const original = await member.request.get(
    "/api/" + base + "/sources/" + invalid.id + "/content?version=1",
  );
  assert.deepEqual(await original.body(), damaged);
  const crossProject = await admin.request.post(
    "/api/" + base + "/sources/" + invalid.id + "/extract",
    { data: { version: 1 } },
  );
  assert.equal(crossProject.status(), 404);
  const identity = await (await member.request.get("/api/me")).json();
  const teams = await (await member.request.get("/api/teams")).json();
  const originalTeam = teams.find((t) => t.default).id;
  const second = await post(admin, "admin/teams", { name: "资料核对范围" });
  async function membership(team, joined) {
    const response = await admin.request.put(
      "/api/admin/teams/" + team + "/members/" + identity.id,
      { data: { joined, reason: "验收任务归属" } },
    );
    assert.equal(response.status(), 204);
  }
  await membership(second.id, true);
  await membership(originalTeam, false);
  const implicit = await post(member, "projects", {
    goal: "单一范围内直接开始工作",
  });
  assert.equal(
    (await (await member.request.get("/api/projects/" + implicit.id)).json())
      .team,
    second.id,
  );
  assert.equal(
    (
      await member.request.post("/api/projects", {
        data: { goal: "检查失效归属", team: originalTeam },
      })
    ).status(),
    403,
  );
  const third = await post(admin, "admin/teams", { name: "资料复核范围" });
  await membership(third.id, true);
  assert.equal(
    (
      await member.request.post("/api/projects", {
        data: { goal: "需要明确归属" },
      })
    ).status(),
    422,
  );
  await member.goto("/");
  await member
    .getByRole("textbox", { name: "消息", exact: true })
    .fill("在选定范围内核对资料");
  await member.getByRole("button", { name: "发送消息" }).click();
  await member.getByLabel("项目所属团队").selectOption(third.id);
  await member.getByRole("button", { name: "发送消息" }).click();
  await member.waitForURL(/#.+/);
  const chosen = await (
    await member.request.get(
      "/api/projects/" + new URL(member.url()).hash.slice(1),
    )
  ).json();
  assert.equal(chosen.team, third.id);
  await member.getByRole("button", {name:"新对话", exact:true}).click();
  await member.getByRole("button", {name:"项目选项 · 资料复核范围"}).waitFor();
  assert.equal(await member.getByLabel("项目所属团队").count(), 0);
  await member.getByRole("textbox", {name:"消息", exact:true}).fill("继续在刚选的范围内工作");
  await member.getByRole("button", {name:"发送消息"}).click();
  await member.waitForURL(/#.+/);
  const repeatedID = new URL(member.url()).hash.slice(1);
  assert.notEqual(repeatedID, chosen.id);
  assert.equal((await (await member.request.get("/api/projects/"+repeatedID)).json()).team, third.id);
  await writeFile(
    join(evidence, "office-intake.json"),
    JSON.stringify(
      {
        input: "synthetic",
        memberSelectedTeam: false,
        soleAlternateScopeSelected: true,
        ambiguousScopeRequiresChoice: true,
        repeatedBriefUsesDisplayedScope: true,
        revokedExplicitScopeStatus: 403,
        wordLocation: "段落 2",
        formats,
        pinnedVersion: 1,
        modelResult: "RECEIPT source72",
        approvedDelivery: true,
        invalidRetained: true,
        scannedPdf: "empty",
        oversizedBody: "limited",
        unauthorizedRetry: 404,
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

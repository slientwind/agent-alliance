import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const baseURL = process.env.SMOKE_ORIGIN,
    password = process.env.BROWSER_PASSWORD;
  const owner = await browser.newContext({ baseURL }),
    guest = await browser.newContext({ baseURL });
  async function call(ctx, path, method = "GET", data, expected = 200, key) {
    const res = await ctx.request.fetch("/api/" + path, {
      method,
      data,
      headers: key ? { "Idempotency-Key": key } : {},
    });
    assert.equal(
      res.status(),
      expected,
      method + " " + path + " " + (res.ok() ? "" : await res.text()),
    );
    return res.headers()["content-type"]?.includes("json")
      ? res.json()
      : undefined;
  }
  await call(owner, "login", "POST", {
    email: "review@sample.invalid",
    password,
  });
  const token = (await call(owner, "admin/invitations", "POST", {}, 201)).token;
  const member = await call(
    guest,
    "register",
    "POST",
    { email: "routing@sample.invalid", name: "资料协作者", password, token },
    201,
  );
  const project = (
    await call(
      owner,
      "projects",
      "POST",
      { goal: "检查培训讲义、题目与独立摘要" },
      201,
    )
  ).id;
  const base = "projects/" + project,
    assignments = base + "/feedback-assignments";
  const detail = () => call(owner, base);
  await call(
    owner,
    base + "/plan",
    "POST",
    {
      expected: (await detail()).revision,
      brief: "检查培训资料",
      agents: Array(3).fill(process.env.FEEDBACK_AGENT),
      dependencies: [[], [1], []],
    },
    204,
  );
  await call(
    owner,
    base + "/runs",
    "POST",
    { expected: (await detail()).revision, authorizeCalls: 3 },
    202,
    "routing-demo",
  );
  let state;
  for (let i = 0; i < 150; i++) {
    state = await detail();
    if (state.stage === "review") break;
    await new Promise((done) => setTimeout(done, 200));
  }
  assert.equal(state.stage, "review");
  const [first, second, third] = state.artifacts,
    path = base + "/artifacts/";
  await call(
    owner,
    path + third.id + "/revise",
    "POST",
    { version: 1, body: "唯一；部门口径", reason: "合成样例" },
    204,
  );
  const original =
    "步骤 1 简化培训讲义并保留引用；删去“唯一；部门口径”；Writer 用语需要澄清";
  await call(owner, base + "/messages", "POST", { body: original }, 204);
  const page = await owner.newPage();
  await page.goto("/#" + project);
  const panel = page
    .locator("details.card")
    .filter({ has: page.locator(":scope > summary", { hasText: "项目反馈" }) });
  await panel.locator("summary").first().click();
  await panel.getByLabel("引用对话里的意见").selectOption({ label: original });
  await panel.getByRole("button", { name: "查看分配建议" }).click();
  const row = (n) =>
    panel.getByRole("group", { name: "意见 " + n, exact: true });
  assert(
    await row(1)
      .getByRole("checkbox", { name: /步骤 1/ })
      .isChecked(),
  );
  assert(await row(3).getByText("这条意见针对哪些成果？").isVisible());
  assert(
    await row(2)
      .getByRole("checkbox", { name: /步骤 3/ })
      .isChecked(),
  );
  await row(1)
    .getByText(/修改后需关联复核 · 步骤 2/)
    .waitFor();
  assert(
    !(await row(1)
      .getByText(/关联复核.*步骤 3/)
      .count()),
  );
  await row(1).getByLabel("处理方式").selectOption("change");
  await row(3).getByLabel("暂缓此项").check();
  await row(1).scrollIntoViewIfNeeded();
  await row(1).screenshot({
    path: process.env.VISUAL_EVIDENCE + "/project-feedback.png",
  });
  const batch = {
    original: "分配验证",
    items: [
      {
        body: "保留培训标题",
        kind: "suggestion",
        targets: [
          { artifact: first.id, version: 1 },
          { artifact: third.id, version: 1 },
        ],
      },
    ],
  };
  await call(owner, assignments, "POST", batch, 409, "stale-group");
  assert((await detail()).artifacts.every((a) => !a.feedback.length));
  await call(guest, assignments, "POST", batch, 404, "no-access");
  await call(
    owner,
    base + "/participants",
    "POST",
    {
      expected: (await detail()).revision,
      member: member.id,
      review: false,
      reason: "参与建议",
    },
    204,
  );
  await call(
    guest,
    assignments,
    "POST",
    {
      ...batch,
      items: [
        {
          ...batch.items[0],
          kind: "change",
          targets: [{ artifact: first.id, version: 1 }],
        },
      ],
    },
    403,
    "no-review",
  );
  assert((await detail()).artifacts.every((a) => !a.feedback.length));
  // A concurrent human edit must invalidate the pinned draft as a unit.
  await call(
    owner,
    path + third.id + "/revise",
    "POST",
    { version: 2, body: "唯一；部门口径，已更新", reason: "并发修订" },
    204,
  );
  await panel.getByRole("button", { name: "确认分配" }).click();
  await page.getByRole("alert").filter({ hasText: "目标成果已更新" }).waitFor();
  assert((await detail()).artifacts.every((a) => !a.feedback.length));
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await row(2).getByText("目标已更新 · 查看 v3").click();
  await row(2).getByRole("button", { name: "改为针对 v3" }).click();
  const pending = page.waitForRequest(
    (r) => r.url().endsWith("/feedback-assignments") && r.method() === "POST",
  );
  await panel.getByRole("button", { name: "确认分配" }).click();
  const sent = await pending;
  await panel
    .getByRole("status")
    .filter({ hasText: "已提交 2 个成果反馈项" })
    .waitFor();
  assert.equal(
    await panel.getByLabel("整组意见").inputValue(),
    "Writer 用语需要澄清",
  );
  const applied = await detail(),
    targets = applied.artifacts.map((a) => a.feedback);
  assert.equal(targets[0][0].state, "required");
  assert.equal(targets[2][0].state, "suggested");
  assert.equal(targets[2][0].base, 3);
  assert.equal(targets[1].length, 0);
  assert.equal(targets[0][0].submission, targets[2][0].submission);
  assert.equal(applied.feedbackOriginals[targets[0][0].submission], original);
  assert.equal(applied.costSummary.calls, state.costSummary.calls);
  const receipt = await call(
    owner,
    assignments,
    "POST",
    sent.postDataJSON(),
    200,
    sent.headers()["idempotency-key"],
  );
  assert.equal(receipt.id, targets[0][0].submission);
  await call(
    owner,
    assignments,
    "POST",
    { ...sent.postDataJSON(), original: "changed" },
    409,
    sent.headers()["idempotency-key"],
  );
  const next = row(1);
  await next.getByRole("checkbox", { name: /步骤 2/ }).check();
  await next.getByRole("checkbox", { name: /步骤 3/ }).check();
  await panel.getByRole("button", { name: "确认分配" }).click();
  await panel
    .getByRole("status")
    .filter({ hasText: "已提交 2 个成果反馈项。" })
    .waitFor();
  const final = await detail();
  assert.deepEqual(
    final.artifacts.map((a) => a.feedback.length),
    [1, 1, 2],
  );
  assert.equal(final.costSummary.calls, state.costSummary.calls);
  const approved = await owner.request.post(
    "/api/" + path + first.id + "/review",
    { data: { version: 1 } },
  );
  assert.equal(approved.status(), 409);
  for (const artifact of [second, third]) {
    const item = final.artifacts.find((a) => a.id === artifact.id);
    for (const f of item.feedback) {
      await call(owner, path + item.id + "/feedback/" + f.id, "POST", {
        version: item.version,
        expected: f.revision,
        action: "decline",
        reason: "现版可沿用，保留本次判断",
      });
    }
    await call(
      owner,
      path + item.id + "/review",
      "POST",
      { version: item.version },
      204,
    );
  }
  const feedbackPath = path + first.id + "/feedback/" + targets[0][0].id;
  await call(
    owner,
    feedbackPath + "/change-runs",
    "POST",
    { version: 1, expected: 1, authorizeOneCall: true },
    202,
    "only-first-step",
  );
  let revised;
  for (let i = 0; i < 150; i++) {
    revised = await detail();
    if (revised.artifacts[0].feedback[0].state === "verification") break;
    await new Promise((r) => setTimeout(r, 200));
  }
  assert.deepEqual(
    revised.artifacts.map((a) => a.version),
    [2, 2, 3],
  );
  assert.deepEqual(
    revised.artifacts.map((a) => a.approved),
    [false, false, true],
  );
  assert.deepEqual(
    revised.costDetails
      .filter((c) => c.scope === "revision")
      .map((c) => c.step),
    [1],
  );
  await call(
    owner,
    feedbackPath,
    "POST",
    { version: 2, expected: 2, action: "resolve", reason: "核对讲义改稿" },
    200,
  );
  await call(owner, path + first.id + "/review", "POST", { version: 2 }, 204);
  await call(
    owner,
    base + "/deliver",
    "POST",
    { expected: (await detail()).revision },
    409,
  );
  await call(owner, path + second.id + "/review", "POST", { version: 2 }, 204);
  const delivery = await call(
    owner,
    base + "/deliver",
    "POST",
    { expected: (await detail()).revision },
    201,
  );
  const manifest = await call(owner, base + "/delivery");
  assert.equal(manifest.id, delivery.id);
  assert.deepEqual(
    manifest.items.map((item) => item.version),
    [2, 2, 3],
  );
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/project-feedback.json",
    JSON.stringify(
      {
        project,
        submission: receipt.id,
        delivery: delivery.id,
        deliveredVersions: manifest.items.map((item) => item.version),
        versions: final.artifacts.map((a) => a.version),
        feedbackCounts: final.artifacts.map((a) => a.feedback.length),
        checks: [
          "step and unique quotation routing",
          "ambiguous name clarified",
          "dependent review preview",
          "deferred feedback retained",
          "stale version atomic rollback",
          "nonparticipant denied",
          "suggestion-only member cannot require changes",
          "explicit version rebase",
          "same submission replay and mismatch",
          "one opinion assigned to two artifacts",
          "approval blocked",
          "no extra invocation on assignment",
          "one authorized revision routed to original step",
          "dependent review invalidated and independent approval preserved",
          "reviewed versions frozen in delivery",
        ],
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

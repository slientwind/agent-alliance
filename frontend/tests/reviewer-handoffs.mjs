import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import { chromium } from "playwright";

const browser = await chromium.launch(),
  origin = process.env.SMOKE_ORIGIN;
try {
  const admin = await browser.newContext({ baseURL: origin });
  const call = async (ctx, path, method = "GET", data, expected = 200) => {
    const r = await ctx.request.fetch("/api/" + path, { method, data });
    assert.equal(
      r.status(),
      expected,
      method +
        " " +
        path +
        " returned " +
        r.status() +
        " " +
        (r.ok() ? "" : await r.text()),
    );
    return r.headers()["content-type"]?.includes("json")
      ? await r.json()
      : undefined;
  };
  const post = (ctx, path, data, status) =>
    call(ctx, path, "POST", data, status);
  const password = process.env.BROWSER_PASSWORD;
  await post(admin, "login", { email: "review@sample.invalid", password }, 200);
  const adminId = (await call(admin, "me")).id;
  async function member(tag) {
    const ctx = await browser.newContext({
      baseURL: origin,
      viewport: { width: 390, height: 844 },
    });
    const invite = await post(admin, "admin/invitations", {}, 201);
    const email = tag.toLowerCase().replaceAll(" ", "-") + "@sample.invalid";
    await post(
      ctx,
      "register",
      { email, password, name: tag, token: invite.token },
      201,
    );
    return { ctx, email, id: (await call(ctx, "me")).id };
  }
  const nominee = await member("Handoff reviewer"),
    outsider = await member("Handoff outsider");
  const service = process.env.HANDOFF_SERVICE;
  const app = await post(
    admin,
    "services/" + service + "/applications",
    { name: "Reviewer lifecycle report", days: 1 },
    201,
  );
  const delegated = await post(
    admin,
    "services/" + service + "/delegate",
    { app: app.id, hours: 1 },
    201,
  );
  const headers = {
    Authorization: "Bearer " + app.token,
    "X-Alliance-Delegation": delegated.token,
  };
  const runFor = new Map();
  const progress = async (base, suffix = "") => {
    const r = await admin.request.get(
      "/api/v1/runs/" + runFor.get(base) + suffix,
      { headers },
    );
    assert.equal(r.status(), 200);
    return r.json();
  };
  async function ready(goal) {
    const started = await admin.request.post(
      "/api/v1/services/" + service + "/runs",
      {
        headers: { ...headers, "Idempotency-Key": crypto.randomUUID() },
        data: { input: goal, version: 1, authorizeCalls: 1 },
      },
    );
    assert.equal(started.status(), 202);
    const run = await started.json(),
      base = "projects/" + run.project;
    runFor.set(base, run.id);
    for (let i = 0; i < 100; i++) {
      if ((await call(admin, base)).stage === "review") return base;
      await new Promise((r) => setTimeout(r, 200));
    }
    throw Error("Service did not reach review");
  }
  const base = await ready(
    "Verify final responsibility and version-specific takeover",
  );
  const rev = async (ctx = admin) => (await call(ctx, base)).revision;
  const proposal = async () =>
    post(
      admin,
      base + "/participants",
      {
        expected: await rev(),
        member: nominee.id,
        final: true,
        reason: "交接负责验收的人员",
      },
      202,
    );
  const decision = async (ctx, id, action, status, expected) =>
    post(
      ctx,
      base + "/handoffs/" + id,
      {
        expected: expected ?? (await rev(ctx)),
        action,
        reason: "核对交接范围",
      },
      status,
    );
  await post(
    admin,
    base + "/participants",
    { email: nominee.email, expected: await rev(), reason: "项目参与" },
    204,
  );
  await post(
    outsider.ctx,
    base + "/takeover",
    {
      expected: await rev(),
      member: outsider.id,
      reason: "无授权",
      endCondition: "完成",
    },
    403,
  );
  await post(
    admin,
    base + "/takeover",
    {
      expected: await rev(),
      member: nominee.id,
      reason: "正常履职",
      endCondition: "完成",
    },
    403,
  );
  const privateProject = await post(
    nominee.ctx,
    "projects",
    { goal: "独立私有的交接样例" },
    201,
  );
  const waiting = await ready(
    "When a nomination is pending the current reviewer can still confirm",
  );
  const getWaiting = () => call(admin, waiting);
  await post(
    admin,
    waiting + "/participants",
    {
      member: nominee.id,
      expected: (await getWaiting()).revision,
      reason: "邀请查看",
    },
    204,
  );
  const open = await post(
    admin,
    waiting + "/participants",
    {
      member: nominee.id,
      final: true,
      expected: (await getWaiting()).revision,
      reason: "待答复",
    },
    202,
  );
  await post(
    admin,
    waiting + "/artifacts/" + (await getWaiting()).artifacts[0].id + "/review",
    { version: 1 },
    204,
  );
  await post(
    admin,
    waiting + "/deliver",
    { expected: (await getWaiting()).revision },
    201,
  );
  assert.equal((await getWaiting()).handoffs[0].state, "closed");
  await post(
    nominee.ctx,
    waiting + "/handoffs/" + open.id,
    { expected: (await getWaiting()).revision, action: "accept" },
    409,
  );
  const first = await proposal();
  assert.equal((await call(admin, base)).reviewer, adminId);
  await decision(admin, first.id, "accept", 403);
  await decision(nominee.ctx, first.id, "decline", 204);
  const second = await proposal();
  await decision(admin, second.id, "withdraw", 204);
  const superseded = await proposal(),
    valid = await proposal();
  await decision(nominee.ctx, superseded.id, "accept", 409);
  const state = await call(admin, base),
    art = state.artifacts[0],
    target = base + "/artifacts/" + art.id;
  await post(admin, target + "/review", { version: 1 }, 204);
  await decision(nominee.ctx, valid.id, "accept", 409, state.revision);
  assert(
    !(await call(outsider.ctx, "projects/reviewer-handoffs")).some((x) =>
      base.endsWith(x.project),
    ),
  );
  const page = await nominee.ctx.newPage();
  await page.goto("/");
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByText("协作与授权", { exact: true }).click();
  await page.getByText("确认权交接待办", { exact: true }).click();
  await page.getByRole("button", { name: /待我接受/ }).click();
  await page.getByRole("button", { name: "接受最终确认职责" }).click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).waitFor();
  assert.equal((await call(admin, base)).reviewer, nominee.id);
  await post(admin, base + "/deliver", { expected: await rev() }, 403);
  // Disabling the current reviewer preserves the original review and project membership. Reset only this test account.
  await call(
    admin,
    "admin/members/" + nominee.id,
    "PATCH",
    {
      role: "member",
      enabled: false,
      password,
      reason: "模拟确认人账号不可用",
    },
    204,
  );
  assert(
    (await call(admin, "projects/reviewer-handoffs")).some(
      (x) => x.project === base.split("/")[1] && x.unavailable,
    ),
  );
  assert(
    !(await call(admin, "projects/reviewer-handoffs")).some(
      (x) => x.project === privateProject.id,
    ),
  );
  await call(admin, "projects/" + privateProject.id, "GET", undefined, 404);
  await post(
    admin,
    "projects/" + privateProject.id + "/takeover",
    {
      expected: 1,
      member: adminId,
      reason: "无本项目权限",
      endCondition: "完成",
    },
    404,
  );
  const browserPage = await admin.newPage();
  await browserPage.goto("/#" + base.split("/")[1]);
  await browserPage.getByText(/^项目协作 ·/).click();
  await browserPage.getByText("异常接管最终确认职责", { exact: true }).click();
  await browserPage.getByLabel("接替人", { exact: true }).selectOption(adminId);
  await browserPage
    .getByLabel("异常原因与影响")
    .fill("原确认人账号停用，验收中断");
  await browserPage
    .getByLabel("接管结束条件")
    .fill("完成本项目当前成果验收并交付");
  await browserPage.getByRole("button", { name: "提交接任提名" }).click();
  await browserPage.getByRole("button", { name: "接受最终确认职责" }).click();
  await browserPage.getByRole("button", { name: "接管人审核此版本" }).waitFor();
  let after = await call(admin, base);
  assert(
    after.takeoverAssignment &&
      after.artifacts[0].approved &&
      after.artifacts[0].needsTakeoverReview,
  );
  assert.equal(after.handoffs.filter((x) => x.state === "accepted").length, 2);
  const external = await progress(base);
  assert.equal(external.nextAction, "review_artifacts");
  assert.equal(external.steps[0].needsTakeoverReview, true);
  await post(admin, base + "/deliver", { expected: after.revision }, 409);
  await post(admin, target + "/review", { version: 1 }, 204);
  assert((await call(admin, base)).artifacts[0].needsTakeoverReview);
  assert(
    (await call(admin, "projects/review-inbox"))
      .filter((x) => x.project === after.id)
      .some((x) => x.kind === "review"),
  );
  await browserPage.reload();
  await browserPage.getByRole("button", { name: "接管人审核此版本" }).click();
  await browserPage
    .getByRole("button", { name: "确认并固定以上版本" })
    .waitFor();
  await post(
    admin,
    target + "/revise",
    { version: 1, body: "本版本需要再次核对", reason: "人工更正" },
    204,
  );
  await post(
    admin,
    target + "/review",
    { version: 1, assignment: after.takeoverAssignment },
    409,
  );
  await post(admin, base + "/deliver", { expected: await rev() }, 409);
  await browserPage.reload();
  await browserPage.getByRole("button", { name: "接管人审核此版本" }).click();
  await browserPage.getByRole("button", { name: "确认并固定以上版本" }).click();
  await browserPage.getByRole("link", { name: /下载交付包/ }).waitFor();
  const receipt = await call(admin, base + "/delivery");
  assert.equal(receipt.confirmedBy, adminId);
  assert.equal(receipt.items[0].version, 2);
  const { events } = await progress(base, "/events");
  assert(events.some((e) => e.state === "reviewer_changed"));
  assert(
    events.some((e) => e.state === "takeover_reviewed" && e.version === 2),
  );
  const zip = await admin.request.get(
    "/api/v1/runs/" + runFor.get(base) + "/delivery/package",
    { headers },
  );
  assert.equal(zip.status(), 200);
  const manifest = JSON.parse(
    execFileSync(
      "python3",
      [
        "-c",
        "import io, sys, zipfile; sys.stdout.buffer.write(zipfile.ZipFile(io.BytesIO(sys.stdin.buffer.read())).read('manifest.json'))",
      ],
      { input: await zip.body() },
    ),
  );
  assert.equal(manifest.reviewerAssignment, after.takeoverAssignment);
  assert.equal(manifest.items[0].takeoverReviewer, adminId);
  assert(manifest.items[0].takeoverReviewedAt);
  await browserPage.getByText(/^项目协作 ·/).click();
  await browserPage.getByText(/^确认职责变更记录/).click();
  await browserPage
    .locator("details.card")
    .filter({ hasText: "确认职责变更记录" })
    .first()
    .screenshot({
      path: process.env.VISUAL_EVIDENCE + "/reviewer-history.png",
    });
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/reviewer-handoffs.json",
    JSON.stringify(
      {
        project: after.id,
        ordinaryHandoff: valid.id,
        emergencyAssignment: after.takeoverAssignment,
        acceptedBy: receipt.confirmedBy,
        packageReview: manifest.items[0].takeoverReviewedAt,
        deliveredVersion: 2,
        checks: [
          "decline and withdrawal",
          "replacement and stale request",
          "nominee accepts in browser",
          "former final reviewer denied",
          "admin and project access checked",
          "no takeover while reviewer available",
          "business role accepted separately",
          "historical approval preserved and explicit takeover review required",
          "source revision invalidates takeover check",
          "final manifest fixed",
        ],
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

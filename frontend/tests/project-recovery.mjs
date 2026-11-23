import { chromium } from "playwright";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { writeFile, readFile } from "node:fs/promises";

const browser = await chromium.launch(),
  baseURL = process.env.SMOKE_ORIGIN;
try {
  const admin = await browser.newContext({ baseURL }),
    password = process.env.BROWSER_PASSWORD;
  const call = async (ctx, path, method = "GET", data, expected = 200) => {
    const r = await ctx.request.fetch("/api/" + path, { method, data });
    assert.equal(
      r.status(),
      expected,
      method + " " + path + " " + (r.ok() ? "" : await r.text()),
    );
    return r.headers()["content-type"]?.includes("json") ? r.json() : undefined;
  };
  await call(admin, "login", "POST", {
    email: "review@sample.invalid",
    password,
  });
  async function member(name) {
    const ctx = await browser.newContext({
      baseURL,
      viewport: { width: 390, height: 844 },
    });
    const invitation = await call(admin, "admin/invitations", "POST", {}, 201),
      email = name + "@sample.invalid";
    const { id } = await call(
      ctx,
      "register",
      "POST",
      { email, name, password, token: invitation.token },
      201,
    );
    return { ctx, id, email };
  }
  const owner = await member("former-owner"),
    next = await member("successor"),
    external = await member("separate-admin");
  const team = (await call(admin, "teams")).find((t) => t.default).id;
  await call(
    admin,
    "admin/members/" + external.id,
    "PATCH",
    { role: "admin", enabled: true, password, reason: "独立范围管理员" },
    204,
  );
  await call(
    admin,
    "admin/teams/" + team + "/members/" + external.id,
    "PUT",
    { joined: false, reason: "隔离管理范围" },
    204,
  );
  const marker = "ORPHAN_PRIVATE_914",
    project = (await call(owner.ctx, "projects", "POST", { goal: marker }, 201))
      .id,
    base = "projects/" + project;
  const detail = (ctx) => call(ctx, base);
  const src = await owner.ctx.request.post(
    "/api/" + base + "/sources?name=private.txt",
    { data: Buffer.from("PROJECT_SOURCE_914") },
  );
  assert.equal(src.status(), 201);
  const source = (await src.json()).id;
  const sourcePath = base + "/sources/" + source + "/content?version=1";
  await call(
    owner.ctx,
    base + "/plan",
    "POST",
    {
      brief: "Make an internal review receipt",
      agents: [process.env.RECOVERY_AGENT],
      expected: (await detail(owner.ctx)).revision,
    },
    204,
  );
  const start = await owner.ctx.request.post("/api/" + base + "/runs", {
    headers: { "Idempotency-Key": "orphan-recovery" },
    data: { expected: (await detail(owner.ctx)).revision, authorizeCalls: 1 },
  });
  assert.equal(start.status(), 202);
  for (let i = 0; i < 100; i++) {
    if ((await detail(owner.ctx)).stage === "review") break;
    await new Promise((r) => setTimeout(r, 200));
  }
  const artifact = (await detail(owner.ctx)).artifacts[0].id;
  await call(
    owner.ctx,
    base + "/artifacts/" + artifact + "/review",
    "POST",
    { version: 1 },
    204,
  );
  const list = (ctx) => call(ctx, "project-recoveries");
  assert(!(await list(admin)).some((x) => x.project === project));
  await call(
    admin,
    "admin/members/" + owner.id,
    "PATCH",
    {
      role: "member",
      employment: "depart",
      enabled: false,
      reason: "原团队成员离职",
      password,
    },
    204,
  );
  const row = async (ctx) =>
    (await list(ctx)).find((x) => x.project === project);
  assert.equal((await row(admin)).state, "available");
  assert(!(await row(next.ctx)));
  assert(!(await row(external.ctx)));
  const forbidden = async (ctx, path) => {
    const r = await ctx.request.get("/api/" + path);
    assert([403, 404].includes(r.status()), path + " " + r.status());
  };
  await forbidden(admin, base);
  await forbidden(next.ctx, sourcePath);
  assert(!JSON.stringify(await list(admin)).includes(marker));
  const root = "project-recoveries/" + project;
  const offer = async () => {
    const x = await row(admin);
    return (
      await call(
        admin,
        root,
        "POST",
        {
          email: next.email,
          reason: "完成已生成成果的审核",
          expected: x.revision,
          hours: 2,
          grantAccess: true,
        },
        201,
      )
    ).id;
  };
  const act = async (ctx, id, action, status = 204) =>
    call(
      ctx,
      root + "/" + id,
      "POST",
      { action, reason: "核对临时职责", expected: (await row(admin)).revision },
      status,
    );
  const one = await offer();
  await forbidden(next.ctx, base);
  await act(admin, one, "accept", 403);
  await act(next.ctx, one, "decline");
  const two = await offer();
  await act(next.ctx, two, "accept");
  let p = await detail(next.ctx);
  assert(
    p.recoveryUntil &&
      p.takeoverAssignment === two &&
      p.canReview &&
      !p.canViewCosts,
  );
  assert(p.artifacts[0].approved && p.artifacts[0].needsTakeoverReview);
  assert.equal(
    (await next.ctx.request.get("/api/" + sourcePath)).status(),
    200,
  );
  await call(
    next.ctx,
    base + "/deliver",
    "POST",
    { expected: p.revision },
    409,
  );
  await act(admin, two, "end");
  await forbidden(next.ctx, base);
  await forbidden(next.ctx, sourcePath);
  const three = await offer();
  await act(next.ctx, three, "accept");
  await call(
    admin,
    "admin/teams/" + team + "/members/" + next.id,
    "PUT",
    { joined: false, reason: "撤销接任人团队授权" },
    204,
  );
  await call(
    admin,
    "admin/teams/" + team + "/members/" + next.id,
    "PUT",
    { joined: true, reason: "重新入组" },
    204,
  );
  await forbidden(next.ctx, base);
  assert(!(await row(next.ctx)));
  const four = await offer();
  await act(next.ctx, four, "accept");
  assert(
    /^[a-f0-9-]{36}$/.test(four) &&
      /^smoke_[a-f0-9]+$/.test(process.env.RECOVERY_DB),
  );
  execFileSync("docker", [
    "exec",
    "alliance-workspace-database-1",
    "psql",
    "-U",
    "alliance",
    "-d",
    process.env.RECOVERY_DB,
    "-c",
    "update reviewer_handoffs set recovery_until=clock_timestamp()-interval '1 second' where id='" +
      four +
      "'",
  ]);
  await forbidden(next.ctx, base);
  await forbidden(next.ctx, sourcePath);
  assert(
    !(await call(next.ctx, "projects/review-inbox")).some(
      (x) => x.project === project,
    ),
  );
  const ap = await admin.newPage();
  await ap.goto("/");
  await ap.getByRole("button", { name: "我的工作区" }).click();
  await ap.getByText("协作与授权", { exact: true }).click();
  await ap.getByText("无人负责项目恢复", { exact: true }).click();
  const form = ap.locator("section.card").filter({ hasText: project });
  await form.getByLabel("接任人团队邮箱").fill(next.email);
  await form.getByLabel("恢复原因及影响").fill("逐版本复核并下载最终成果");
  await form.getByRole("checkbox").check();
  await form.getByRole("button", { name: "提交恢复提名" }).click();
  await form
    .getByText(/待接受/)
    .first()
    .waitFor();
  const handoff = (await row(next.ctx)).offer;
  const pg = await next.ctx.newPage();
  await pg.goto("/");
  await pg.getByRole("button", { name: "我的工作区" }).click();
  await pg.getByText("协作与授权", { exact: true }).click();
  await pg.getByText("项目临时接管", { exact: true }).click();
  await pg
    .getByRole("button", { name: "接受临时授权与最终确认职责" })
    .waitFor();
  assert(!(await pg.locator("body").innerText()).includes(marker));
  await pg.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/orphan-offer.png",
  });
  await pg.getByRole("button", { name: "接受临时授权与最终确认职责" }).click();
  await pg.getByRole("heading", { name: marker }).waitFor();
  await pg.getByRole("button", { name: "接管人审核此版本" }).click();
  const dl = pg.waitForEvent("download");
  await pg.getByRole("button", { name: "确认交付并下载成果包" }).click();
  const archive = await dl;
  const bytes = await readFile(await archive.path());
  const manifest = JSON.parse(
    execFileSync(
      "python3",
      [
        "-c",
        "import io,json,sys,zipfile;sys.stdout.buffer.write(zipfile.ZipFile(io.BytesIO(sys.stdin.buffer.read())).read('manifest.json'))",
      ],
      { input: bytes },
    ),
  );
  assert.equal(manifest.confirmedBy, next.id);
  assert.equal(manifest.items[0].reviewer, owner.id);
  assert.equal(manifest.items[0].takeoverReviewer, next.id);
  assert.equal(manifest.reviewerAssignment, handoff);
  await pg.getByText("已确认交付", { exact: true }).waitFor();
  await forbidden(next.ctx, base + "/delivery/package");
  await forbidden(next.ctx, base);
  assert(!(await row(admin)));
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/project-recovery.json",
    JSON.stringify(
      {
        project,
        delivered: manifest.delivery,
        originalReviewer: owner.id,
        successor: next.id,
        reviewedVersion: manifest.items[0].version,
        checks: [
          "private metadata exclusion",
          "team boundary",
          "nomination without access",
          "explicit acceptance",
          "revoke and reassign",
          "team removal and rejoin",
          "database deadline independent of worker",
          "pinned review",
          "ZIP delivered before temporary access ends",
          "post-delivery read denied",
        ],
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

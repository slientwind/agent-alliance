import { execFileSync } from "node:child_process";
import { chromium } from "playwright";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

const browser = await chromium.launch();
try {
  const baseURL = process.env.SMOKE_ORIGIN,
    password = process.env.BROWSER_PASSWORD;
  const admin = await browser.newContext({ baseURL });
  async function call(ctx, path, method = "GET", data, expected = 200, key) {
    const r = await ctx.request.fetch("/api/" + path, {
      method,
      data,
      headers: key ? { "Idempotency-Key": key } : {},
    });
    assert.equal(
      r.status(),
      expected,
      method + " " + path + " " + (r.ok() ? "" : await r.text()),
    );
    return r.headers()["content-type"]?.includes("json") ? r.json() : undefined;
  }
  await call(admin, "login", "POST", {
    email: "review@sample.invalid",
    password,
  });
  async function person(name) {
    const email = name + "@sample.invalid",
      ctx = await browser.newContext({
        baseURL,
        viewport: { width: 390, height: 844 },
      });
    const token = (await call(admin, "admin/invitations", "POST", {}, 201))
      .token;
    const { id } = await call(
      ctx,
      "register",
      "POST",
      { email, name, password, token },
      201,
    );
    return { email, ctx, id, name };
  }
  const a = await person("rule-author"),
    b = await person("rule-successor"),
    c = await person("rule-observer");
  const team = (await call(admin, "teams")).find((t) => t.default).id;
  const secondary = (
    await call(admin, "admin/teams", "POST", { name: "规则移交专组" }, 201)
  ).id;
  const teamMember = (team, user, joined) =>
    call(
      admin,
      "admin/teams/" + team + "/members/" + user.id,
      "PUT",
      { joined, reason: "合成权限走查" },
      204,
    );
  const rules = "teams/" + team + "/rules";
  const body = "培训讲义应引用现行制度（交接验收）";
  const expiration = new Date(Date.now() + 15 * 86400000).toISOString();
  const { id } = await call(
      a.ctx,
      rules,
      "POST",
      { body, expiresAt: expiration },
      201,
    ),
    url = rules + "/" + id;
  const rule = async (ctx = admin) =>
    (await call(ctx, rules)).find((row) => row.id === id);
  const get = (ctx) => call(ctx, url + "/maintenance");
  const action = async (ctx, action, extra = {}, status = 204) =>
    call(
      ctx,
      url + "/maintenance",
      "POST",
      {
        expected: (await get(ctx)).version,
        action,
        reason: "验收依据 " + action,
        ...extra,
      },
      status,
    );
  await call(
    admin,
    url + "/publish",
    "POST",
    { revision: 1, expected: (await rule()).lifecycle },
    204,
  );
  // Advance just this version's clock-dependent fixture in the disposable smoke database.
  function ageRule(sql) {
    const db = process.env.RULE_HANDOFF_DB;
    assert.match(db, /^smoke_[a-f0-9]{10}$/);
    execFileSync(
      "docker",
      [
        "exec",
        "-i",
        "alliance-workspace-database-1",
        "psql",
        "-Xq",
        "-v",
        "ON_ERROR_STOP=1",
        "-U",
        "alliance",
        "-d",
        db,
        "-v",
        "rule_key=" + id,
      ],
      { input: sql },
    );
  }
  ageRule(
    "update rule_versions set review_at=now()-interval '1 day' where rule_id=:'rule_key'::uuid and revision=1;",
  );
  const published = await rule();
  assert(published.due && !published.expired);
  assert(
    (await call(a.ctx, "rules/maintenance-inbox")).some(
      (t) => t.id === id && t.action === "review",
    ),
  );
  async function plan(title) {
    const { id } = await call(admin, "projects", "POST", { goal: title }, 201);
    const path = "projects/" + id;
    await call(
      admin,
      path + "/plan",
      "POST",
      {
        brief: "测试团队规则",
        expected: (await call(admin, path)).revision,
        agents: [process.env.RULE_HANDOFF_AGENT],
      },
      204,
    );
    return path;
  }
  const privatePath = await plan("私有演练标题 71"),
    sharedPath = await plan("供接任人核对的项目 19");
  await call(
    admin,
    sharedPath + "/participants",
    "POST",
    {
      member: b.id,
      expected: (await call(admin, sharedPath)).revision,
      review: false,
    },
    204,
  );
  await call(
    admin,
    sharedPath + "/runs",
    "POST",
    { expected: (await call(admin, sharedPath)).revision, authorizeCalls: 1 },
    202,
    "rule-owner-run",
  );
  let initial;
  for (let i = 0; i < 150; i++) {
    initial = await call(admin, sharedPath);
    if (initial.stage === "review") break;
    await new Promise((r) => setTimeout(r, 200));
  }
  assert.equal(initial.stage, "review");
  assert(
    (await call(b.ctx, url + "/references")).every(
      (r) => r.title !== "私有演练标题 71",
    ),
  );
  assert.deepEqual(
    (await call(b.ctx, url + "/references")).map((r) => r.project),
    [initial.id],
  );
  await action(admin, "nominate", { email: b.email }, 403);
  await action(a.ctx, "nominate", {
    email: b.email,
    reason: "知识责任移交依据 91",
  });
  await action(c.ctx, "accept", {}, 403);
  assert.equal((await get(c.ctx)).reason, null);
  await action(b.ctx, "cancel");
  const oldVersion = (await get(a.ctx)).version;
  await action(a.ctx, "nominate", { email: b.email });
  await call(
    b.ctx,
    url + "/maintenance",
    "POST",
    { expected: oldVersion, action: "accept", reason: "过期页面" },
    409,
  );
  await call(
    b.ctx,
    url + "/review",
    "POST",
    { expected: published.lifecycle, revision: 1, reason: "尚未接任" },
    404,
  );
  const tasks = await call(b.ctx, "rules/maintenance-inbox");
  assert(tasks.some((t) => t.id === id && t.action === "accept"));
  const page = await b.ctx.newPage();
  await page.goto("/");
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByText("协作与授权", { exact: true }).click();
  await page.getByRole("button", { name: "团队规则", exact: true }).click();
  const modal = page.getByRole("dialog");
  await modal.getByText(/我的规则维护待办/).click();
  await modal
    .getByRole("button", { name: new RegExp(body.slice(0, 5)) })
    .click();
  const card = modal
    .locator("article.card")
    .filter({ has: page.getByText(body, { exact: true }) });
  const panel = card.locator("details").filter({
    has: page.locator(":scope > summary", { hasText: "待接受维护职责" }),
  });
  await panel.locator("summary").click();
  await panel.getByLabel("交接依据").fill("接受维护，复查制度原文");
  await panel.getByRole("button", { name: "接受维护职责" }).click();
  await card.getByText(/维护人 · rule-successor/).waitFor();
  await card.getByText("项目引用与交接记录").click();
  await card.getByRole("link", { name: "供接任人核对的项目 19" }).waitFor();
  assert.equal(await card.getByText(/私有演练标题 71/).count(), 0);
  await card
    .getByRole("link", { name: "供接任人核对的项目 19" })
    .scrollIntoViewIfNeeded();
  await page.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/rule-maintenance.png",
  });
  let current = await rule();
  assert.equal(current.creator, a.name);
  assert.equal(current.owner, b.name);
  assert.equal(current.reviewAt, published.reviewAt);
  assert.equal(Date.parse(current.publishedExpiresAt), Date.parse(expiration));
  assert(
    (await call(b.ctx, "rules/maintenance-inbox")).some(
      (t) => t.id === id && t.action === "review",
    ),
  );
  assert(
    !(await call(a.ctx, "rules/maintenance-inbox")).some((t) => t.id === id),
  );
  assert.equal(current.published, 1);
  await call(
    a.ctx,
    url + "/review",
    "POST",
    { expected: current.lifecycle, revision: 1, reason: "旧维护人" },
    404,
  );
  await call(
    b.ctx,
    url + "/review",
    "POST",
    { expected: current.lifecycle, revision: 1, reason: "核对原件" },
    204,
  );
  await call(
    b.ctx,
    rules,
    "POST",
    { id, expected: 1, body: "新版制度要求，新任务采用" },
    201,
  );
  await call(
    b.ctx,
    url + "/publish",
    "POST",
    { revision: 2, expected: (await rule()).lifecycle },
    403,
  );
  await call(
    admin,
    url + "/publish",
    "POST",
    { revision: 2, expected: (await rule()).lifecycle },
    204,
  );
  const fixed = await call(admin, sharedPath);
  assert(fixed.plan.rules.some((r) => r.revision === 1 && r.body === body));
  await call(
    admin,
    sharedPath + "/artifacts/" + fixed.artifacts[0].id + "/review",
    "POST",
    { version: 1 },
    204,
  );
  const delivery = (
    await call(
      admin,
      sharedPath + "/deliver",
      "POST",
      { expected: (await call(admin, sharedPath)).revision },
      201,
    )
  ).id;
  assert.equal((await call(admin, sharedPath + "/delivery")).id, delivery);
  const newer = await plan("新版规则方案");
  assert(
    (await call(admin, newer)).plan.rules.some(
      (r) => r.revision === 2 && r.body === "新版制度要求，新任务采用",
    ),
  );
  // Membership loss invalidates invitations and prevents a silent restoration on rejoin.
  await action(b.ctx, "nominate", { email: c.email });
  await teamMember(team, b, false);
  assert(!(await get(c.ctx)).pending);
  assert(
    (await call(admin, "rules/maintenance-inbox")).some(
      (t) => t.id === id && t.action === "assign",
    ),
  );
  await teamMember(team, b, true);
  assert(!(await rule(b.ctx)).mine);
  await action(b.ctx, "nominate", { email: c.email }, 403);
  await action(admin, "nominate", { email: b.email });
  await action(b.ctx, "accept");
  assert((await rule(b.ctx)).mine);
  await call(
    admin,
    "teams/" + secondary + "/rules/" + id + "/maintenance",
    "GET",
    undefined,
    404,
  );
  assert.equal(
    (await call(c.ctx, url + "/handoff-history")).find(
      (ev) => ev.actor === a.name && ev.target === b.name,
    )?.reason,
    null,
  );
  // A rule in another team is discoverable to its selected member only.
  for (const person of [a, b]) await teamMember(secondary, person, true);
  const other = "teams/" + secondary + "/rules";
  const otherId = (
    await call(a.ctx, other, "POST", { body: "专组资料转交" }, 201)
  ).id;
  const otherURL = other + "/" + otherId + "/maintenance";
  await call(
    a.ctx,
    otherURL,
    "POST",
    { expected: 1, action: "nominate", reason: "第二团队职责", email: b.email },
    204,
  );
  assert(
    (await call(b.ctx, "rules/maintenance-inbox")).some(
      (t) => t.team === secondary && t.id === otherId,
    ),
  );
  assert(
    !(await call(c.ctx, "rules/maintenance-inbox")).some(
      (t) => t.team === secondary,
    ),
  );
  const freeze = (user, enabled) =>
    call(
      admin,
      "admin/members/" + user.id,
      "PATCH",
      { role: "member", enabled, password, reason: "账号授权演练" },
      204,
    );
  await freeze(b, false);
  await freeze(b, true);
  assert(!(await call(a.ctx, otherURL)).pending);
  // Suspending a maintainer permits a scoped admin proposal; reactivation voids that proposal.
  await freeze(b, false);
  await action(admin, "nominate", { email: c.email });
  await freeze(b, true);
  assert(!(await get(c.ctx)).pending);
  await action(c.ctx, "accept", {}, 409);
  await call(
    admin,
    "admin/members/" + a.id,
    "PATCH",
    {
      role: "member",
      enabled: false,
      employment: "depart",
      password,
      reason: "整理创建人离职后的知识职责",
    },
    204,
  );
  assert.equal((await rule()).creator, a.name);
  assert.equal((await rule()).owner, b.name);
  await call(b.ctx, "login", "POST", { email: b.email, password });
  assert.equal((await get(b.ctx)).available, true);
  assert(
    (await call(admin, "rules/maintenance-inbox")).some(
      (t) => t.team === secondary && t.id === otherId && t.action === "assign",
    ),
  );
  ageRule(
    "update rule_versions set expires_at=now()-interval '1 minute' where rule_id=:'rule_key'::uuid and revision=2;",
  );
  assert(
    (await call(b.ctx, "rules/maintenance-inbox")).some(
      (t) => t.id === id && t.action === "expired",
    ),
  );
  assert(
    (await call(admin, newer)).plan.rules.some(
      (r) => r.revision === 2 && !r.available,
    ),
  );
  assert(
    (await call(admin, sharedPath)).plan.rules.some(
      (r) => r.revision === 1 && r.available,
    ),
  );
  assert.equal((await call(admin, sharedPath + "/delivery")).id, delivery);
  const log = await call(admin, url + "/handoff-history");
  assert(
    log.some((e) => e.action === "rule.maintenance.membership") &&
      log.some((e) => e.action === "rule.maintenance.authority"),
  );
  await call(
    admin,
    url + "/publish",
    "POST",
    { revision: 2, disabled: true, expected: (await rule()).lifecycle },
    204,
  );
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/rule-maintenance.json",
    JSON.stringify(
      {
        rule: id,
        project: initial.id,
        delivery,
        creator: a.name,
        maintainer: (await rule()).owner,
        priorPin: 1,
        newPin: 2,
        actions: log.map((e) => e.action),
        checks: [
          "nominee acceptance in mobile browser",
          "review and publication isolated",
          "private project references concealed",
          "old plan and delivery retained",
          "new plans adopt new publication",
          "outsider and stale action denied",
          "team exit and rejoin",
          "multi-team inbox",
          "suspension invalidates pending authority",
          "filtered history",
          "due review transferred without extending the deadline",
          "expired new rule blocked while delivered older pin preserved",
          "creator departure preserves successor and vacates other duties",
        ],
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

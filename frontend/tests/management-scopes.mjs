import { exerciseFinancialExport } from "./financial-exports.mjs";
import { exerciseMemberProfile } from "./member-profile.mjs";
import { exerciseEnterprise } from "./enterprise-management.mjs";
import { exerciseDetails } from "./management-details.mjs";
import { exerciseExport } from "./management-exports.mjs";
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const context = () =>
    browser.newContext({ baseURL: process.env.SMOKE_ORIGIN });
  const admin = await context(),
    manager = await context(),
    author = await context();
  async function call(c, url, method = "GET", data, expected = 200, key) {
    const res = await c.request.fetch("/api/" + url, {
      method,
      data,
      headers: key ? { "Idempotency-Key": key } : {},
    });
    assert.equal(
      res.status(),
      expected,
      method + " " + url + " " + (res.ok() ? "" : await res.text()),
    );
    return res.headers()["content-type"]?.includes("json")
      ? res.json()
      : undefined;
  }
  const password = process.env.BROWSER_PASSWORD;
  await call(admin, "login", "POST", {
    email: "review@sample.invalid",
    password,
  });
  async function register(c, n, name) {
    const { token } = await call(admin, "admin/invitations", "POST", {}, 201);
    const account = await call(
      c,
      "register",
      "POST",
      { email: n + "@sample.invalid", name, password, token },
      201,
    );
    return { ...account, name };
  }
  const who = await register(manager, "scope-manager", "范围管理者");
  const writer = await register(author, "scope-author", "汇总业务发起人");
  const createOrg = async (type, name) =>
    (await call(admin, "admin/" + type, "POST", { name }, 201)).id;
  const team = await createOrg("teams", "例会协作组");
  const dept = await createOrg("departments", "培训业务部");
  const dept2 = await createOrg("departments", "售后业务部");
  const assign = (member, previous, department, expected = 204) =>
    call(
      admin,
      "admin/members/" + member + "/department",
      "PUT",
      { department, previous, reason: "合成组织调整" },
      expected,
    );
  const join = (member, joined) =>
    call(
      admin,
      "admin/teams/" + team + "/members/" + member,
      "PUT",
      { joined, reason: "例会组调整" },
      204,
    );
  await join(writer.id, true);
  await assign(writer.id, "", dept);
  await assign(who.id, "", dept);
  await assign(writer.id, "", dept2, 409);
  assert.deepEqual(await call(manager, "management-scopes"), []);
  const page = await admin.newPage();
  await page.goto("/");
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByRole("button", { name: "企业管理", exact: true }).click();
  await page.getByRole("button", { name: "高级设置", exact: true }).click();
  await page.getByText("管理汇总授权", { exact: true }).click();
  await page.getByLabel("授权成员", { exact: true }).selectOption(who.id);
  await page.getByLabel("统计范围", { exact: true }).selectOption(team);
  await page.getByLabel("允许导出已授权的汇总字段").check();
  await page.getByLabel("授权原因", { exact: true }).fill("负责人核对协作交付");
  await page.getByRole("button", { name: "授予汇总查看权" }).click();
  await page.getByText("已授予汇总查看权", { exact: true }).waitFor();
  const grant = async (kind, scope, costs, expected = 201) =>
    call(
      admin,
      "admin/management-grants",
      "POST",
      {
        member: who.id,
        scope,
        kind,
        costs,
        export: kind === "department" && scope === dept,
        days: 7,
        reason: "按范围核对",
      },
      expected,
    );
  const deptGrant = (await grant("department", dept, true)).id;
  const teamGrant = (await call(manager, "management-scopes")).find(
    (s) => s.kind === "team",
  ).id;
  await grant("team", team, true, 409);
  const summary = (c, id, status = 200, query = "?days=7") =>
    call(
      c,
      "management-scopes/" + id + "/summary" + query,
      "GET",
      undefined,
      status,
    );
  const payerTeam = (await call(author, "teams")).find((t) => t.default).id;
  const payerGrant = (await grant("team", payerTeam, true)).id;
  const payerBefore = await summary(manager, payerGrant);
  await summary(author, teamGrant, 404);
  await summary(manager, teamGrant, 422, "?member=" + writer.id);
  await call(manager, "admin/usage", "GET", undefined, 403);
  await call(manager, "admin/management-scopes", "GET", undefined, 403);
  const fixedPeriod = {
    from: new Date(Date.now() - 86400000).toISOString(),
    to: new Date(Date.now() + 86400000).toISOString(),
    costs: false,
    purpose: "核对运行变化",
    recipient: "测试接收方",
  };
  const frozen = (
    await call(
      manager,
      "management-scopes/" + teamGrant + "/exports",
      "POST",
      fixedPeriod,
      201,
      crypto.randomUUID(),
    )
  ).id;
  const base =
    "projects/" +
    (
      await call(
        author,
        "projects",
        "POST",
        { goal: "合成例会简报", team },
        201,
      )
    ).id;
  await call(
    author,
    base + "/plan",
    "POST",
    { expected: 1, brief: "核对简报", agents: [process.env.SCOPE_AGENT] },
    204,
  );
  await call(
    author,
    base + "/runs",
    "POST",
    { expected: (await call(author, base)).revision, authorizeCalls: 1 },
    202,
    "scope-check",
  );
  let project;
  for (let i = 0; i < 160; i++) {
    project = await call(author, base);
    if (project.stage === "review") break;
    await new Promise((r) => setTimeout(r, 200));
  }
  assert.equal(project.stage, "review");
  await assign(writer.id, dept, dept2);
  const item = project.artifacts[0];
  await call(
    author,
    base + "/artifacts/" + item.id + "/review",
    "POST",
    { version: item.version },
    204,
  );
  await call(
    author,
    base + "/deliver",
    "POST",
    { expected: (await call(author, base)).revision },
    201,
  );
  await call(manager, base, "GET", undefined, 404);
  const teamResult = await summary(manager, teamGrant);
  assert.equal(teamResult.costs, null);
  assert.deepEqual(teamResult.runStates, { completed: 1 });
  assert.deepEqual(
    [teamResult.runs, teamResult.reviewed, teamResult.editions],
    [1, 1, 1],
  );
  const payerAfter = await summary(manager, payerGrant);
  assert.equal(payerAfter.costs.calls, payerBefore.costs.calls + 1);
  const deptResult = await summary(manager, deptGrant);
  assert.equal(deptResult.costs.calls, 1);
  assert.deepEqual(
    [deptResult.runs, deptResult.completed, deptResult.editions],
    [1, 1, 1],
  );
  assert.ok(!JSON.stringify(deptResult).includes(writer.id));
  const nextDept = (await grant("department", dept2, true)).id;
  assert.equal((await summary(manager, nextDept)).runs, 0);
  const mp = await manager.newPage();
  await mp.goto("/");
  await mp.getByRole("button", { name: "我的工作区" }).click();
  await mp.getByText("协作与授权", { exact: true }).click();
  await mp.getByText("获授权的管理汇总", { exact: true }).click();
  await mp.getByLabel("已授权范围").selectOption(deptGrant);
  const report = mp.getByRole("region", { name: "范围汇总结果" });
  await report.getByText(/调用 1 次/).waitFor();
  await report.screenshot({
    path: path.join(process.env.VISUAL_EVIDENCE, "scoped-department.png"),
  });
  const frozenPath = "management-scopes/" + teamGrant + "/exports/" + frozen;
  assert.equal((await call(manager, frozenPath)).outcomes.runs, 0);
  assert.deepEqual((await call(manager, frozenPath)).outcomes.runStates, {});
  assert.equal((await summary(manager, teamGrant)).runs, 1);
  await exerciseDetails({
    call,
    admin,
    manager,
    author,
    teamGrant,
    deptGrant,
    nextDept,
    payerGrant,
    writer,
    base,
    page,
    mp,
    report,
  });
  const exported = await exerciseExport({
    page,
    call,
    manager,
    author,
    admin,
    teamGrant,
    deptGrant,
    payerGrant,
    report,
    mp,
    deptResult,
  });
  await exerciseFinancialExport({
    call,
    admin,
    manager,
    author,
    deptGrant,
    payerTeam,
    dept,
    writer,
    page,
    mp,
    report,
    deptResult,
  });
  const enterprise = await exerciseEnterprise({
    call,
    admin,
    manager,
    author,
    who,
    writer,
    page,
    mp,
    deptGrant,
    teamGrant,
  });
  await exerciseMemberProfile({
    author,
    call,
    admin,
    manager,
    writer,
    team,
    payerTeam,
  });
  const revoke = (id) =>
    call(
      admin,
      "admin/management-grants/" + id,
      "DELETE",
      { reason: "恢复最小范围" },
      204,
    );
  await revoke(teamGrant);
  await summary(manager, teamGrant, 404);
  const teamCostGrant = (await grant("team", team, true)).id;
  const teamCosts = await summary(manager, teamCostGrant);
  assert.equal(teamCosts.runs, 1);
  assert.equal(teamCosts.costs.calls, 0);
  await assign(who.id, dept, dept2);
  assert.ok((await summary(manager, enterprise.id)).runs > 1);
  await call(manager, enterprise.filePath);
  await summary(manager, deptGrant, 404);
  await call(manager, exported, "GET", undefined, 404);
  await report.getByRole("button", { name: "刷新范围汇总" }).click();
  await report.getByRole("alert").waitFor();
  assert.equal(await report.locator("dd").count(), 0);
  await join(who.id, true);
  await join(who.id, false);
  await summary(manager, teamCostGrant, 404);
  await join(who.id, true);
  await summary(manager, teamCostGrant, 404);
  const timed = (await grant("team", team, false)).id;
  const taskList = (id) => "management-scopes/" + id + "/tasks";
  await call(
    admin,
    "admin/management-grants/" + timed + "/details",
    "PATCH",
    {
      details: true,
      detailCosts: false,
      expected: 1,
      reason: "验证明细到期边界",
    },
    204,
  );
  assert.equal((await call(manager, taskList(timed))).rows.length, 1);
  const database = process.env.SCOPE_DB;
  assert.match(database, /^smoke_[a-f0-9]{10}$/);
  assert.match(timed, /^[a-f0-9-]{36}$/);
  execFileSync("docker", [
    "exec",
    "alliance-workspace-database-1",
    "psql",
    "-X",
    "-v",
    "ON_ERROR_STOP=1",
    "-U",
    "alliance",
    "-d",
    database,
    "-c",
    "UPDATE management_grants SET expires_at=now()-interval '1 second' WHERE id='" +
      timed +
      "'",
  ]);
  await summary(manager, timed, 404);
  await call(manager, taskList(timed), "GET", undefined, 404);
  assert(
    !(await call(manager, "management-scopes")).some((g) => g.id === timed),
  );
  const renewed = (await grant("team", team, false)).id;
  assert.notEqual(renewed, timed);
  await call(manager, taskList(renewed), "GET", undefined, 404);
  await summary(manager, timed, 404);
  assert.equal((await summary(manager, renewed)).runs, 1);
  await call(
    admin,
    "admin/members/" + who.id,
    "PATCH",
    { password, role: "member", enabled: false, reason: "暂停合成账号" },
    204,
  );
  await call(
    admin,
    "admin/members/" + who.id,
    "PATCH",
    { password, role: "member", enabled: true, reason: "恢复合成账号" },
    204,
  );
  await call(manager, "login", "POST", {
    email: "scope-manager@sample.invalid",
    password,
  });
  await summary(manager, enterprise.id, 404);
  await call(manager, enterprise.filePath, "GET", undefined, 404);
  await summary(manager, nextDept, 404);
  assert.deepEqual(await call(manager, "management-scopes"), []);
  await page.getByRole("button", { name: "高级设置", exact: true }).click();
  await page.getByRole("button", { name: "刷新授权清单" }).click();
  await page.getByText("已结束的授权（最近 50 条）", { exact: true }).click();
  await page.getByText(/培训业务部 · 已调离授权部门/).waitFor();
  await page
    .getByText(/已移出授权团队/)
    .first()
    .waitFor();
  await page
    .getByText(/成员账号已停用/)
    .first()
    .waitFor();
  await writeFile(
    path.join(process.env.VISUAL_EVIDENCE, "management-scopes.json"),
    JSON.stringify(
      {
        fixture: "synthetic; real API, database, worker and Chromium",
        teamResult,
        deptResult,
        teamCosts,
        newDepartmentRuns: 0,
        payerTeamCallsAdded: payerAfter.costs.calls - payerBefore.costs.calls,
        endReasonsVisibleToAdmin: true,
        unauthorized: 404,
        ordinaryEnterpriseDashboard: 403,
        staleDepartmentUpdate: 409,
        duplicateGrant: 409,
        revocationClearedVisibleResult: true,
        returnDidNotRestoreGrants: true,
        expiredGrantStayedInaccessibleAfterReissue: true,
      },
      null,
      2,
    ),
  );
  console.log(
    "Browser: granted aggregates, independent fee scope, historical department, scope withdrawal and account return OK",
  );
} finally {
  await browser.close();
}

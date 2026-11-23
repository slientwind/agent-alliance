import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";

function micros(value) {
  const [whole, fraction = ""] = value.split(".");
  return BigInt(whole + fraction.padEnd(6, "0"));
}

export async function exerciseEnterprise({
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
}) {
  const register = "admin/management-grants",
    root = (id) => "management-scopes/" + id;
  const input = {
    kind: "enterprise",
    scope: "enterprise",
    days: 7,
    member: who.id,
    reason: "企业月度运营报告",
  };
  const get = (c, route, status = 200) =>
    call(c, route, "GET", undefined, status);
  await call(manager, register, "POST", input, 403);
  await call(admin, register, "POST", { ...input, scope: teamGrant }, 422);
  const originalDashboard = await get(
    admin,
    "admin/usage?days=30&view=dashboard",
  );
  assert.equal(originalDashboard.enterpriseExport, null);

  const editor = page.getByText("管理汇总授权", { exact: true }).locator("..");
  const select = editor.getByLabel("统计范围", { exact: true });
  assert.equal(await select.inputValue(), "");
  await select.selectOption("enterprise");
  await editor.getByText(/此次授权覆盖本企业全部团队/).waitFor();
  await editor.getByLabel("同时开放该范围的用量与费用汇总").check();
  await editor.getByLabel("允许导出已授权的汇总字段").check();
  await editor.getByLabel("授权成员", { exact: true }).selectOption(who.id);
  await editor.getByLabel("授权原因", { exact: true }).fill("企业月度运营报告");
  const created = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/admin/management-grants") &&
      r.request().method() === "POST",
  );
  await editor.getByRole("button", { name: "授予汇总查看权" }).click();
  const response = await created;
  assert.equal(response.status(), 201);
  const { id } = await response.json();
  await editor.getByText("已授予汇总查看权", { exact: true }).waitFor();
  assert.equal(await select.inputValue(), "");
  await call(admin, register, "POST", input, 409);
  const eligible = (await get(manager, "management-scopes")).find(
    (x) => x.id === id,
  );
  assert.equal(eligible.kind, "enterprise");
  assert.equal(eligible.details, false);
  const base = root(id);
  await get(admin, base + "/summary", 404);
  await get(author, base + "/summary", 404);
  await get(manager, base + "/tasks", 404);
  await get(manager, "admin/usage?view=dashboard", 403);

  await mp.evaluate(() => window.dispatchEvent(new Event("focus")));
  const picker = mp.getByLabel("已授权范围");
  await picker.selectOption(id);
  const panel = mp.getByRole("region", { name: "范围汇总结果" });
  await panel.getByText(/同一运行在企业内只计一次/).waitFor();
  await panel.getByRole("button", { name: "准备导出汇总" }).click();
  const form = mp.getByRole("region", { name: "管理汇总导出" });
  await form.getByText(new RegExp("企业全域")).waitFor();
  await form.getByLabel("接收对象").fill("内部运营组");
  await form.getByLabel("使用目的").fill("企业总量核对");
  await form.getByRole("checkbox").check();
  await form.getByRole("button", { name: "生成 JSON 文件" }).click();
  const download = mp.waitForEvent("download");
  await form.getByRole("button", { name: "下载管理汇总" }).click();
  const file = await download,
    bytes = await readFile(await file.path()),
    data = JSON.parse(bytes);
  assert.deepEqual(data.scope, { kind: "enterprise", name: eligible.name });
  const query = "?" + new URLSearchParams({ from: data.from, to: data.to });
  const rows = await get(admin, "admin/usage" + query);
  const sum = (field) => rows.reduce((n, r) => n + r[field], 0);
  for (const [field, metric] of Object.entries({
    runs: "runs",
    completed: "completed",
    waiting: "waiting",
    reviewed: "reviewedVersions",
    adopted: "adoptedAnswers",
    checked: "passedAgentChecks",
    editions: "deliveredEditions",
  }))
    assert.equal(data.outcomes[field], sum(metric));
  for (const [field, metric] of Object.entries({
    calls: "attempts",
    tokens: "tokens",
    pending: "unsettledCalls",
    unknown: "unknown",
  }))
    assert.equal(data.costs[field], sum(metric));
  const states = {};
  for (const row of rows)
    for (const [state, n] of Object.entries(row.runStates))
      states[state] = (states[state] ?? 0) + n;
  assert.deepEqual(data.outcomes.runStates, states);
  assert.equal(
    Object.values(states).reduce((n, v) => n + v, 0),
    data.outcomes.runs,
  );
  for (const field of ["recordedCny", "remainingEstimateCny"])
    assert.equal(
      micros(data.costs[field]),
      rows.reduce((n, r) => n + micros(r[field]), 0n),
    );
  assert(
    data.outcomes.runs >
      (await get(manager, root(teamGrant) + "/summary" + query)).runs,
  );
  assert(rows.some((row) => row.kind === "application" && row.runs > 0));
  assert(!bytes.includes(writer.name));
  const [receipt] = await get(manager, base + "/exports");
  const filePath = base + "/exports/" + receipt.id;
  await get(author, filePath, 404);
  await get(manager, root(teamGrant) + "/exports/" + receipt.id, 404);
  await get(admin, filePath, 404);
  await writeFile(
    path.join(process.env.VISUAL_EVIDENCE, "enterprise-summary-sample.json"),
    bytes,
  );
  await form.screenshot({
    path: path.join(process.env.VISUAL_EVIDENCE, "enterprise-export.png"),
  });
  await call(
    admin,
    register + "/" + id + "/details",
    "PATCH",
    { expected: 1, details: true, detailCosts: true, reason: "核对承担团队" },
    204,
  );
  const runs = (
    await get(manager, base + "/tasks" + query + "&member=" + writer.id)
  ).rows;
  assert.equal(runs.length, 1);
  assert.equal(runs[0].costs.entries, 1); // Charged to a different team.
  const auditView = await get(admin, "me");
  const selfGrant = {
    ...input,
    member: auditView.id,
    export: true,
    costs: false,
  };
  const competing = await Promise.all(
    [0, 1].map(() =>
      admin.request.post("/api/" + register, { data: selfGrant }),
    ),
  );
  assert.deepEqual(competing.map((r) => r.status()).sort(), [201, 409]);
  const adminGrant = (await competing.find((r) => r.status() === 201).json())
    .id;
  assert.equal(
    (await get(admin, "admin/usage?view=dashboard&member=" + writer.id))
      .enterpriseExport,
    null,
  );
  const dash = page.getByRole("region", { name: "运行监控" });
  const [current] = await Promise.all([
    page.waitForResponse(
      (r) => r.url().includes("admin/usage?") && r.url().includes("view=dashboard"),
    ),
    page.getByRole("button", { name: "用户与监控", exact: true }).click(),
  ]);
  const displayed = await current.json();
  await dash.getByRole("button", { name: "准备导出汇总" }).click();
  const adminForm = dash.getByRole("region", { name: "管理汇总导出" });
  assert.equal(await adminForm.getByRole("checkbox").count(), 0);
  await adminForm.getByLabel("接收对象").fill("企业管理组");
  await adminForm.getByLabel("使用目的").fill("核对大盘当前周期");
  const issue = page.waitForResponse(
    (r) =>
      r.url().endsWith("/" + adminGrant + "/exports") &&
      r.request().method() === "POST",
  );
  await adminForm.getByRole("button", { name: "生成 JSON 文件" }).click();
  const issued = await issue;
  assert.equal(issued.status(), 201);
  const requested = issued.request().postDataJSON();
  assert.equal(requested.from, displayed.from);
  assert.equal(requested.to, displayed.to);
  const adminFile = root(adminGrant) + "/exports/" + (await issued.json()).id;
  const adminBytes = await get(admin, adminFile);
  assert.equal(adminBytes.costs, undefined);
  assert.equal(adminBytes.scope.kind, "enterprise");
  await dash.getByLabel("发起成员或应用").selectOption(writer.id);
  assert.equal(
    await dash.getByRole("region", { name: "管理汇总导出" }).count(),
    0,
  );
  assert.equal(
    await dash.getByRole("button", { name: "准备导出汇总" }).count(),
    0,
  );
  await dash.getByLabel("发起成员或应用").selectOption("");
  await call(
    admin,
    register + "/" + adminGrant,
    "DELETE",
    { reason: "撤销临时企业授权" },
    204,
  );
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await dash
    .getByRole("button", { name: "准备导出汇总" })
    .waitFor({ state: "detached" });
  assert.equal(
    (await get(admin, "admin/usage?view=dashboard")).enterpriseExport,
    null,
  );
  await get(admin, adminFile, 404);
  const expired = (
    await call(
      admin,
      register,
      "POST",
      { ...selfGrant, costs: true, financialExport: true, export: false },
      201,
    )
  ).id;
  const financeOnly = await get(admin, "admin/usage?view=dashboard");
  assert.equal(financeOnly.enterpriseExport, null);
  assert.equal(financeOnly.enterpriseFinancialExport.id, expired);
  await get(admin, root(expired) + "/exports", 404);
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await dash.getByRole("button", { name: "准备导出逐笔费用" }).click();
  const finance = dash.getByRole("region", { name: "逐笔费用导出" });
  await finance.getByText("缩短导出周期", { exact: true }).click();
  const date = new Date(Date.now() - 2 * 86400000).toISOString().slice(0, 10);
  await finance.getByLabel("开始时间").fill(date + "T10:00");
  await finance.getByLabel("结束时间").fill(date + "T11:00");
  await finance.getByLabel("接收对象").fill("财务核对");
  await finance.getByLabel("使用目的").fill("核对短周期");
  const creating = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" && r.url().endsWith(expired + "/exports"),
  );
  await finance.getByRole("button", { name: "生成 JSON 文件" }).click();
  const financialResponse = await creating;
  assert.equal(financialResponse.status(), 201);
  const selected = financialResponse.request().postDataJSON();
  assert.equal(Date.parse(selected.to) - Date.parse(selected.from), 3600000);
  const financeFile = root(expired) + "/exports/" + (await financialResponse.json()).id;
  assert.equal((await get(admin, financeFile)).totals.calls, 0);
  await dash.getByLabel("发起成员或应用").selectOption(writer.id);
  assert.equal(
    await dash.getByRole("region", { name: "逐笔费用导出" }).count(),
    0,
  );
  await dash.getByLabel("发起成员或应用").selectOption("");

  const db = process.env.SCOPE_DB;
  assert.match(db, /^smoke_[a-f0-9]{10}$/);
  assert.match(expired, /^[a-f0-9-]{36}$/);
  execFileSync("docker", [
    "exec",
    "alliance-workspace-database-1",
    "psql",
    "-Xq",
    "-v",
    "ON_ERROR_STOP=1",
    "-U",
    "alliance",
    "-d",
    db,
    "-c",
    "update management_grants set expires_at=now()-interval '1 second' where id='" +
      expired +
      "'",
  ]);
  await get(admin, root(expired) + "/summary", 404);
  await get(admin, financeFile, 404);
  const replacement = (
    await call(admin, register, "POST", { ...selfGrant, export: false }, 201)
  ).id;
  assert.notEqual(replacement, expired);
  await get(admin, root(expired) + "/summary", 404);
  await get(admin, root(replacement) + "/summary");
  await call(
    admin,
    register + "/" + replacement,
    "DELETE",
    { reason: "到期测试完成" },
    204,
  );
  await picker.selectOption(deptGrant);

  await writeFile(
    path.join(process.env.VISUAL_EVIDENCE, "enterprise-summary.json"),
    JSON.stringify(
      {
        input:
          "synthetic records with real Go API, PostgreSQL, Temporal and Chromium",
        matchesAllEnterpriseOutcomes: true,
        includesApplicationRuns: true,
        matchesExactEnterpriseMoney: true,
        enterpriseRuns: data.outcomes.runs,
        enterpriseCalls: data.costs.calls,
        enterpriseName: eligible.name,
        explicitSelfGrantForAdminExport: true,
        concurrentSelfGrantStatuses: [201, 409],
        dashboardPeriodPinned: true,
        expiredGrantRemainedClosedAfterReissue: true,
        memberFilterHidesEnterpriseExport: true,
        scopedAndThirdPartyLinkStatus: 404,
        detailCrossTeamChargeEntries: runs[0].costs.entries,
      },
      null,
      2,
    ),
  );
  return { id, filePath };
}

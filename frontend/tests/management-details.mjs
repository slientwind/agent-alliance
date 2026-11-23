import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { writeFile } from "node:fs/promises";
import path from "node:path";

export async function exerciseDetails({
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
}) {
  const root = (id) => "management-scopes/" + id + "/";
  const get = (c, id, resource, expected = 200) =>
    call(c, root(id) + resource, "GET", undefined, expected);
  const policy = (c, id, expected, details, detailCosts, status = 204) =>
    call(
      c,
      "admin/management-grants/" + id + "/details",
      "PATCH",
      { expected, details, detailCosts, reason: "演练明细授权调整" },
      status,
    );
  await get(manager, deptGrant, "tasks", 404);
  await get(manager, deptGrant, "members", 404);
  await policy(manager, deptGrant, 1, true, true, 403);
  await policy(admin, deptGrant, 1, false, true, 422);
  await policy(admin, deptGrant, 1, true, false);
  await policy(admin, deptGrant, 1, true, true, 409);
  await get(author, deptGrant, "tasks", 404);
  await get(admin, deptGrant, "tasks", 404);
  assert.deepEqual((await get(manager, deptGrant, "members")).rows, [
    { id: writer.id, kind: "person", name: writer.name, runs: 1 },
  ]);
  assert.equal(
    (
      await get(
        manager,
        deptGrant,
        "members?q=" + encodeURIComponent("汇总业务"),
      )
    ).rows.length,
    1,
  );
  assert.equal(
    (
      await get(
        manager,
        deptGrant,
        "members?q=" + encodeURIComponent("范围管理者"),
      )
    ).rows.length,
    0,
  );
  const withoutMoney = (
    await get(manager, deptGrant, "tasks?member=" + writer.id)
  ).rows;
  assert.equal(withoutMoney.length, 1);
  const task = withoutMoney[0];
  assert.deepEqual(
    Object.keys(task).sort(),
    [
      "actor",
      "block",
      "createdAt",
      "elapsedSeconds",
      "finishedAt",
      "id",
      "kind",
      "reviewer",
      "status",
    ].sort(),
  );
  assert.equal(task.status, "completed");
  assert.equal(task.reviewer, writer.name);
  assert.ok(task.finishedAt && task.elapsedSeconds >= 0);
  await get(manager, deptGrant, "tasks?cursor=bad", 422);
  await get(manager, deptGrant, "tasks?content=true", 422);
  assert.equal(
    (await get(manager, deptGrant, "tasks?member=" + crypto.randomUUID())).rows
      .length,
    0,
  );
  await call(manager, base, "GET", undefined, 404);
  await policy(admin, deptGrant, 2, true, true);
  const charge = (await get(manager, deptGrant, "tasks")).rows[0].costs;
  assert.equal(charge.pending, 1);
  assert.equal(charge.unknown, 1);
  await policy(admin, teamGrant, 1, true, true);
  assert.equal(
    (await get(manager, teamGrant, "tasks")).rows[0].costs.entries,
    0,
  );
  await policy(admin, nextDept, 1, true, false);
  assert.equal((await get(manager, nextDept, "tasks")).rows.length, 0);
  await get(manager, payerGrant, "tasks", 404);

  // Seek through ties in both directories without starting extra jobs.
  const db = process.env.SCOPE_DB,
    project = base.split("/")[1];
  assert.match(db, /^smoke_[a-f0-9]{10}$/);
  assert.match(project, /^[0-9a-f-]{36}$/);
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
    `
    WITH people AS (
      INSERT INTO members(display_name,email,password_hash,role)
      SELECT 'ZZ分页样例'||n,'detail-page-'||n||'@sample.invalid','!','member' FROM generate_series(1,27) n RETURNING id
    ), historical AS (
      INSERT INTO runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,status,created_at)
      SELECT p.id,1,a.id,p.team_id,'historical-page','x'::bytea,1,'blocked','2020-05-02T00:00:00Z' FROM people a JOIN projects p ON p.id='${project}'
      RETURNING id
    ) INSERT INTO trace_events(run_id,kind,code) SELECT id,'checkpoint','budget_denied' FROM historical`,
  ]);
  const window = "from=2020-05-01T00:00:00Z&to=2020-05-03T00:00:00Z";
  for (const list of ["members", "tasks"]) {
    const first = await get(manager, deptGrant, list + "?" + window);
    const second = await get(
      manager,
      deptGrant,
      list + "?" + window + "&cursor=" + encodeURIComponent(first.next),
    );
    assert.equal(first.rows.length, 25);
    assert.equal(second.rows.length, 2);
    assert.equal(second.next, undefined);
    assert.equal(
      new Set([...first.rows, ...second.rows].map((x) => x.id)).size,
      27,
    );
    if (list === "tasks") assert.equal(first.rows[0].block, "budget");
  }
  await mp.setViewportSize({ width: 390, height: 844 });
  await mp.evaluate(() => window.dispatchEvent(new Event("focus")));
  await report.getByText("成员与任务明细", { exact: true }).click();
  const pane = mp.getByRole("region", { name: "管理明细" });
  await pane.getByRole("button", { name: writer.name + " · 查看任务" }).click();
  await pane.getByText("任务 " + task.id, { exact: true }).waitFor();
  await pane.getByText(/未知费用 1 笔/).waitFor();
  assert.ok(
    await pane.evaluate((e) => e.getBoundingClientRect().right <= innerWidth),
  );
  await pane.screenshot({
    path: path.join(
      process.env.VISUAL_EVIDENCE,
      "management-details-mobile.png",
    ),
  });

  await page.getByRole("button", { name: "刷新授权清单" }).click();
  const choice = page
    .locator("summary")
    .filter({ hasText: /^范围管理者 · 部门 · 培训业务部$/ });
  if ((await choice.locator("..").getAttribute("open")) === null)
    await choice.click();
  const controls = choice.locator("..");
  await controls.getByLabel("成员与任务明细权限").selectOption("summary");
  await controls
    .getByLabel("明细权限调整原因")
    .fill("演练结束，保留汇总和汇总导出");
  await controls.getByRole("button", { name: "保存明细权限" }).click();
  await page.getByText("明细授权已更新", { exact: true }).waitFor();
  await get(manager, deptGrant, "tasks", 404);
  await get(manager, deptGrant, "members", 404);
  await pane.getByRole("button", { name: "刷新明细" }).click();
  await pane.getByRole("alert").waitFor();
  assert.equal(await pane.locator("article").count(), 0);
  assert.equal((await get(manager, deptGrant, "summary")).runs, 1);
  await get(manager, deptGrant, "exports");
  await mp.setViewportSize({ width: 1280, height: 800 });
  if ((await choice.locator("..").getAttribute("open")) !== null)
    await choice.click();
  await writeFile(
    path.join(process.env.VISUAL_EVIDENCE, "management-details.json"),
    JSON.stringify(
      {
        source:
          "isolated enterprise, synthetic work, real Go API, PostgreSQL and Chromium",
        originalDepartmentRuns: 1,
        newDepartmentRuns: 0,
        originalPayerEntries: charge.entries,
        otherTeamEntries: 0,
        redactedTaskFields: Object.keys(task),
        unknownFees: charge.unknown,
        detailRevokedSummaryAvailable: true,
        pages: [25, 2],
        tiedRuns: 27,
        memberPages: [25, 2],
        staleWriteStatus: 409,
        ordinaryProjectReadStatus: 404,
      },
      null,
      2,
    ),
  );
  console.log(
    "Scoped member and task drilldown: page boundaries, redaction, billing scope, revoke without affecting summary and mobile rendering",
  );
}

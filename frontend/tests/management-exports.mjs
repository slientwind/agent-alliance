import assert from "node:assert/strict";
import { randomUUID, createHash } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { execFileSync } from "node:child_process";

export async function exerciseExport({
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
}) {
  const root = (id) => "management-scopes/" + id + "/exports";
  const input = {
    from: deptResult.from,
    to: deptResult.to,
    costs: false,
    purpose: "合成周期核对",
    recipient: "内部演练组",
  };
  const key = randomUUID();
  await call(manager, root(payerGrant), "POST", input, 403, randomUUID());
  await call(
    manager,
    root(teamGrant),
    "POST",
    { ...input, costs: true },
    403,
    randomUUID(),
  );
  await call(
    manager,
    root(deptGrant),
    "POST",
    { ...input, purpose: "" },
    422,
    randomUUID(),
  );
  const created = await Promise.all(
    [0, 1].map(() =>
      manager.request.post("/api/" + root(teamGrant), {
        data: input,
        headers: { "Idempotency-Key": key },
      }),
    ),
  );
  assert.deepEqual(created.map((x) => x.status()).sort(), [200, 201]);
  const [first, replayed] = await Promise.all(created.map((x) => x.json()));
  assert.equal(first.id, replayed.id);
  await call(
    manager,
    root(teamGrant),
    "POST",
    { ...input, recipient: "changed" },
    409,
    key,
  );
  const firstPath = root(teamGrant) + "/" + first.id;
  const business = await call(manager, firstPath);
  assert.equal(business.scope.kind, "team");
  assert.equal(business.outcomes.editions, 1);
  assert(!("costs" in business));
  await call(author, firstPath, "GET", undefined, 404);
  await call(admin, firstPath, "GET", undefined, 404);
  await call(manager, root(deptGrant) + "/" + first.id, "GET", undefined, 404);
  await report.getByRole("button", { name: "准备导出汇总" }).click();
  const form = report.getByRole("region", { name: "管理汇总导出" });
  await form.getByLabel("接收对象").fill("部门例会");
  await form.getByLabel("使用目的").fill("核对交付与承担的费用");
  await form.getByRole("checkbox").check();
  await form.getByRole("button", { name: "生成 JSON 文件" }).click();
  const button = form.getByRole("button", { name: "下载管理汇总" });
  await button.waitFor();
  await form.screenshot({
    path: path.join(process.env.VISUAL_EVIDENCE, "management-export.png"),
  });
  const down = mp.waitForEvent("download");
  await button.click();
  const file = await down;
  assert.equal(file.suggestedFilename(), "管理汇总.json");
  const bytes = await readFile(await file.path()),
    data = JSON.parse(bytes);
  assert.deepEqual(
    Object.keys(data).sort(),
    [
      "version",
      "scope",
      "from",
      "to",
      "generatedAt",
      "outcomes",
      "costs",
    ].sort(),
  );
  await writeFile(path.join(process.env.VISUAL_EVIDENCE, "management-export-sample.json"),bytes);
  assert.equal(data.scope.name, "培训业务部");
  assert.equal(data.costs.calls, 1);
  assert.equal(data.costs.unknown, 1);
  assert.equal(data.outcomes.reviewed, 1);
  assert(!bytes.includes("部门例会"));
  const [item] = await call(manager, root(deptGrant));
  const filePath = root(deptGrant) + "/" + item.id;
  const exact = await manager.request.get("/api/" + filePath);
  assert.equal(exact.headers()["cache-control"], "private, no-store");
  assert((await exact.body()).equals(bytes));
  await form.getByRole("button", { name: "关闭导出" }).click();
  await report.getByRole("button", { name: "查看近期导出" }).click();
  await report.getByText(/含费用/).waitFor();
  const db = process.env.SCOPE_DB;
  assert.match(db, /^smoke_[a-f0-9]{10}$/);
  const psql = (sql) =>
    execFileSync(
      "docker",
      [
        "exec",
        "alliance-workspace-database-1",
        "psql",
        "-XqtA",
        "-v",
        "ON_ERROR_STOP=1",
        "-U",
        "alliance",
        "-d",
        db,
        "-c",
        sql,
      ],
      { encoding: "utf8" },
    ).trim();
  assert.match(first.id, /^[a-f0-9-]{36}$/);
  psql(
    "update management_exports set expires_at=now()-interval '1 second' where id='" +
      first.id +
      "'",
  );
  await call(manager, firstPath, "GET", undefined, 410);
  await call(manager, root(teamGrant), "POST", input, 410, key);
  assert(
    !(await call(manager, root(teamGrant))).some(
      (entry) => entry.id === first.id,
    ),
  );
  assert.match(item.id, /^[a-f0-9-]{36}$/);
  const counts = psql(
    "select action,count(*) from audit_events where subject='" +
      item.id +
      "' group by action order by action",
  );
  assert.equal(
    counts,
    "management.export.create|1\nmanagement.export.download|2",
  );
  await page.getByRole("button", { name: "刷新授权清单" }).click();
  const choice = page
    .locator("summary")
    .filter({ hasText: /^范围管理者 · 部门 · 培训业务部$/ });
  await choice.click();
  const controls = choice.locator("..");
  await controls.getByLabel("导出权限调整原因").fill("审核已结束，保留查看权");
  await controls
    .getByRole("button", { name: "关闭导出并使已有下载失效" })
    .click();
  await page.getByText("已关闭导出，已有下载已失效", { exact: true }).waitFor();
  assert.equal(
    (await call(manager, "management-scopes/" + deptGrant + "/summary"))
      .editions,
    1,
  );
  await call(manager, filePath, "GET", undefined, 404);
  await call(manager, root(deptGrant), "POST", input, 403, randomUUID());
  await call(
    admin,
    "admin/management-grants/" + deptGrant + "/export",
    "PATCH",
    { enabled: true, expected: 1, reason: "迟到的调整" },
    409,
  );
  await controls.getByLabel("导出权限调整原因").fill("新的月度汇报需求");
  await controls.getByRole("button", { name: "开放汇总导出" }).click();
  await page.getByText("已开放导出", { exact: true }).waitFor();
  await call(manager, filePath, "GET", undefined, 410);
  assert.deepEqual(await call(manager, root(deptGrant)), []);
  const renewed = await call(
    manager,
    root(deptGrant),
    "POST",
    input,
    201,
    randomUUID(),
  );
  const newPath = root(deptGrant) + "/" + renewed.id;
  assert.equal((await call(manager, newPath)).outcomes.editions, 1);
  await writeFile(
    path.join(process.env.VISUAL_EVIDENCE, "management-export.json"),
    JSON.stringify(
      {
        source: "synthetic enterprise, actual HTTP, PostgreSQL and Chromium",
        jsonFields: Object.keys(data),
        costs: data.costs,
        sha256: createHash("sha256").update(bytes).digest("hex"),
        receiptOwnedByRequester: true,
        identicalDownloads: true,
        expiredStatus: 410,
        priorFileKeptOriginalRunCount: true,
        viewingSurvivedExportWithdrawal: true,
        restoredPermissionDidNotRestoreOldFile: true,
        simultaneousCreation: [200, 201],
        audit: { created: 1, downloaded: 2 },
      },
      null,
      2,
    ),
  );
  return newPath;
}

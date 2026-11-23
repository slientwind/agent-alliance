import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";

export async function exerciseFinancialExport({
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
}) {
  const root = "management-scopes/" + deptGrant;
  const collection = root + "/exports";
  const input = {
    from: deptResult.from,
    to: deptResult.to,
    recipient: "内部财务",
    purpose: "按调用核对入账",
    costs: true,
    costLines: true,
  };
  const generate = (status = 201, key = crypto.randomUUID(), data = input) =>
    call(manager, collection, "POST", data, status, key);
  await generate(403);
  const controls = page
    .locator("summary")
    .filter({ hasText: /^范围管理者 · 部门 · 培训业务部$/ })
    .locator("..");
  await controls.getByLabel("逐笔费用授权调整原因").fill("核对本部门调用账本");
  await controls
    .getByRole("button", { name: "开放逐笔费用导出", exact: true })
    .click();
  await page.getByText("已开放逐笔费用导出", { exact: true }).waitFor();
  await mp.evaluate(() => window.dispatchEvent(new Event("focus")));
  await report.getByRole("button", { name: "准备导出逐笔费用" }).click();
  const form = report.getByRole("region", { name: "逐笔费用导出" });
  await form.getByLabel("使用目的").fill(input.purpose);
  await form.getByLabel("接收对象").fill(input.recipient);
  const created = mp.waitForResponse(
    (r) => r.request().method() === "POST" && r.url().endsWith(collection),
  );
  await form.getByRole("button", { name: "生成 JSON 文件" }).click();
  const receipt = await (await created).json();
  const file = collection + "/" + receipt.id;
  const ready = mp.waitForEvent("download");
  await form.getByRole("button", { name: "下载逐笔费用" }).click();
  const download = await ready;
  assert.equal(download.suggestedFilename(), "逐笔费用.json");
  const snapshot = JSON.parse(await readFile(await download.path(), "utf8"));
  assert.deepEqual(snapshot.totals, deptResult.costs);
  assert.equal(snapshot.calls.length, 1);
  const [row] = snapshot.calls;
  assert.equal(row.payerTeamId, payerTeam);
  assert.equal(row.actorId, writer.id);
  assert.equal(row.departmentId, dept);
  assert.equal(row.operation, "execution");
  assert.ok(
    row.resource.budgetId && row.resource.revision > 0 && row.resource.name,
  );
  assert.equal(row.unknown, true);
  assert.equal(row.remainingEstimateCny, null);
  assert.deepEqual(row.postings, []);
  assert(!("project" in row) && !("body" in row));
  await call(admin, file, "GET", undefined, 404);
  await call(author, file, "GET", undefined, 404);
  for (const [reference, amount, final] of [
    ["first", "0.000001", false],
    ["last", "0.120000", true],
  ]) {
    await call(
      admin,
      "admin/bills",
      "POST",
      {
        line: "financial-fixture-" + reference,
        spend: row.ledgerId,
        amount,
        final,
      },
      204,
    );
  }
  assert.deepEqual(await call(manager, file), snapshot);
  const key = crypto.randomUUID();
  const settled = await generate(201, key);
  assert.equal((await generate(200, key)).id, settled.id);
  await generate(409, key, { ...input, purpose: "另一接收用途" });
  const updated = await call(manager, collection + "/" + settled.id);
  assert.equal(updated.calls[0].callId, row.callId);
  assert.equal(updated.calls[0].recordedCny, "0.120001");
  assert.equal(updated.calls[0].unknown, false);
  assert.deepEqual(
    updated.calls[0].postings.map((p) => p.amountCny),
    ["0.000001", "0.120000"],
  );
  const fixed = new URLSearchParams({ from: input.from, to: input.to });
  assert.deepEqual(
    updated.totals,
    (await call(manager, root + "/summary?" + fixed)).costs,
  );
  assert(!(await call(manager, collection)).some((x) => x.id === receipt.id));
  assert(
    (await call(manager, collection + "?kind=charges")).some(
      (x) => x.id === receipt.id,
    ),
  );
  await form.screenshot({
    path: path.join(process.env.VISUAL_EVIDENCE, "financial-export.png"),
  });
  await form.getByRole("button", { name: "关闭导出" }).click();
  const change = async (enabled) => {
    const g = (await call(admin, "admin/management-scopes")).grants.find(
      (x) => x.id === deptGrant,
    );
    await call(
      admin,
      "admin/management-grants/" + deptGrant + "/export",
      "PATCH",
      {
        financial: true,
        enabled,
        expected: g.exportRevision,
        reason: "核对结束和重新开放",
      },
      204,
    );
  };
  await change(false);
  await call(manager, file, "GET", undefined, 404);
  await generate(403);
  assert((await call(manager, collection)).length > 0);
  await change(true);
  await call(manager, file, "GET", undefined, 410);
  assert.deepEqual(await call(manager, collection + "?kind=charges"), []);
  await change(false);
  await writeFile(
    path.join(process.env.VISUAL_EVIDENCE, "financial-export.json"),
    JSON.stringify(
      {
        source: "synthetic calls and bills, PostgreSQL, Go and Chromium",
        scope: snapshot.scope.kind,
        before: snapshot.totals,
        after: updated.totals,
        postings: updated.calls[0].postings.map(({ amountCny, final }) => ({
          amountCny,
          final,
        })),
        originalSnapshotUnchanged: true,
        restoredPermissionRevivedFile: false,
        independentSummaryDownloads: true,
        filename: download.suggestedFilename(),
      },
      null,
      2,
    ),
  );
}

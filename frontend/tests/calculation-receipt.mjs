import { verifyReceiptPages } from "./receipt-pages.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkCalculationReceipt({
  page,
  get,
  path,
  before,
  directory,
  real,
}) {
  const card = page
    .locator("section")
    .filter({ has: page.getByRole("heading", { name: "补充核算原件" }) })
    .last();
  await card.waitFor({ timeout: 90000 });
  const held = await get(path);
  assert.equal(held.stage, "blocked");
  assert.equal(held.run.receiptRequest.step, 2);
  assert.equal(held.artifacts.length, 1);
  assert.equal(held.costSummary.calls, 1);
  const action = "/api/runs/" + held.run.id + "/calculation-receipts";
  const control = await page.request.post(
    "/api/runs/" + held.run.id + "/control",
    {
      headers: { "Idempotency-Key": "bare-" + held.run.id },
      data: {
        action: "resume",
        reason: "仍需补件",
        expected: held.run.controlRevision,
      },
    },
  );
  assert.equal(control.status(), 409);
  await page.reload();
  await card.waitFor();
  if (!real) await verifyReceiptPages(page, path, held, card, directory);
  const quote = "住宿结算实付 528.50 元。";
  const prefix = "📎 旧记录\n" + quote + "\n本次结算\n";
  const sourceText = prefix + quote + "\n仅供内部归档的订房编号 OFFICE-54。";
  const from = Array.from(prefix).length + 1;
  const to = from + Array.from(quote).length - 1;
  await card.getByLabel("补充原件", { exact: true }).setInputFiles({
    name: "本次结算.txt",
    mimeType: "text/plain",
    buffer: Buffer.from(sourceText),
  });
  await card.screenshot({
    path:
      directory +
      "/calculation-receipt-" +
      (real ? "local" : "fixture") +
      "-wait.png",
  });
  const submit = card.getByRole("button", { name: "确认补件并继续核算" });
  const post = () =>
    page.waitForResponse(
      (r) =>
        r.url().endsWith("/calculation-receipts") &&
        r.request().method() === "POST",
    );
  const invalid = post();
  await submit.click();
  assert.equal((await invalid).status(), 422);
  assert.equal((await get(path)).planVersion, before.planVersion);
  await card.getByLabel(/含金额的原句/).fill(quote);
  const ambiguous = post();
  await submit.click();
  assert.equal((await ambiguous).status(), 422);
  const listing = page.waitForResponse((r) =>
    r.url().includes("/receipt-passages?"),
  );
  await card.getByRole("button", { name: "上传并查看金额原句" }).click();
  const list = await listing;
  assert.equal(list.status(), 200, await list.text());
  const rows = (await list.json()).rows;
  assert.deepEqual(
    rows.map((r) => r.text),
    [quote, quote],
  );
  assert.deepEqual(rows.map((r) => r.preceding), ["📎 旧记录", "本次结算"]);
  const badURL = new URL(list.url());
  badURL.searchParams.set("step", "200");
  assert.equal((await page.request.get(badURL.toString())).status(), 404);
  const candidate = card.getByRole("button", {
    name: new RegExp("前文 · 本次结算.*第 " + from + "–" + to + " 字"),
  });
  await candidate.click();
  await card.screenshot({
    path:
      directory + "/receipt-passages-" + (real ? "local" : "fixture") + ".png",
  });
  assert.equal((await get(path)).costSummary.calls, 1);
  const bad = await page.request.post(action, {
    headers: { "Idempotency-Key": crypto.randomUUID() },
    data: {
      expected: held.run.controlRevision,
      step: 2,
      source: badURL.searchParams.get("source"),
      version: 1,
      excerpt: quote,
      excerptStart: from + 1,
    },
  });
  assert.equal(bad.status(), 422);
  const accepted = post();
  await submit.click();
  const response = await accepted;
  assert.equal(response.status(), 202, await response.text());
  const replay = await page.request.post(action, {
    headers: {
      "Idempotency-Key": response.request().headers()["idempotency-key"],
    },
    data: response.request().postDataJSON(),
  });
  assert.equal(replay.status(), 202, await replay.text());
  const conflict = await page.request.post(action, {
    headers: {
      "Idempotency-Key": response.request().headers()["idempotency-key"],
    },
    data: { ...response.request().postDataJSON(), excerptStart: rows[0].start },
  });
  assert.equal(conflict.status(), 409);
  assert.equal((await page.request.get(list.url())).status(), 404);
  await page
    .getByRole("button", { name: "审核此版本" })
    .first()
    .waitFor({ timeout: 90000 });
  const finished = await get(path);
  assert.equal(finished.stage, "review");
  assert.equal(finished.run.id, held.run.id);
  assert.equal(finished.costSummary.calls, finished.plan.nodes.length);
  assert.equal(finished.planVersion, before.planVersion + 1);
  assert.equal(finished.artifacts[0].id, held.artifacts[0].id);
  const answer = finished.artifacts.find((a) => a.ordinal === 2);
  assert.match(answer.body, /差额为 78\.50 元/);
  assert.ok(answer.body.includes(quote));
  assert.ok(!answer.body.includes("OFFICE-54"));
  const link = page
    .getByRole("link", { name: /查看本次结算.txt v1 原件/ })
    .last();
  assert.equal(
    await (await page.request.get(await link.getAttribute("href"))).text(),
    sourceText,
  );
  await page
    .locator("article")
    .filter({ has: page.getByRole("heading", { name: /原文与金额核算/ }) })
    .screenshot({
      path:
        directory +
        "/calculation-receipt-" +
        (real ? "local" : "fixture") +
        "-answer.png",
    });
  const mode = real ? "local" : "fixture";
  for (const _ of finished.artifacts)
    await page.getByRole("button", { name: "审核此版本" }).first().click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  await page.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
  const zip = await page.request.get("/api/" + path + "/delivery/package");
  assert.ok(zip.ok());
  await writeFile(
    directory + "/calculation-receipt-" + mode + ".zip",
    await zip.body(),
  );
  await writeFile(
    directory + "/calculation-receipt-" + mode + ".json",
    JSON.stringify(
      {
        run: finished.run.id,
        searchArtifact: held.artifacts[0].id,
        selectedRange: { from, to },
        initialCalls: held.costSummary.calls,
        finalCalls: finished.costSummary.calls,
        original: before.planVersion,
        amendment: finished.planVersion,
        pins: finished.plan.sources,
        answer: answer.body,
        replay: replay.status(),
      },
      null,
      2,
    ),
  );
}

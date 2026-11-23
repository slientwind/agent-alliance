import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

export async function verifyReceiptPages(
  page,
  projectPath,
  held,
  card,
  directory,
) {
  const file = await readFile(
    new URL("./fixtures/receipt-pages.docx", import.meta.url),
  );
  const source = await page.request.post(
    "/api/" + projectPath + "/sources?name=pages.docx",
    { data: file },
  );
  assert.ok(source.ok(), await source.text());
  const { id, version, readable } = await source.json();
  assert.ok(readable);
  await page.reload();
  await card.getByLabel("补件来源").selectOption(id);
  const displayed = page.waitForResponse((r) =>
    r.url().includes("/receipt-passages?"),
  );
  await card.getByRole("button", { name: "查看金额原句", exact: true }).click();
  const response = await displayed;
  assert.equal(response.status(), 200, await response.text());
  const first = await response.json();
  assert.equal(first.rows.length, 20);
  assert.equal(first.more, true);
  assert.equal(first.rows[0].location, "段落 1");
  assert.equal(first.rows[19].location, "段落 20");
  await card.getByRole("button", { name: "下一页原句" }).click();
  const choice = card.getByRole("button", { name: /92\.40 元.*段落 22/ });
  await choice.click();
  const quote = await card.getByLabel(/含金额的原句/).inputValue();
  assert.ok(
    quote.includes("92.40 元") &&
      !quote.includes("13.20 元") &&
      Array.from(quote).length <= 320,
  );
  await card.screenshot({ path: directory + "/receipt-pages-office.png" });
  const wrongVersion = new URL(response.url());
  wrongVersion.searchParams.set("version", "2");
  assert.equal((await page.request.get(wrongVersion.toString())).status(), 404);
  await card.getByRole("button", { name: "上一页原句" }).click();
  await card.getByRole("button", { name: /段落 1 ·/ }).waitFor();
  assert.equal(await card.getByLabel(/含金额的原句/).inputValue(), quote);
  const deleted = await page.request.delete(
    "/api/" + projectPath + "/sources/" + id + "?version=" + version,
  );
  assert.ok(deleted.ok(), await deleted.text());
  assert.equal((await page.request.get(response.url())).status(), 404);
  const denied = await page.request.post(
    "/api/runs/" + held.run.id + "/calculation-receipts",
    {
      headers: { "Idempotency-Key": crypto.randomUUID() },
      data: {
        expected: held.run.controlRevision,
        step: 2,
        source: id,
        version,
        excerpt: quote,
      },
    },
  );
  assert.equal(denied.status(), 409);
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await card.getByText("原件已变化，请重新选择").waitFor({ state: "attached" });
  assert.equal(await card.getByLabel(/含金额的原句/).count(), 0);
  await card.getByLabel("补件来源").selectOption("");
}

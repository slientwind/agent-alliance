import { chromium } from "playwright";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import path from "node:path";

const origin = process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173";
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  const requests = [];
  const initial = "2026-03-01T00:00:00.000Z",
    later = "2026-03-02T00:00:00.000Z";
  let start = initial,
    open = true;
  const expiresAt = "2026-06-01T00:00:00.000Z";
  await page.route("**/api/**", async (r) => {
    const endpoint = new URL(r.request().url()).pathname;
    if (endpoint.endsWith("/exports") && r.request().method() === "POST") {
      requests.push({
        key: r.request().headers()["idempotency-key"],
        ...r.request().postDataJSON(),
      });
      if (requests.length === 1)
        return r.fulfill({ status: 503, json: { error: "暂时无法获取回执" } });
      if (requests.length === 2)
        return r.fulfill({
          status: 410,
          json: { error: "该文件已失效，请在当前授权下重新生成" },
        });
      return r.fulfill({ status: 201, json: { id: "sample-file", expiresAt } });
    }
    const result = {
      "/api/instance": { name: "合成企业", initialized: true },
      "/api/me": { id: "member", name: "汇总使用者", role: "member" },
      "/api/management-scopes": [
        {
          id: "sample",
          name: "部门示例",
          kind: "department",
          costs: false,
          export: open,
          expiresAt,
        },
      ],
      "/api/management-scopes/sample/summary": {
        runs: 1,
        completed: 1,
        waiting: 0,
        checked: 0,
        reviewed: 1,
        adopted: 0,
        editions: 1,
        costs: null,
        from: start,
        to: "2026-04-01T00:00:00.000Z",
        checkedAt: "2026-04-01T00:00:00.000Z",
        expiresAt,
      },
    };
    return r.fulfill({ json: result[endpoint] ?? [] });
  });
  await page.goto(origin);
  await page.getByRole("button", { name: "我的工作区" }).click();
  await page.getByText("协作与授权", { exact: true }).click();
  await page.getByText("获授权的管理汇总", { exact: true }).click();
  await page.getByRole("button", { name: "准备导出汇总" }).click();
  const form = page.getByRole("region", { name: "管理汇总导出" });
  assert.equal(await form.getByRole("checkbox").count(), 0);
  await form.getByLabel("接收对象").fill("部门内");
  await form.getByLabel("使用目的").fill("周会");
  start = later;
  const read = page.waitForResponse((r) => r.url().includes("/sample/summary"));
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await (await read).finished();
  const generate = form.getByRole("button", { name: "生成 JSON 文件" });
  await generate.click();
  await form.getByRole("alert").getByText("暂时无法获取回执").waitFor();
  await generate.click();
  await form
    .getByRole("alert")
    .getByText(/该文件已失效/)
    .waitFor();
  await generate.click();
  await form.getByRole("button", { name: "下载管理汇总" }).waitFor();
  assert.equal(requests.length, 3);
  assert.equal(requests[0].key, requests[1].key);
  assert.notEqual(requests[1].key, requests[2].key);
  assert(requests.every((r) => r.from === initial && !r.costs));
  assert(await form.evaluate((el) => el.scrollWidth <= el.clientWidth));
  if (process.env.VISUAL_EVIDENCE) {
    await form.screenshot({
      path: path.join(process.env.VISUAL_EVIDENCE, "mobile-export.png"),
    });
  }
  open = false;
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await form.waitFor({ state: "detached" });
  assert.equal(
    await page.getByRole("button", { name: "准备导出汇总" }).count(),
    0,
  );
  if (process.env.VISUAL_EVIDENCE)
    await writeFile(
      path.join(process.env.VISUAL_EVIDENCE, "export-ui-retry.json"),
      JSON.stringify(
        {
          fixture: "synthetic browser responses",
          periodStayedFixed: true,
          transientFailureReusedKey: true,
          expiredReceiptGeneratedNewKey: true,
          viewOnlyAfterExportWithdrawal: true,
          mobileOverflow: false,
        },
        null,
        2,
      ),
    );
  console.log(
    "Browser: frozen export period, retry identity, expired receipt regeneration and scoped permission refresh OK",
  );
} finally {
  await browser.close();
}

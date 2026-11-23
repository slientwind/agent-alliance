import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";
const browser = await chromium.launch();
const widths = [];
try {
  for (const width of [320, 390, 1440]) {
    const page = await browser.newPage({
      baseURL: process.env.SMOKE_ORIGIN,
      viewport: { width, height: 850 },
    });
    let clock = {
      project: "project",
      title: "培训材料审核",
      kind: "review",
      name: "Manual-" + "LongVersion".repeat(12),
      version: 3,
      clock: "clock",
      revision: 1,
      phase: "escalated",
      opened: "2026-01-04T00:00:00Z",
      originalDue: "2026-01-05T00:00:00Z",
      due: "2026-01-05T00:00:00Z",
      overdue: true,
      owner: true,
      assignees: ["业务审核人"],
    };
    const writes = [];
    await page.route("**/api/**", async (route) => {
      const req = route.request(),
        url = new URL(req.url()).pathname;
      if (url.endsWith("/extend")) {
        writes.push(req.postDataJSON());
        await route.fulfill({ status: 204 });
        return;
      }
      const data = {
        "/api/me": { id: "owner", name: "Owner", role: "member" },
        "/api/instance": { initialized: true, name: "演示" },
        "/api/projects/review-reminders": [clock],
        "/api/teams": [
          { id: "team", name: "培训团队", default: true, joined: true },
        ],
      };
      await route.fulfill({ json: data[url] ?? [] });
    });
    await page.goto("/?inbox=review");
    const drawer = page.getByRole("dialog");
    await drawer.getByText(/1 项需协调/).click();
    await drawer.getByText("调整本项期限", { exact: true }).click();
    const form = drawer
      .locator("form")
      .filter({ has: page.getByLabel("延期原因") });
    await form.getByLabel("新截止时间（本地时间）").fill("2027-01-08T10:30");
    await form.getByLabel("延期原因").fill("业务方需要补充核对");
    clock = { ...clock, revision: 2, due: "2027-01-07T00:00:00Z" };
    await drawer.getByRole("button", { name: "刷新提醒" }).click();
    const acknowledge = form.getByRole("button", { name: "已核对当前期限" });
    await acknowledge.waitFor();
    assert.ok(
      await form.getByRole("button", { name: "确认延期" }).isDisabled(),
    );
    assert.equal(
      await form.getByLabel("延期原因").inputValue(),
      "业务方需要补充核对",
    );
    assert.ok(await drawer.evaluate((el) => el.scrollWidth <= el.clientWidth));
    if (width === 390)
      await form.screenshot({
        path: process.env.VISUAL_EVIDENCE + "/concurrent-extension-mobile.png",
      });
    await acknowledge.click();
    await form.getByRole("button", { name: "确认延期" }).click();
    await form.waitFor({ state: "hidden" });
    assert.deepEqual(
      writes.map((x) => [x.expected, x.reason]),
      [[2, "业务方需要补充核对"]],
    );
    widths.push(width);
    await page.close();
  }
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/concurrent-extension-mock.json",
    JSON.stringify(
      {
        data: "mock",
        widths,
        staleSubmitDisabled: true,
        draftPreserved: true,
        currentRevisionSubmitted: 2,
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179", viewport: { width: 1440, height: 900 } });
  page.setDefaultTimeout(10000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const project = "2b40fe15-8725-45cf-9576-5661de125cb5";
  const privateID = "ce79221f-3d4c-45f7-a299-7015c7c556b4";
  const mutation = "3cf3a247-312c-4c93-88e9-ef2135591b3b";
  const path = "/api/projects/" + project;
  const team = "/api/spaces/team-space";
  const cite = team + "/knowledge/documents/guide/citations/3/1";
  const citation = { documentId: "guide", spaceId: "team-space", title: "库存操作规程", version: 3, ordinal: 1,
    breadcrumb: ["出库"], citationAnchor: { kind: "sheet", sheet: "库存", startRow: 5, endRow: 7 } };
  const artifact = { id: "artifact", name: "仓库成果", ordinal: 1, version: 2, body: "仓库核对完毕",
    file: null, approved: true, reviewNotesReadable: true, feedback: [], sourceChanges: [] };
  const work = {
    id: project, title: "入库差异核对", stage: "brief", revision: 1, owner: "member", reviewer: "member", team: "stock", planVersion: 0,
    spaceKind: "team", spaceId: "team-space", canReview: true,
    messages: [{ seq: 1, kind: "user", mine: true, body: "核对仓库差异" }], participants: [], handoffs: [],
    sharedAgents: [], deliveries: [], distributionCases: [], budget: { warning: false, pending: 0 }, costSummary: {},
    run: { id: "run", status: "completed", general: true, attempts: [] }, suggestion: null, plan: null,
    artifacts: [{ ...artifact, knowledgeCitations: [citation], businessProvenance: [{
      viewId: "view", viewName: "库存查询", source: "库存 ERP", access: "本人目前负责的仓库", rows: 2,
      formulas: { quantity: "sum(units)" }, formulaSources: { quantity: { term: "核准件数", revision: 4 } },
    }] }],
  };
  const personal = { ...work, id: privateID, title: "个人核对", spaceKind: "personal", spaceId: "private-space",
    run: null, artifacts: [{ ...artifact, canPromote: true }] };
  let decided = false;
  let promote;
  let captured;
  let readable = true;
  await page.route("**/api/**", route => {
    const req = route.request(), pathname = new URL(req.url()).pathname;
    if (pathname === path + "/system-mutations/" + mutation + "/decision") {
      assert.deepEqual(req.postDataJSON(), { expected: 3, accept: true });
      decided = true;
      work.revision++;
      return route.fulfill({ status: 204 });
    }
    if (pathname === team + "/eval-suites/suite/cases") {
      captured = req.postDataJSON();
      return route.fulfill({ status: 201, json: { id: "regression-case" } });
    }
    if (pathname === "/api/spaces/private-space/promote") {
      assert.ok(req.headers()["idempotency-key"]);
      promote = req.postDataJSON();
      return route.fulfill({ status: 201, json: { id: "snapshot", spaceId: "team-space" } });
    }
    if (pathname === cite && !readable) return route.fulfill({ status: 404, json: { error: "知识出处已不可用" } });
    if (pathname.endsWith("-events")) return route.fulfill({ contentType: "text/event-stream", body: ": heartbeat\n\n" });
    const fixture = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "项目发起人", role: "member", enabled: true },
      "/api/teams": [{ id: "stock", name: "供应链", joined: true, default: true }],
      "/api/spaces": [{ id: "private-space", kind: "personal", name: "个人空间", role: "space_admin" },
        { id: "team-space", kind: "team", name: "供应链", role: "space_admin", teamId: "stock" }],
      "/api/projects": [work, personal],
      [path]: work,
      ["/api/projects/" + privateID]: personal,
      [cite]: { ...citation, excerpt: "查阅范围 · 库存行 5 至 7", docStatus: "active" },
      [path + "/runs/run/activity"]: { items: [], dlp: [{ outcome: "masked", counts: { mobile: 2, floor_price: 1 } }] },
      [path + "/system-mutations"]: [{ mutationId: mutation, label: "更新补货量", recordKey: "STOCK-11",
        systemName: "库存 ERP", status: decided ? "succeeded" : "pending", revision: decided ? 4 : 3, canDecide: !decided,
        before: { quantity: 11 }, after: { quantity: 18 }, externalReceiptNo: decided ? "ERP-178" : "" }],
      [team + "/eval-suites"]: [{ id: "suite", name: "仓库月检" }],
      [team + "/publications"]: promote ? [{ id: "snapshot", kind: "artifact", name: "仓库成果" }] : [],
      [team + "/publications/snapshot"]: { id: "snapshot", kind: "artifact", name: "仓库成果", body: "团队可见版本" },
      [team + "/knowledge/documents"]: { documents: [], next: "" },
    };
    return route.fulfill({ json: fixture[pathname] ?? [] });
  });
  await page.goto("/chat/" + project + "#mutation-" + mutation);
  const card = page.locator("#mutation-" + mutation);
  await card.waitFor();
  assert.equal(await card.evaluate(node => node === document.activeElement), true);
  await card.getByRole("cell", { name: "11", exact: true }).waitFor();
  await card.getByRole("cell", { name: "18", exact: true }).waitFor();
  await page.getByText("DLP 外发保护已自动脱敏 · 手机号 2 处、底价 1 处").waitFor();
  await card.getByRole("button", { name: "批准写入系统（幂等执行）" }).click();
  await card.getByText("ERP-178").waitFor();
  await page.getByText("取数口径 · 库存查询").click();
  await page.getByText("本人目前负责的仓库").waitFor();
  await page.getByText("核准件数").waitFor();
  const chip = page.getByRole("button", { name: /库存操作规程 v3.*第 5–7 行/ });
  await chip.click();
  const canvas = page.getByRole("complementary", { name: "知识溯源" });
  await canvas.getByText("查阅范围 · 库存行 5 至 7").waitFor();
  assert.equal(await canvas.getByRole("link", { name: "打开原文" }).getAttribute("href"), cite + "/file");
  await canvas.getByRole("heading", { name: "知识溯源" }).press("Escape");
  await canvas.waitFor({ state: "hidden" });
  readable = false;
  await chip.click();
  await canvas.getByRole("alert").getByText("知识出处已不可用").waitFor();
  assert.equal(await canvas.getByRole("link", { name: "打开原文" }).count(), 0);
  await canvas.getByRole("button", { name: "关闭知识溯源" }).click();
  await page.getByText("加入黄金回归体检集").click();
  await page.getByRole("button", { name: "保存为回归用例" }).click();
  await page.getByText("已保存为脱敏回归用例。").waitFor();
  assert.deepEqual(captured, { artifactId: "artifact", version: 2 });

  await page.goto("/chat/" + privateID);
  await page.getByRole("button", { name: "移交至团队空间" }).click();
  const dialog = page.getByRole("dialog", { name: "移交至团队空间" });
  await dialog.getByRole("button", { name: "确认移交成果" }).click();
  await dialog.getByRole("button", { name: "查看团队成果快照" }).click();
  await page.getByRole("region", { name: "团队成果快照" }).getByText("团队可见版本").waitFor();
  assert.deepEqual(promote, { destination: "team-space", kind: "artifact", item: "artifact", version: 2 });
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}

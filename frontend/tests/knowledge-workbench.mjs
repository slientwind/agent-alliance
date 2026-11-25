import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179", viewport: { width: 390, height: 840 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const root = "/api/spaces/team-space";
  const knowledge = root + "/knowledge/";
  const business = root + "/business/";
  const source = { documentId: "team-doc", version: 3, ordinal: 1, title: "仓库制度", value: "2", unit: "天", comparator: "≤" };
  const changes = new Map();
  const docs = (id, title, collectionId) => ({ documents: [
    { id, title, collectionId, path: ["操作规程"], docStatus: "active", parseStatus: "ready", sensitivity: "internal", contentVersion: 3 },
  ], next: "" });
  const fixture = {
    "/api/instance": { initialized: true, name: "测试企业" },
    "/api/me": { id: "owner", name: "空间管理员", role: "admin", enabled: true },
    "/api/teams": [{ id: "stock-team", name: "供应链", joined: true, default: true }],
    "/api/spaces": [
      { id: "team-space", kind: "team", name: "供应链", teamId: "stock-team", role: "space_admin" },
      { id: "commons", kind: "commons", name: "全企公共底座", role: "member" },
    ],
    [root + "/members"]: [{ id: "person", name: "仓库负责人" }],
    [knowledge + "collections"]: [{ id: "wiki", name: "供应链知识库", provider: "feishu_wiki", state: "ready", aclMode: "source", enabled: true }],
    [knowledge + "documents"]: docs("team-doc", "团队操作指南", "wiki"),
    "/api/spaces/commons/knowledge/collections": [{ id: "manual", name: "公共目录", provider: "manual", state: "ready" }],
    "/api/spaces/commons/knowledge/documents": docs("commons-doc", "公用操作指南", "manual"),
    [knowledge + "gaps"]: [{ id: "gap", topic: "周转天数", askCount: 3, status: "open", revision: 4, sampleMaskedQueries: [] }],
    [knowledge + "conflicts"]: [{ id: "conflict", metric: "出库时限", status: "open", revision: 2, left: source, right: { ...source, title: "配送制度", value: "3" } }],
    [business + "connectors"]: [{ id: "erp", spaceId: "team-space", name: "库存 ERP", provider: "kingdee", driver: "saas_pack",
      category: "erp", authMode: "obo", health: "ready", enabled: true, revision: 1, operations: [] }],
    [business + "views"]: [{ id: "moves", spaceId: "team-space", connectorId: "erp", name: "出库记录", description: "按员工授权读取",
      sensitivity: "internal", enabled: true, revision: 2, dimensions: { region: { label: "地区", type: "string" } },
      metrics: { qty: { label: "出库数量", formula: "sum(quantity)" } }, relationships: [] }],
    [business + "connectors/erp/obo"]: { bound: true, authorized: true },
    [root + "/templates"]: [{ id: "template", spaceId: "team-space", scope: "team", name: "库存月报", format: "xlsx",
      active: true, default: false, revision: 3 }],
  };
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() !== "GET") {
      changes.set(path, request.postDataJSON());
      if (path === business + "views/moves/test") return route.fulfill({ json: { rowCount: 2, provenance: { access: "obo" } } });
      return route.fulfill({ json: {} });
    }
    return route.fulfill({ json: fixture[path] ?? [] });
  });
  await page.goto("/knowledge");
  const docPanel = page.getByRole("region", { name: "空间文档" });
  const link = docPanel.getByRole("link", { name: "团队操作指南" });
  await link.waitFor();
  assert.equal(await link.getAttribute("href"), knowledge + "documents/team-doc/citations/3/1/file");
  await docPanel.getByRole("link", { name: "公用操作指南" }).waitFor();
  await page.getByRole("button", { name: "供应链知识库" }).click();
  await docPanel.getByRole("link", { name: "公用操作指南" }).waitFor({ state: "hidden" });
  await page.getByLabel("补齐文档负责人").selectOption("person");
  await page.getByRole("button", { name: "指派补齐文档" }).click();
  await page.getByText("确认适用口径").click();
  await page.getByLabel("消歧原因").fill("按最新仓库制度执行");
  await Promise.all([
    page.waitForResponse(response => response.url().endsWith("/conflicts/conflict/resolution")),
    page.getByRole("button", { name: "保存口径决定" }).click(),
  ]);
  assert.deepEqual(changes.get(knowledge + "gaps/gap/assignee"), { assigneeId: "person", expectedVersion: 4 });
  assert.deepEqual(changes.get(knowledge + "conflicts/conflict/resolution"), { resolution: "use_left", note: "按最新仓库制度执行", expectedVersion: 2 });

  const library = page.getByRole("tab", { name: "知识库与雷达" });
  await library.press("ArrowRight");
  await page.getByText("库存 ERP").waitFor();
  await page.getByRole("button", { name: "测试行权限连通" }).click();
  await page.getByText("连通 · 2 行 · obo").waitFor();
  assert.deepEqual(changes.get(business + "views/moves/test"), { dimensions: ["region"] });
  await page.getByText("出库记录", { exact: true }).click();
  await page.getByText("sum(quantity)", { exact: true }).waitFor();

  await page.getByRole("tab", { name: "术语口径与交付母版" }).click();
  await page.getByRole("button", { name: "新增术语" }).click();
  const form = page.getByRole("form", { name: "编辑术语口径" });
  await form.getByLabel("术语", { exact: true }).fill("出库及时率");
  await form.getByLabel("同义词").fill("按时率，达成率");
  await form.getByLabel("口径说明").fill("当日出库量占应出库量");
  await form.getByRole("button", { name: "保存术语" }).click();
  await form.waitFor({ state: "hidden" });
  const glossary = [...changes].find(([path]) => path.startsWith(root + "/glossary/"));
  assert.deepEqual(glossary?.[1].aliases, ["按时率", "达成率"]);
  await Promise.all([
    page.waitForResponse(response => response.url().endsWith("/templates/template/settings")),
    page.getByRole("button", { name: "设为空间默认" }).click(),
  ]);
  assert.deepEqual(changes.get(root + "/templates/template/settings"), { active: true, default: true, expectedVersion: 3 });
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}

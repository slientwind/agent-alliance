import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { createServer } from "vite";

let server;
before(async () => {
  server = await createServer({ configFile: false, optimizeDeps: { noDiscovery: true }, server: { middlewareMode: true, ws: false } });
});
after(async () => { await server.close(); });

test("workspace routes cover the sidebar and preserve legacy chat links", async () => {
  const { pageFromPath, projectFromPath, chatPath } = await server.ssrLoadModule("/src/workspace-routes.ts");
  assert.equal(pageFromPath("/inbox"), "history");
  assert.equal(pageFromPath("/knowledge"), "knowledge");
  assert.equal(pageFromPath("/agents"), "library");
  assert.equal(pageFromPath("/agents/studio/project-2"), "studio");
  assert.equal(pageFromPath("/agents/studio"), "studio");
  assert.equal(pageFromPath("/agents/connect"), "externalConnect");
  assert.equal(pageFromPath("/admin/monitor"), "monitor");
  assert.equal(pageFromPath("/admin/gateway"), "gateway");
  assert.equal(pageFromPath("/admin/models"), "gateway");
  assert.equal(pageFromPath("/admin/setup"), "setup");
  assert.equal(pageFromPath("/admin/releases"), "releases");
  assert.equal(pageFromPath("/automations"), "services");
  assert.equal(projectFromPath("/", "#project-1"), "project-1");
  assert.equal(projectFromPath(chatPath("project-1")), "project-1");
  assert.equal(projectFromPath("/agents", "#project-1"), "");
  assert.equal(projectFromPath("/chat/../x"), "");
});

test("login retains both history styles and validates review anchors", async () => {
  const { loginReturnPath } = await server.ssrLoadModule("/src/review-link.ts");
  const id = "2b40fe15-8725-45cf-9576-5661de125cb5";
  assert.equal(loginReturnPath({ pathname: `/chat/${id}`, search: "?task=deliver-3", hash: "" }), `/chat/${id}?task=deliver-3`);
  assert.equal(loginReturnPath({ pathname: "/", search: "?task=deliver-3", hash: `#${id}` }), `/?task=deliver-3#${id}`);
  assert.equal(loginReturnPath({ pathname: `/chat/${id}`, search: "?task=javascript:bogus", hash: "" }), `/chat/${id}`);
  assert.equal(loginReturnPath({ pathname: `/chat/${id}`, search: "", hash: `#mutation-${id}` }), `/chat/${id}#mutation-${id}`);
  assert.equal(loginReturnPath({ pathname: `/chat/${id}`, search: "", hash: "#mutation-invalid" }), `/chat/${id}`);
  assert.equal(loginReturnPath({ pathname: "/inbox", search: "", hash: "" }), "/inbox");
});

test("workspace shortcuts respect composition and modifiers", async () => {
  const { workspaceShortcut } = await server.ssrLoadModule("/src/workspace-commands.tsx");
  assert.equal(workspaceShortcut({ key: "K", metaKey: true }), "search");
  assert.equal(workspaceShortcut({ key: "n", ctrlKey: true }), "new");
  assert.equal(workspaceShortcut({ key: "\\", metaKey: true }), "rail");
  for (const modifier of ["isComposing", "repeat", "altKey", "shiftKey", "defaultPrevented"]) {
    assert.equal(workspaceShortcut({ key: "k", metaKey: true, [modifier]: true }), undefined);
  }
  assert.equal(workspaceShortcut({ key: "k" }), undefined);
  assert.equal(workspaceShortcut({ key: "a", metaKey: true }), undefined);
});

test("command search matches titles and categories and limits rendered suggestions", async () => {
  const { matchingCommands } = await server.ssrLoadModule("/src/workspace-commands.tsx");
  const orders = { label: "采购报价", category: "历史对话" };
  const agent = { label: "采购", category: "Agent" };
  assert.deepEqual(matchingCommands([orders, agent], "报价 历史"), [orders]);
  assert.deepEqual(matchingCommands([orders, agent], "采购 Ａｇｅｎｔ"), [agent]);
  assert.deepEqual(matchingCommands([orders, agent], "差旅"), []);
  assert.equal(matchingCommands(Array(42).fill(orders), "").length, 20);
});

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
  assert.equal(pageFromPath("/agents"), "library");
  assert.equal(pageFromPath("/agents/studio/project-2"), "library");
  assert.equal(pageFromPath("/agents/connect"), "library");
  assert.equal(pageFromPath("/admin/monitor"), "admin");
  assert.equal(pageFromPath("/admin/setup"), "setup");
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
  assert.equal(loginReturnPath({ pathname: "/inbox", search: "", hash: "" }), "/inbox");
});

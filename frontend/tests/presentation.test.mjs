import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { createServer } from "vite";

let server;
before(async () => {
  server = await createServer({ configFile: false, optimizeDeps: { noDiscovery: true }, server: { middlewareMode: true, hmr: false } });
});
after(async () => { await server.close(); });

test("Markdown renders tables and highlighted code without loading embedded content", async () => {
  const { MarkdownText } = await server.ssrLoadModule("/src/markdown-text.tsx");
  const body = [
    "| 名称 | 金额 |",
    "| --- | ---: |",
    "| 采购 | 27 |",
    "",
    "```js",
    "const amount = 27;",
    "```",
    "",
    "<img src=\"https://example.test/private.png\">",
    "",
    "![图说明](https://example.test/tracking.png)",
    "",
    "[虚假链接](javascript:alert(1))",
  ].join("\n");
  const markup = renderToStaticMarkup(createElement(MarkdownText, { content: body }));
  assert.match(markup, /<table>/);
  assert.match(markup, /<td[^>]*>27<\/td>/);
  assert.match(markup, /hljs-keyword[^>]*>const/);
  assert.match(markup, /图说明/);
  assert.doesNotMatch(markup, /private\.png|tracking\.png|javascript:|<img/);
});

test("the chart provides an accessible name and leaves gaps for missing observations", async () => {
  const { Sparkline } = await server.ssrLoadModule("/src/sparkline.tsx");
  const markup = renderToStaticMarkup(createElement(Sparkline, { values: [3, 2, NaN, 1], label: "任务量" }));
  assert.match(markup, /<svg[^>]*aria-label="任务量"/);
  assert.equal((markup.match(/M\d+/g) || []).length, 2);
  assert.match(markup, /<circle/);
  assert.doesNotMatch(markup, /NaN/);
  const empty = renderToStaticMarkup(createElement(Sparkline, { values: [], label: "任务量" }));
  assert.match(empty, /任务量暂无数据/);
});

test("the selected theme persists and still applies when storage is blocked", async () => {
  const { applyTheme, readTheme, themeKey } = await server.ssrLoadModule("/src/theme.ts");
  const preferences = new Map();
  const storage = { getItem: key => preferences.get(key), setItem: (key, value) => preferences.set(key, value) };
  const root = { dataset: {} };
  assert.equal(readTheme(storage), "light");
  applyTheme("dark", root, storage);
  assert.equal(root.dataset.theme, "dark");
  assert.equal(preferences.get(themeKey), "dark");
  assert.equal(readTheme(storage), "dark");
  applyTheme("light", root, { setItem() { throw Error("storage blocked"); } });
  assert.equal(root.dataset.theme, "light");
  assert.equal(readTheme({ getItem: () => "unknown" }), "light");
});

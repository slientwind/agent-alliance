import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179", viewport: { width: 1360, height: 880 } });
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  const team = { id: "stock", spaceId: "team-space", spaceKind: "team", title: "团队库存巡检", owner: "owner", reviewer: "owner", revision: 1, stage: "draft", planVersion: 0 };
  const personal = { ...team, id: "personal", spaceId: "personal-space", spaceKind: "personal", title: "个人草稿整理" };
  let submitted;
  await page.route("**/api/**", route => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/projects" && request.method() === "POST") {
      submitted = request.postDataJSON();
      return route.fulfill({ status: 201, json: { id: team.id } });
    }
    const fixture = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "owner", name: "工作区成员", role: "member", enabled: true },
      "/api/teams": [{ id: "stock-team", name: "库存团队", joined: true, default: true }],
      "/api/spaces": [
        { id: "personal-space", kind: "personal", name: "个人空间", role: "space_admin" },
        { id: "team-space", kind: "team", name: "库存团队", teamId: "stock-team", role: "member" },
        { id: "commons", kind: "commons", name: "全企公共底座", role: "member" },
      ],
      "/api/projects": [personal, team],
      "/api/projects/stock": { ...team, canReview: true, canManage: true, artifacts: [], messages: [], sources: [], nodes: [], participants: [] },
    };
    return route.fulfill({ json: fixture[path] ?? [] });
  });
  await page.goto("/chat");
  const picker = page.getByLabel("当前空间");
  const nav = page.getByRole("navigation", { name: "最近对话" });
  const prompt = page.getByRole("textbox", { name: "消息" });
  await nav.getByRole("button", { name: personal.title }).waitFor();
  assert.equal(await picker.inputValue(), "personal-space");
  assert.equal(await nav.getByRole("button", { name: team.title }).count(), 0);
  await prompt.fill("个人工作草稿");
  await picker.selectOption("team-space");
  await nav.getByRole("button", { name: team.title }).waitFor();
  assert.equal(await nav.getByRole("button", { name: personal.title }).count(), 0);
  assert.equal(await prompt.inputValue(), "");
  await page.getByText("库存团队 · 使用当前空间知识与全企公共底座").waitFor();
  await prompt.fill("按团队知识核对库存");
  await picker.selectOption("personal-space");
  assert.equal(await prompt.inputValue(), "个人工作草稿");
  await picker.selectOption("team-space");
  assert.equal(await prompt.inputValue(), "按团队知识核对库存");
  await page.getByRole("button", { name: "发送消息" }).click();
  await page.waitForURL("**/chat/stock");
  assert.deepEqual(submitted, { goal: "按团队知识核对库存", team: "stock-team", space: "team-space" });
  assert.deepEqual(errors, []);
} finally {
  await browser.close();
}

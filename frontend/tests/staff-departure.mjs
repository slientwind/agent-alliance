import assert from "node:assert/strict";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const page = await browser.newPage({
    baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15179",
    viewport: { width: 390, height: 844 },
  });
  const admin = { id: "admin", role: "admin", enabled: true, name: "管理员", email: "admin@example.test" };
  const employee = { id: "leaving", role: "member", enabled: true, name: "李明", email: "li@example.test", accountRevision: "3" };
  const candidate = { id: "next", role: "member", enabled: true, name: "林青", email: "lin@example.test" };
  let submission;
  await page.route("**/api/**", route => {
    const request = route.request();
    const url = new URL(request.url());
    switch (url.pathname) {
      case "/api/instance": return route.fulfill({ json: { initialized: true, name: "企业" } });
      case "/api/me": return route.fulfill({ json: admin });
      case "/api/admin/members": return route.fulfill({ json: [admin, employee, candidate] });
      case "/api/admin/members/leaving/responsibilities":
        return route.fulfill({ json: { finalProjects: 2, reviewVersions: 3, delegations: 0 } });
      case "/api/admin/members/leaving/departure-handoff":
        return route.fulfill({ json: { projects: 2, agents: 1, drafts: 1, rules: 1, automations: 1, successors: [candidate] } });
      case "/api/admin/members/leaving":
        submission = request.postDataJSON();
        employee.enabled = false;
        employee.departed = true;
        employee.accountRevision = "4";
        return route.fulfill({ status: 204 });
      default: return route.fulfill({ json: [] });
    }
  });
  await page.goto("/admin");
  await page.getByRole("button", { name: "用户与监控" }).click();
  await page.getByRole("button", { name: /李明 · li@example.test/ }).click();
  const profile = page.getByRole("region", { name: "成员资料" });
  await profile.getByLabel("变更类型").selectOption("depart");
  await profile.getByText("2 个项目 · 2 份 Agent 资产 · 1 条团队规则 · 1 项定时任务").waitFor();
  await profile.getByRole("combobox", { name: "接手人", exact: true }).selectOption("next");
  await profile.getByLabel("变更原因").fill("移交当前职责");
  await profile.getByLabel("管理员当前密码").fill("fixture-password");
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
  await profile.getByRole("button", { name: "确认变更" }).click();
  await profile.waitFor({ state: "detached" });
  assert.deepEqual(submission, { expected: "3", role: "member", enabled: false, employment: "depart", successor: "next",
    reason: "移交当前职责", password: "fixture-password" });
} finally {
  await browser.close();
}

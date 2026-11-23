import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";

const members = ["甲", "乙"].map((name, i) => ({
  id: "person-" + i,
  name,
  email: i + "@sample.invalid",
  enabled: true,
  role: "member",
  registration: "invitation",
  accountRevision: "1",
  teams: [],
}));
const origin = process.env.SMOKE_ORIGIN || "http://127.0.0.1:15173";
const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } });
  let blocked, release;
  const first = new Promise((resolve) => {
    blocked = resolve;
  });
  const resume = new Promise((resolve) => {
    release = resolve;
  });
  let isAdmin = true;
  const actor = { id: "viewer", name: "管理员", enabled: true };
  await page.route("**/api/**", async (route) => {
    const pathname = new URL(route.request().url()).pathname;
    const reply = (json) => route.fulfill({ json });
    if (pathname === "/api/instance")
      return reply({ initialized: true, name: "合成企业" });
    if (pathname === "/api/me")
      return reply({ ...actor, role: isAdmin ? "admin" : "member" });
    if (pathname === "/api/admin/members")
      return isAdmin
        ? reply(members)
        : route.fulfill({ status: 403, json: { error: "管理员权限已变更" } });
    if (pathname === "/api/me/account-notices")
      return reply({
        member: actor.id,
        role: isAdmin ? "admin" : "member",
        count: 0,
        rows: [],
      });
    if (pathname.endsWith("/responsibilities"))
      return reply({ reviewVersions: 0, finalProjects: 0, delegations: 0 });
    const m = members.find(
      (m) => pathname === "/api/admin/members/" + m.id + "/history",
    );
    if (m) {
      if (m === members[0]) {
        blocked();
        await resume;
      }
      return reply({
        member: m.id,
        next: "",
        rows: [
          {
            id: "1",
            at: "2026-05-01T00:00:00Z",
            action: "member.change",
            actor: "负责人",
            oldRole: "member",
            role: "admin",
            reason: m.name + "的账号变更",
          },
        ],
      });
    }
    return reply([]);
  });
  await page.goto(origin);
  const workspace = page.getByRole("button", { name: "我的工作区" });
  await workspace.click();
  await page.getByRole("button", { name: "企业管理", exact: true }).click();
  await page.getByRole("button", { name: "用户与监控", exact: true }).click();
  const directory = page.getByRole("region", { name: "用户管理" });
  const profile = directory.getByRole("region", { name: "成员资料" });
  await directory.getByRole("button", { name: /甲 ·/ }).click();
  await profile.getByText("账号变更记录", { exact: true }).click();
  await first;
  await directory.getByRole("button", { name: /乙 ·/ }).click();
  await profile.getByText("账号变更记录", { exact: true }).click();
  await profile.getByText("乙的账号变更").waitFor();
  const delivered = page.waitForResponse((r) =>
    r.url().endsWith("/person-0/history"),
  );
  release();
  await (await delivered).finished();
  assert.equal(await profile.getByText("甲的账号变更").count(), 0);
  isAdmin = false;
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await directory.waitFor({ state: "detached" });
  await workspace.click();
  await page.getByText(/管理员 · 合成企业 · 普通用户/).waitFor();
  assert.equal(
    await page.getByRole("button", { name: "企业管理", exact: true }).count(),
    0,
  );
  assert.equal(await page.getByText("乙的账号变更").count(), 0);
  await page.getByRole("button", { name: "关闭", exact: true }).click();
  const draft = page.getByRole("textbox", { name: "消息", exact: true });
  await draft.fill("上一个成员的私有草稿");
  actor.id = "second-viewer";
  actor.name = "新成员";
  const navigation = page.waitForEvent(
    "framenavigated",
    (frame) => frame === page.mainFrame(),
  );
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await navigation;
  await draft.waitFor();
  assert.equal(await draft.inputValue(), "");
  await workspace.click();
  await page.getByText("新成员 · 合成企业 · 普通用户").waitFor();
  if (process.env.VISUAL_EVIDENCE)
    await writeFile(
      path.join(process.env.VISUAL_EVIDENCE, "member-profile-switch.json"),
      JSON.stringify(
        {
          fixture: "synthetic delayed HTTP responses",
          staleHistoryVisible: false,
          directoryVisibleAfterDemotion: false,
          memberRoleReloaded: true,
          previousMemberDraftVisible: false,
        },
        null,
        2,
      ),
    );
  console.log(
    "Mobile member history: late previous response isolated, admin directory cleared after revocation",
  );
} finally {
  await browser.close();
}

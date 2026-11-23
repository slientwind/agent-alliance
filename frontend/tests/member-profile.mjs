import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import path from "node:path";

export async function exerciseMemberProfile({
  call,
  admin,
  manager,
  author,
  writer,
  team,
  payerTeam,
}) {
  const base = "admin/members/" + writer.id;
  const history = base + "/history";
  const list = await call(admin, "admin/members");
  const original = list.find((x) => x.id === writer.id);
  assert.equal(original.registration, "invitation");
  assert.equal(original.department, "售后业务部");
  assert.ok(original.teams.some((t) => t.id === team));
  assert.ok(list.some((x) => x.registration === "initial"));
  await call(manager, history, "GET", undefined, 403);
  const anon = await admin
    .browser()
    .newContext({ baseURL: process.env.SMOKE_ORIGIN });
  try {
    await call(anon, history, "GET", undefined, 401);
  } finally {
    await anon.close();
  }
  await call(admin, history + "?before=0", "GET", undefined, 422);
  await call(admin, history + "?before=2&before=1", "GET", undefined, 422);
  const app = (await call(admin, "admin/usage")).find(
    (x) => x.kind === "application",
  );
  assert.ok(app && !list.some((x) => x.id === app.id));
  await call(
    admin,
    "admin/members/" + app.id + "/history",
    "GET",
    undefined,
    404,
  );
  const { rows } = await call(admin, history);
  assert.ok(
    rows.some(
      (r) =>
        r.department === "售后业务部" && r.previousDepartment === "培训业务部",
    ),
  );
  assert.ok(rows.some((r) => r.team === "例会协作组" && r.joined));
  assert.ok(rows.some((r) => r.action === "member.enroll"));
  const report = async (teamID) => {
    const result = await call(
      admin,
      "admin/usage?view=dashboard&days=7&member=" +
        writer.id +
        "&team=" +
        teamID,
    );
    assert.equal(result.team, teamID);
    assert.equal(result.enterpriseExport, null);
    assert.equal(result.items.length, 1);
    const { runs, attempts, deliveredEditions } = result.items[0];
    return { runs, attempts, deliveredEditions };
  };
  const projectTotals = await report(team),
    payerTotals = await report(payerTeam);
  assert.deepEqual(projectTotals, {
    runs: 1,
    attempts: 0,
    deliveredEditions: 1,
  });
  assert.deepEqual(payerTotals, { runs: 0, attempts: 1, deliveredEditions: 0 });

  const page = await admin.newPage();
  try {
    await page.goto("/");
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "企业管理", exact: true }).click();
    await page.getByRole("button", { name: "用户与监控", exact: true }).click();
    const directory = page.getByRole("region", { name: "用户管理" });
    const monitor = page.getByRole("region", { name: "运行监控" });
    const profile = directory.getByRole("region", { name: "成员资料" });
    await monitor.getByLabel("统计周期").selectOption("7");
    await directory.getByLabel("查找成员").fill(writer.name);
    await directory.getByLabel("成员所属团队").selectOption(team);
    await directory.getByLabel("账号角色").selectOption("admin");
    await directory.getByText("没有符合条件的成员").waitFor();
    await directory.getByLabel("账号角色").selectOption("member");
    await directory.getByLabel("账号状态").selectOption("enabled");
    const select = directory.getByRole("button", {
      name: new RegExp(writer.name + " ·"),
    });
    await select.click();
    await profile.getByText(/邀请注册/).waitFor();
    await profile.getByRole("button", { name: "查看此成员用量" }).click();
    assert.equal(
      await monitor.getByLabel("发起成员或应用").inputValue(),
      writer.id,
    );
    assert.equal(await monitor.getByLabel("统计周期").inputValue(), "7");
    await monitor.getByText(/发起运行 1 ·/).waitFor();
    await monitor.getByLabel("用量统计团队").selectOption(payerTeam);
    await monitor.getByText(/调用 1 次/).waitFor();
    await monitor.getByRole("button", { name: "查看成员资料" }).click();
    await profile.getByText("账号变更记录", { exact: true }).click();
    const record = profile.getByRole("region", { name: "账号记录列表" });
    await record.getByText("部门 · 培训业务部 → 售后业务部").waitFor();
    await call(
      admin,
      base,
      "PATCH",
      {
        role: "admin",
        enabled: true,
        password: process.env.BROWSER_PASSWORD,
        reason: "核验角色变更",
        expected: original.accountRevision,
      },
      204,
    );
    await call(
      admin,
      base,
      "PATCH",
      {
        role: "member",
        enabled: true,
        password: process.env.BROWSER_PASSWORD,
        reason: "旧资料",
        expected: original.accountRevision,
      },
      409,
    );
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await profile.getByRole("button", { name: "按最新资料修改" }).click();
    await profile.getByLabel("成员角色").selectOption("member");
    await profile.getByLabel("变更原因").fill("完成核验，恢复普通用户");
    await profile
      .getByLabel("管理员当前密码")
      .fill(process.env.BROWSER_PASSWORD);
    await profile.getByRole("button", { name: "确认变更" }).click();
    await profile.waitFor({ state: "detached" });
    await select.click();
    await profile.getByText("账号变更记录", { exact: true }).click();
    await record.getByText("管理员（启用） → 普通用户（启用）").waitFor();
    await record.getByText("完成核验，恢复普通用户").waitFor();
    const latest = (await call(admin, history)).rows[0];
    const older = await call(admin, history + "?before=" + latest.id);
    assert.ok(
      older.rows.length &&
        older.rows.every((x) => BigInt(x.id) < BigInt(latest.id)),
    );
    await page.setViewportSize({ width: 390, height: 844 });
    await record.scrollIntoViewIfNeeded();
    await page.screenshot({
      path: path.join(process.env.VISUAL_EVIDENCE, "member-profile-mobile.png"),
    });
    await call(manager, base + "/responsibilities", "GET", undefined, 403);
    const service = "services/" + process.env.PROFILE_SERVICE;
    const application = (
      await call(author, service + "/access")
    ).applications.find((a) => a.state === "active");
    const delegated = await call(
      author,
      service + "/delegate",
      "POST",
      { app: application.id, hours: 1 },
      201,
    );
    const responsibilities = await call(admin, base + "/responsibilities");
    assert.equal(responsibilities.delegations, 1);
    const pending = await call(
      author,
      "projects",
      "POST",
      { goal: "成员交接前的合成项目" },
      201,
    );
    const updated = await call(admin, base + "/responsibilities");
    assert.equal(updated.finalProjects, responsibilities.finalProjects + 1);
    assert.ok(!JSON.stringify(updated).includes(pending.id));
    const notices = "me/account-notices";
    const before = await call(author, notices);
    assert.equal(before.count, 2);
    assert.deepEqual(
      before.rows.map((n) => n.role),
      ["admin", "member"],
    );
    await call(
      manager,
      notices + "/" + before.rows[0].id + "/read",
      "PUT",
      {},
      404,
    );
    const ownerPage = await author.newPage();
    try {
      await ownerPage.goto("/");
      await ownerPage.getByRole("button", { name: "我的工作区" }).click();
      await ownerPage.getByText(/账号通知.*2 条未读/).click();
      await ownerPage.getByText("完成核验，恢复普通用户").waitFor();
      await ownerPage.getByRole("button", { name: "标记已读" }).first().click();
      await ownerPage.getByText(/账号通知.*1 条未读/).waitFor();
    } finally {
      await ownerPage.close();
    }
    await call(
      author,
      notices + "/" + before.rows[0].id + "/read",
      "PUT",
      {},
      204,
    );
    assert.equal((await call(author, notices)).count, 1);
    const changeState = (enabled) =>
      call(
        admin,
        base,
        "PATCH",
        {
          role: "member",
          enabled,
          password: process.env.BROWSER_PASSWORD,
          reason: enabled ? "恢复登录" : "暂时停用账号",
        },
        204,
      );
    await changeState(false);
    await call(author, notices, "GET", undefined, 401);
    await changeState(true);
    await call(author, "login", "POST", {
      email: original.email,
      password: process.env.BROWSER_PASSWORD,
    });
    const after = await call(author, notices);
    assert.equal(after.count, 3);
    assert.deepEqual(
      after.rows.slice(-2).map((row) => row.enabled),
      [false, true],
    );
    assert.equal(
      (await call(author, service + "/access")).delegations.find(
        (d) => d.id === delegated.id,
      ).state,
      "revoked",
    );
    assert.equal(
      (await call(admin, base + "/responsibilities")).delegations,
      0,
    );
    await changeState(true);
    assert.equal((await call(author, notices)).count, 3);
    await writeFile(
      path.join(process.env.VISUAL_EVIDENCE, "member-profile.json"),
      JSON.stringify(
        {
          unreadAfterReturn: after.count,
          handoffProjectCountChange:
            updated.finalProjects - responsibilities.finalProjects,
          previousDelegationAfterReturn: "revoked",
          projectTotals,
          payerTotals,
          registration: original.registration,
          department: original.department,
          staleUpdate: 409,
          nonAdmin: 403,
          anonymous: 401,
          applicationProfile: 404,
          latest: { oldRole: latest.oldRole, role: latest.role },
          historyActions: [...new Set(rows.map((r) => r.action))],
        },
        null,
        2,
      ),
    );
  } finally {
    await page.close();
  }
}

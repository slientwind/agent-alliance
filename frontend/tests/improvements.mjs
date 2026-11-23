import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
export async function checkImprovements(browser, workspace) {
  const origin = process.env.SMOKE_ORIGIN;
  const member = await browser.newContext({ baseURL: origin });
  const bystander = await browser.newContext({ baseURL: origin });
  const user = workspace.request,
    maintainer = member.request;
  async function value(response, status) {
    assert.equal(response.status(), status, await response.text());
    return status === 204 ? null : response.json();
  }
  const post = (session, path, data, status, headers) =>
    session
      .post("/api/" + path, { data, headers })
      .then((r) => value(r, status));
  const get = (session, path) =>
    session.get("/api/" + path).then((r) => value(r, 200));
  async function library(page) {
    await page.goto("/");
    await page.getByRole("button", { name: "我的工作区" }).click();
    await page.getByRole("button", { name: "团队能力库", exact: true }).click();
    return page.getByRole("dialog");
  }
  try {
    await post(
      maintainer,
      "login",
      {
        email: "participant@sample.invalid",
        password: process.env.BROWSER_FINANCE_PASSWORD,
      },
      200,
    );
    const invitation = await post(user, "admin/invitations", {}, 201);
    await post(
      bystander.request,
      "register",
      {
        email: "improvement-observer@sample.invalid",
        name: "Observer",
        password: crypto.randomUUID(),
        token: invitation.token,
      },
      201,
    );
    const model = (await get(maintainer, "resources")).find(
      (m) => m.name === "Stub TLS",
    );
    const config = {
      name: "提案整理助手",
      instructions: "整理提案材料",
      resource: model.id,
      resourceRevision: model.revision,
    };
    const agent = (await post(maintainer, "agents", config, 201)).id;
    const root = "agents/" + agent,
      endpoint = root + "/improvements";
    async function validate(revision) {
      const proof = await post(
        maintainer,
        root + "/validate",
        {
          revision,
          input:
            revision === 2
              ? "IMPROVEMENT_ACCEPTANCE_PROBE 李文跟进预算"
              : "synthetic",
          expected:
            revision === 2 ? "行动事项：跟进预算；负责人：李文" : "RECEIPT",
          authorizeOneCall: true,
        },
        200,
      );
      assert.equal(proof.status, "passed");
    }
    await validate(1);
    await post(user, root + "/publish", { revision: 1 }, 204);
    const creator = await workspace.newPage(),
      reviewer = await member.newPage();
    try {
      const requesterPage = await library(creator);
      const form = requesterPage
        .getByRole("heading", { name: "描述一个新 Agent" })
        .locator("..");
      await form.getByLabel("名称", { exact: true }).fill(config.name);
      await form.locator(".similar-agents > summary").click();
      const enhancement = form
        .getByText("向维护者提出增强需求", { exact: true })
        .locator("..");
      await enhancement.locator("summary").click();
      const description = "在提案摘要后补一份行动清单",
        acceptance = "清单列出事项和负责人";
      await enhancement.getByLabel("需要增强什么").fill(description);
      await enhancement.getByLabel("如何验收增强结果").fill(acceptance);
      const submitted = creator.waitForRequest(
        (r) => r.method() === "POST" && r.url().endsWith("/improvements"),
      );
      await enhancement.getByRole("button", { name: "提交增强需求" }).click();
      const key = (await submitted).headers()["idempotency-key"];
      await enhancement.getByText(/需求已提交/).waitFor();
      const payload = { base: 1, description, acceptance };
      const header = { "Idempotency-Key": key };
      const { id } = await post(user, endpoint, payload, 200, header);
      await post(
        user,
        endpoint,
        { ...payload, acceptance: "不同要求" },
        409,
        header,
      );
      const decision = "agent-improvements/" + id + "/decisions";
      assert.equal(
        (await get(user, "agent-improvements")).filter((i) => i.id === id)
          .length,
        1,
      );
      assert.ok(
        !(await get(bystander.request, "agent-improvements")).some(
          (i) => i.id === id,
        ),
      );
      await post(
        bystander.request,
        decision,
        { expected: 1, action: "accept", reason: "旁观者尝试" },
        404,
      );
      await post(
        user,
        decision,
        { expected: 1, action: "accept", reason: "管理员尝试越过维护职责" },
        403,
      );
      const reviewPage = await library(reviewer);
      await reviewPage.getByText(/^能力增强需求/).click();
      const record = reviewPage
        .locator("article")
        .filter({ hasText: description });
      await record.getByLabel("处理依据").fill("在新修订增加责任人栏");
      await record.getByRole("button", { name: "受理需求" }).click();
      await record.getByText("已受理", { exact: true }).first().waitFor();
      await post(
        user,
        decision,
        { expected: 1, action: "withdraw", reason: "过时页面" },
        409,
      );
      await post(
        maintainer,
        decision,
        {
          expected: 2,
          action: "resolve",
          publication: 1,
          reason: "旧版不可交差",
        },
        409,
      );
      await post(
        maintainer,
        "agents",
        {
          ...config,
          id: agent,
          expected: 1,
          instructions: "提案摘要后附行动清单和负责人",
        },
        201,
      );
      await post(
        maintainer,
        decision,
        {
          expected: 2,
          action: "resolve",
          publication: 2,
          reason: "草稿不能交差",
        },
        409,
      );
      await validate(2);
      await post(maintainer, root + "/publish", { revision: 2 }, 403);
      await post(user, root + "/publish", { revision: 2 }, 204);
      await reviewPage.getByRole("button", { name: "刷新处理记录" }).click();
      await record.getByLabel("处理依据").fill("逐项检查了行动清单和负责人");
      await record
        .getByRole("button", { name: "关联已发布 v2 并完成" })
        .click();
      await record
        .getByText("已关联发布版本", { exact: true })
        .first()
        .waitFor();
      await requesterPage.getByText(/^能力增强需求/).click();
      const receipt = requesterPage
        .locator("article")
        .filter({ hasText: description });
      await receipt
        .getByText("逐项检查了行动清单和负责人", { exact: false })
        .waitFor();
      await receipt.screenshot({
        path: process.env.VISUAL_EVIDENCE + "/improvement-receipt.png",
      });
      const final = (await get(user, "agent-improvements")).find(
        (i) => i.id === id,
      );
      assert.equal(final.base, 1);
      assert.equal(final.result, 2);
      assert.deepEqual(
        final.decisions.map((d) => d.state),
        ["accepted", "resolved"],
      );
      for (const action of ["withdraw", "decline"]) {
        const extra = await post(user, endpoint, { ...payload, base: 2 }, 200, {
          "Idempotency-Key": crypto.randomUUID(),
        });
        await post(
          action === "withdraw" ? user : maintainer,
          "agent-improvements/" + extra.id + "/decisions",
          { action, expected: 1, reason: "合成边界走查" },
          204,
        );
      }
      const concurrentHeaders = { "Idempotency-Key": crypto.randomUUID() };
      const pair = await Promise.all([
        post(user, endpoint, { ...payload, base: 2 }, 200, concurrentHeaders),
        post(user, endpoint, { ...payload, base: 2 }, 200, concurrentHeaders),
      ]);
      assert.equal(pair[0].id, pair[1].id);
      const responses = await Promise.all(
        ["accept", "decline"].map((action) =>
          maintainer.post(
            "/api/agent-improvements/" + pair[0].id + "/decisions",
            { data: { expected: 1, reason: "并发审批验证", action } },
          ),
        ),
      );
      assert.deepEqual(responses.map((r) => r.status()).sort(), [204, 409]);
      const tracePath = root + "/maintenance";
      const observerEmail = "improvement-observer@sample.invalid",
        observer = bystander.request;
      const team = (await get(user, "teams")).find((t) => t.default).id;
      const observerID = (await get(observer, "me")).id;
      async function membership(joined) {
        await value(
          await user.put(
            "/api/admin/teams/" + team + "/members/" + observerID,
            { data: { joined, reason: "合成交接验证" } },
          ),
          204,
        );
      }
      async function change(
        session,
        action,
        expected,
        status,
        email = observerEmail,
      ) {
        return post(
          session,
          tracePath,
          { action, expected, email, reason: "合成维护交接" },
          status,
        );
      }
      const before = await get(user, tracePath);
      assert.equal(before.canNominate, false);
      await change(user, "nominate", before.version, 403);
      await change(maintainer, "nominate", before.version, 204);
      const offer = await get(observer, tracePath);
      assert.equal(offer.canAccept, true);
      assert.ok(
        !(await get(observer, "agent-improvements")).some(
          (row) => row.id === id,
        ),
      );
      await membership(false);
      await membership(true);
      const cancelled = await get(observer, tracePath);
      assert.equal(cancelled.pending, false);
      await change(observer, "accept", offer.version, 409);
      const panel = reviewPage
        .locator("#library-agent-" + agent)
        .getByText("维护者与交接", { exact: true })
        .locator("..");
      await panel.locator("summary").click();
      await panel.getByLabel("接任人团队邮箱").fill(observerEmail);
      await panel.getByLabel("交接依据").fill("交接提案整理助手的日常维护");
      await panel.getByRole("button", { name: "提名接任人" }).click();
      await panel.getByText(/等待 Observer 接受/).waitFor();
      const nextPage = await bystander.newPage();
      try {
        const lib = await library(nextPage);
        const incoming = lib
          .locator("#library-agent-" + agent)
          .getByText("待接受维护职责", { exact: true })
          .locator("..");
        await incoming.locator("summary").click();
        await incoming
          .getByLabel("交接依据")
          .fill("已核对职责和待办，接受维护");
        await incoming.getByRole("button", { name: "接受维护职责" }).click();
        await lib
          .locator("#library-agent-" + agent)
          .getByText("Observer", { exact: true })
          .waitFor();
        await lib.getByText(/^能力增强需求/).click();
        await lib
          .locator("article")
          .filter({ hasText: "逐项检查了行动清单和负责人" })
          .waitFor();
      } finally {
        await nextPage.close();
      }
      const after = await get(observer, tracePath);
      assert.equal(after.owner, "Observer");
      assert.equal(after.available, true);
      assert.ok(
        !(await get(maintainer, "agent-improvements")).some(
          (row) => row.id === id,
        ),
      );
      await membership(false);
      await membership(true);
      const vacant = await get(user, tracePath);
      assert.equal(vacant.available, false);
      assert.ok(
        !(await get(observer, "agent-improvements")).some(
          (row) => row.id === id,
        ),
      );
      await change(
        user,
        "nominate",
        vacant.version,
        204,
        "participant@sample.invalid",
      );
      const recovery = await get(maintainer, tracePath);
      await change(user, "accept", recovery.version, 403);
      await change(maintainer, "accept", recovery.version, 204);
      assert.equal((await get(maintainer, tracePath)).owner, "Participant");
      await writeFile(
        process.env.VISUAL_EVIDENCE + "/improvements.json",
        JSON.stringify(
          {
            source:
              "synthetic business request; real API, PostgreSQL and Chromium",
            base: final.base,
            resolution: final.result,
            states: final.decisions.map((d) => d.state),
            replayRows: 1,
            adminWithoutMaintenance: 403,
            unrelatedTeamMember: 404,
            staleWithdrawal: 409,
            unpublishedRevision: 409,
            concurrentDecisions: [204, 409],
            handover: "explicit acceptance",
            revokedOffer: "cancelled",
            rejoinedOwner: "vacant",
            recoveredOwner: "Participant",
          },
          null,
          2,
        ),
      );
    } finally {
      await creator.close();
      await reviewer.close();
    }
  } finally {
    await member.close();
    await bystander.close();
  }
}

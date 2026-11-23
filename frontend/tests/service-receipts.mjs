import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkServiceReceipts(member, agent, real, directory) {
  const admin = await member
    .context()
    .browser()
    .newPage({ baseURL: process.env.SMOKE_ORIGIN });
  const mode = real ? "local" : "fixture";
  const request =
    "住宿结算实付比单晚住宿报销标准超出多少元？列出差额和原文依据。";
  const standard = "单晚住宿报销标准为 450 元。";
  const excerpt = "住宿结算实付 528.50 元。";
  const original = excerpt + "\n内部编号 SVC-A7。";
  const attachment = (name, text) => ({
    name,
    data: Buffer.from(text).toString("base64"),
  });
  const post = async (client, path, data, key, headers = {}) => {
    const r = await client.post(path, {
      data,
      headers: { ...headers, ...(key ? { "Idempotency-Key": key } : {}) },
    });
    assert.ok(r.ok(), path + " " + r.status() + " " + (await r.text()));
    return r.status() === 204 ? null : r.json();
  };
  const get = async (client, path, headers = {}) => {
    const r = await client.get(path, { headers });
    assert.ok(r.ok(), await r.text());
    return r.json();
  };
  const until = async (fn) => {
    const end = Date.now() + 80000;
    while (Date.now() < end) {
      const value = await fn();
      if (value) return value;
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
    throw new Error("核算服务状态未按期变化");
  };
  try {
    await post(admin.request, "/api/login", {
      email: "review@sample.invalid",
      password: process.env.BROWSER_PASSWORD,
    });
    const project = (
      await post(admin.request, "/api/projects", { goal: request })
    ).id;
    const base = "/api/projects/" + project;
    const source = await admin.request.post(
      base + "/sources?name=template.txt",
      { data: Buffer.from(standard + "\n" + excerpt) },
    );
    assert.ok(source.ok(), await source.text());
    const pin = (await source.json()).id;
    await post(admin.request, base + "/plan", {
      expected: (await get(admin.request, base)).revision,
      brief: request,
      agents: [agent],
      agentRevisions: [1],
      sourceLookup: true,
      calculationSteps: [1],
      sources: [{ id: pin, version: 1 }],
    });
    await post(
      admin.request,
      base + "/runs",
      {
        expected: (await get(admin.request, base)).revision,
        authorizeCalls: 2,
      },
      crypto.randomUUID(),
    );
    const svc = (
      await post(admin.request, "/api/services", {
        project,
        name: "差旅结算核算 " + mode,
        instructions: request,
      })
    ).id;
    const desc = "/api/services/" + svc;
    const account = await get(member.request, "/api/me");
    const readProject = (id) => get(admin.request, "/api/projects/" + id);
    async function deliver(
      id,
      stem,
      client = admin.request,
      downloadPath = "/api/projects/" + id + "/delivery/package",
      headers = {},
    ) {
      const reviewed = await until(async () => {
        const p = await readProject(id);
        return p.stage === "review" && p;
      });
      assert.equal(reviewed.costSummary.calls, 2);
      assert.equal(reviewed.planVersion, 2);
      const answer = reviewed.artifacts.find((a) => a.ordinal === 2);
      assert.match(answer.body, /差额为 78\.50 元/);
      assert.ok(
        answer.body.includes(excerpt) && !answer.body.includes("SVC-A7"),
      );
      await admin.goto("/#" + id);
      for (const artifact of reviewed.artifacts)
        await admin.getByRole("button", { name: "审核此版本" }).first().click();
      await admin.getByRole("button", { name: "确认并固定以上版本" }).click();
      await admin.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
      const archive = await client.get(downloadPath, { headers });
      assert.ok(archive.ok(), await archive.statusText());
      await writeFile(directory + "/" + stem + ".zip", await archive.body());
      return {
        run: reviewed.run.id,
        answer: answer.body,
        finalCalls: 2,
        planVersion: 2,
        reviewer: reviewed.reviewer,
        requester: reviewed.owner,
      };
    }
    await member.goto("/");
    const projectsBefore = (await get(member.request, "/api/projects")).length;
    const composer = member.getByRole("textbox", { name: "消息", exact: true });
    await composer.fill(request);
    await member.getByRole("button", { name: "我的工作区" }).click();
    await member
      .getByRole("button", { name: "团队能力库", exact: true })
      .click();
    await member
      .locator("#library-agent-" + agent)
      .getByRole("button", { name: /开始对话/ })
      .click();
    assert.equal(await composer.inputValue(), request);
    await member
      .locator(".picked-agent")
      .getByText(/承担费用/)
      .waitFor();
    if (!real) {
      const single = await post(admin.request, "/api/services", {
        agent,
        name: "核对文字",
        instructions: "核对原文",
      });
      const invalid = await member.request.post(
        "/api/services/" + single.id + "/chat",
        {
          headers: { "Idempotency-Key": crypto.randomUUID() },
          data: {
            version: 1,
            input: request,
            authorizeCalls: 1,
            awaitCalculationReceipt: true,
          },
        },
      );
      assert.equal(invalid.status(), 422, await invalid.text());
    }
    assert.equal(
      (await get(member.request, "/api/projects")).length,
      projectsBefore,
    );
    await member.goto("/?service=" + svc);
    await member.getByLabel("向服务提出任务").fill(request);
    await member.getByLabel("附资料").setInputFiles({
      name: "差旅标准.txt",
      mimeType: "text/plain",
      buffer: Buffer.from(standard),
    });
    const toggle = member.getByLabel("金额不足时在原任务等待我补交原件");
    assert.ok(await toggle.isChecked());
    const consent = member.getByLabel(/授权本次最多/);
    await consent.check();
    await toggle.uncheck();
    assert.ok(!(await consent.isChecked()));
    await toggle.check();
    await consent.check();
    const launched = member.waitForResponse(
      (r) => r.url().endsWith("/chat") && r.request().method() === "POST",
    );
    await member.getByRole("button", { name: "确认并启动对话" }).click();
    const call = await launched;
    assert.equal(call.status(), 202);
    await member.waitForURL(/\/#[-0-9a-f]{36}$/);
    const acceptedAgain = await member.request.post(desc + "/chat", {
      headers: {
        "Idempotency-Key": call.request().headers()["idempotency-key"],
      },
      data: call.request().postDataJSON(),
    });
    assert.equal(acceptedAgain.status(), 202);
    const web = await acceptedAgain.json();
    assert.equal(new URL(member.url()).hash.slice(1), web.project);
    const card = member
      .getByRole("heading", { name: "补充核算原件" })
      .locator("..");
    await card.waitFor({ timeout: 80000 });
    await member.reload();
    await card.waitFor();
    await card.screenshot({
      path: directory + "/service-receipt-" + mode + "-waiting.png",
    });
    const waiting = await readProject(web.project);
    assert.equal(waiting.costSummary.calls, 1);
    assert.equal(waiting.run.id, web.id);
    const replayHeaders = {
      "Idempotency-Key": call.request().headers()["idempotency-key"],
    };
    const changed = await member.request.post(desc + "/chat", {
      headers: replayHeaders,
      data: {
        ...call.request().postDataJSON(),
        awaitCalculationReceipt: false,
      },
    });
    assert.equal(changed.status(), 409);
    if (real) {
      await card.getByLabel("补充原件", { exact: true }).setInputFiles({
        name: "结算说明.txt",
        mimeType: "text/plain",
        buffer: Buffer.from(original),
      });
    } else {
      const file = await member.request.post(
        "/api/projects/" +
          web.project +
          "/sources?name=" +
          encodeURIComponent("结算说明.txt"),
        { data: Buffer.from(original) },
      );
      assert.ok(file.ok(), await file.text());
      const sourceID = (await file.json()).id;
      const passages = "/api/runs/" + web.id + "/receipt-passages?step=2&version=1&source=" + sourceID;
      assert.equal((await member.request.get(passages)).status(), 200);
      assert.equal((await admin.request.get(passages)).status(), 404);
      const membership = async (joined) => {
        const result = await admin.request.put(
          "/api/admin/teams/" + waiting.team + "/members/" + account.id,
          { data: { joined, reason: "验证补件期间的服务访问" } },
        );
        assert.ok(result.ok(), await result.text());
      };
      await membership(false);
      try {
        assert.equal((await member.request.get(passages)).status(), 404);
        const denied = await member.request.post(
          "/api/runs/" + web.id + "/calculation-receipts",
          {
            headers: { "Idempotency-Key": crypto.randomUUID() },
            data: {
              expected: waiting.run.controlRevision,
              step: 2,
              source: sourceID,
              version: 1,
              excerpt,
            },
          },
        );
        assert.equal(denied.status(), 403, await denied.text());
        assert.equal((await readProject(web.project)).costSummary.calls, 1);
      } finally {
        await membership(true);
      }
      await member.reload();
      await card.getByLabel("补件来源").selectOption(sourceID);
    }
    await card.getByLabel(/含金额的原句/).fill(excerpt);
    await card.getByRole("button", { name: "确认补件并继续核算" }).click();
    const webResult = await deliver(
      web.project,
      "service-receipt-" + mode + "-web",
    );
    assert.equal(webResult.run, web.id);
    assert.equal(webResult.requester, account.id);
    assert.notEqual(webResult.reviewer, account.id);
    const app = await post(admin.request, desc + "/applications", {
      name: "合成核算应用",
      days: 1,
    });
    const d = await post(member.request, desc + "/delegate", {
      app: app.id,
      hours: 1,
    });
    const appHeaders = { Authorization: "Bearer " + app.token };
    const headers = { ...appHeaders, "X-Alliance-Delegation": d.token };
    const versioned = "/api/v1/services/" + svc + "/runs";
    const input = {
      version: 1,
      input: request,
      authorizeCalls: 2,
      attachment: attachment("标准.txt", standard),
      awaitCalculationReceipt: true,
    };
    const launchedAPI = await post(
      member.request,
      versioned,
      input,
      crypto.randomUUID(),
      headers,
    );
    const status = "/api/v1/runs/" + launchedAPI.id;
    const blocked = await until(async () => {
      const p = await get(member.request, status, headers);
      return p.nextAction === "submit_receipt" && p;
    });
    assert.equal(blocked.receiptRequest.step, 2);
    const key = crypto.randomUUID();
    const extra = {
      action: "submit_receipt",
      step: 2,
      expected: blocked.controlRevision,
      excerpt,
      attachment: attachment("结算说明.txt", original),
    };
    const submit = (data, h, k = key) =>
      member.request.post(status + "/actions", {
        headers: { ...h, "Idempotency-Key": k },
        data,
      });
    assert.equal((await submit(extra, appHeaders)).status(), 404);
    assert.equal(
      (await submit({ ...extra, excerpt: "" }, headers)).status(),
      422,
    );
    assert.equal(
      (
        await submit(
          {
            ...extra,
            excerpt: "",
            attachment: attachment("同一标准.txt", standard),
          },
          headers,
        )
      ).status(),
      409,
    );
    const prefix = "🧾 历史记录\n" + excerpt + "\n本次记录\n";
    extra.attachment = attachment("结算说明.txt", prefix + original);
    assert.equal((await submit(extra, headers)).status(), 422);
    extra.excerptStart = Array.from(prefix).length + 1;
    const range = {
      from: extra.excerptStart,
      to: extra.excerptStart + Array.from(excerpt).length - 1,
    };
    assert.equal(
      (
        await submit({ ...extra, excerptStart: range.from + 1 }, headers)
      ).status(),
      422,
    );
    const state = await readProject(launchedAPI.project);
    assert.equal(state.costSummary.calls, 1);
    assert.equal(state.planVersion, 1);
    const replies = await Promise.all([
      submit(extra, headers),
      submit(extra, headers),
    ]);
    for (const r of replies) assert.equal(r.status(), 200, await r.text());
    assert.deepEqual(await replies[0].json(), await replies[1].json());
    assert.equal(
      (await submit({ ...extra, excerptStart: 4 }, headers)).status(),
      409,
    );
    assert.equal(
      (
        await submit(
          { ...extra, attachment: attachment("另一个名称.txt", prefix + original) },
          headers,
        )
      ).status(),
      409,
    );
    const apiResult = await deliver(
      launchedAPI.project,
      "service-receipt-" + mode + "-api",
      member.request,
      status + "/delivery/package",
      headers,
    );
    assert.equal(apiResult.run, launchedAPI.id);
    const events = (await get(member.request, status + "/events", headers))
      .events;
    assert.equal(
      events.filter((e) => e.state === "receipt_submitted").length,
      1,
    );
    const revoked = await member.request.delete("/api/delegations/" + d.id);
    assert.ok(revoked.ok());
    assert.equal((await submit(extra, headers)).status(), 403);
    let applicationResult;
    if (!real) {
      const applicationRun = await post(
        member.request,
        versioned,
        input,
        crypto.randomUUID(),
        appHeaders,
      );
      const path = "/api/v1/runs/" + applicationRun.id;
      const pending = await until(async () => {
        const r = await get(member.request, path, appHeaders);
        return r.nextAction === "submit_receipt" && r;
      });
      await post(
        member.request,
        path + "/actions",
        { ...extra, expected: pending.controlRevision },
        crypto.randomUUID(),
        appHeaders,
      );
      applicationResult = await deliver(
        applicationRun.project,
        "service-receipt-fixture-app",
        member.request,
        path + "/delivery/package",
        appHeaders,
      );
      assert.equal(applicationResult.requester, app.id);
    }
    await writeFile(
      directory + "/service-receipt-" + mode + ".json",
      JSON.stringify(
        {
          web: webResult,
          app: applicationResult,
          api: apiResult,
          apiRange: range,
          initialCalls: 1,
          replayed: true,
          wrongPrincipal: 404,
          changedScope: 409,
          revokedDelegation: 403,
          receiptEvents: 1,
        },
        null,
        2,
      ),
    );
  } finally {
    await admin.close();
  }
}

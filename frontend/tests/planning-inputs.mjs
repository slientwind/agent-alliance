import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  QUICK_ENDPOINT: endpoint,
  VISUAL_EVIDENCE: out,
} = process.env;
const real = process.env.PLANNING_REAL_MODEL === "1";
const scenario = process.env.PLANNING_CASE || "handoff";
const cases = JSON.parse(
  await readFile(new URL("planning-cases.json", import.meta.url), "utf8"),
);
const sample = cases[scenario];
assert.ok(sample, "unknown planning case");
const mode =
  (real ? "local" : "fixture") + (scenario === "handoff" ? "" : "-" + scenario);
const browser = await chromium.launch();
const page = await browser.newPage({
  baseURL,
  viewport: { width: 390, height: 844 },
});
async function post(path, data, key) {
  const response = await page.request.post("/api/" + path, {
    data,
    headers: key ? { "Idempotency-Key": key } : {},
  });
  assert.ok(response.ok(), path + " " + (await response.text()));
  return response.status() === 204 ? null : response.json();
}
const get = async (path) => (await page.request.get("/api/" + path)).json();
const recorded = [];
const unrelatedName =
  "unselected-internal-" + "contract_revision_".repeat(6) + "2026.txt";
try {
  await post("login", { email: "review@sample.invalid", password });
  const team = (await post("admin/teams", { name: "资料规划 " + mode })).id;
  const original = (await get("teams")).find((t) => t.default).id;
  const admin = await browser.newPage({ baseURL });
  await admin.request.post("/api/login", {
    data: { email: "review@sample.invalid", password },
  });
  async function model(name, model) {
    return post("resources", { name, endpoint, model, team, key: "synthetic" });
  }
  const planner = await model(
    "选取资料规划 " + mode,
    real ? "local-course-qwen" : "planning-input-fixture",
  );
  const configured = await admin.request.put(
    "/api/teams/" + team + "/setup/planner",
    { data: { resource: planner.id, revision: 1, expected: 0 } },
  );
  assert.ok(configured.ok(), await configured.text());
  const text = await model(
    "问答执行 " + mode,
    real ? "local-course-qwen" : "source-lookup-fixture",
  );
  const reader = await post("agents", {
    name: "规程查证 " + mode,
    instructions: "在已授权的规程中核对事实，回答问题并附原文依据。",
    resource: text.id,
    resourceRevision: 1,
  });
  const group = await post("agents/tools", {
    team,
    name: "区域流水汇总 " + mode,
    description: `按 ${sample.group} 汇总所选 CSV 的 ${sample.amount} 金额，交付 Excel 表格。`,
    tool: {
      operation: "aggregate",
      config: { group: sample.group, amount: sample.amount },
    },
  });
  for (const [agent, input, expected] of [
    [reader, "请只回复两个字：收到", real ? "收到" : "RECEIPT"],
    [group, `${sample.group},${sample.amount}\n验收,30.10`, "30.10"],
  ]) {
    assert.equal(
      (
        await post("agents/" + agent.id + "/validate", {
          revision: 1,
          input,
          expected,
          authorizeOneCall: true,
        })
      ).status,
      "passed",
    );
    await post("agents/" + agent.id + "/publish", { revision: 1 });
  }
  const invite = await post("admin/invitations", {});
  await post("logout", {});
  await post("register", {
    token: invite.token,
    name: "业务经办",
    email: "plan-" + mode + "@sample.invalid",
    password,
  });
  const member = (await get("me")).id;
  for (const [id, joined] of [
    [team, true],
    [original, false],
  ]) {
    const r = await admin.request.put(
      "/api/admin/teams/" + id + "/members/" + member,
      { data: { joined, reason: "独立验证资料规划能力" } },
    );
    assert.ok(r.ok(), await r.text());
  }
  async function requestPlan(form) {
    const pending = page.waitForResponse(
      (r) =>
        r.url().endsWith("/suggestions") && r.request().method() === "POST",
    );
    await form.getByRole("button", { name: "请助手规划" }).click();
    const response = await pending;
    const body = await response.json();
    return { response, body };
  }
  const scope = async (question, docs, omitted = []) => {
    await page.goto("/");
    await page
      .getByRole("textbox", { name: "消息", exact: true })
      .fill(question);
    await page.locator(".composer input[type=file]").setInputFiles(
      docs.map(([name, content]) => ({
        name,
        buffer: Buffer.from(content),
        mimeType: "text/plain",
      })),
    );
    await page.getByRole("button", { name: "发送消息" }).click();
    const details = page
      .locator("details")
      .filter({
        has: page.getByRole("button", {
          name: "请助手规划",
          includeHidden: true,
        }),
      })
      .last();
    await details.waitFor();
    if (!(await details.evaluate((node) => node.open)))
      await details.locator("summary").first().click();
    const form = page
      .locator("form")
      .filter({ has: page.getByRole("button", { name: "请助手规划" }) });
    for (const [name] of docs) {
      const input = form.getByLabel(new RegExp(name));
      await input.waitFor();
      assert.ok(await input.isChecked(), name + " from composer");
      if (omitted.includes(name)) await input.uncheck();
    }
    if (!recorded.length) {
      const measurements = [];
      for (const width of [320, 390, 1440]) {
        await page.setViewportSize({ width, height: 844 });
        const dimensions = await form.evaluate((node) => {
          const thread = node.closest(".thread");
          return {
            formWidth: node.clientWidth,
            formScroll: node.scrollWidth,
            threadWidth: thread.clientWidth,
            threadScroll: thread.scrollWidth,
          };
        });
        assert.ok(
          dimensions.formScroll <= dimensions.formWidth + 1 &&
            dimensions.threadScroll <= dimensions.threadWidth + 1,
          JSON.stringify(dimensions),
        );
        measurements.push({ width, ...dimensions });
      }
      await page.setViewportSize({ width: 390, height: 844 });
      await writeFile(
        out + "/planning-layout-" + mode + ".json",
        JSON.stringify(measurements, null, 2),
      );
    }
    assert.equal(
      await form.getByLabel("规划助手模型").inputValue(),
      planner.id,
    );
    await form.getByLabel(/授权一次规划调用/).check();
    const path = "projects/" + new URL(page.url()).hash.slice(1);
    const { response, body } = await requestPlan(form);
    recorded.push({
      question,
      input: docs.map((d) => d[0]),
      selected: docs.map((d) => d[0]).filter((n) => !omitted.includes(n)),
      status: body.status,
      result: body.result,
    });
    await writeFile(
      out + "/planning-inputs-" + mode + ".json",
      JSON.stringify(recorded, null, 2),
    );
    return {
      path,
      body,
      pins: (await get(path + "/sources"))
        .filter((f) => !omitted.includes(f.name))
        .map(({ id, version }) => ({
          id,
          version,
        })),
      request: response.request(),
    };
  };
  async function finish(task, kind) {
    assert.equal(task.body.status, "ready", JSON.stringify(task.body));
    const state = await get(task.path);
    assert.equal(state.run, null);
    const result = task.body.result;
    assert.equal(result.kind, "plan", JSON.stringify(result));
    const expected = kind === "aggregate" ? group : reader;
    assert.deepEqual(result.agents, [expected.id], JSON.stringify(result));
    assert.deepEqual(result.lookupSteps ?? [], kind === "aggregate" ? [] : [1]);
    assert.deepEqual(
      result.calculationSteps ?? [],
      kind === "calculation" ? [1] : [],
    );
    const retry = await page.request.post(
      "/api/" + task.path + "/suggestions",
      {
        headers: {
          "Idempotency-Key": task.request.headers()["idempotency-key"],
        },
        data: {
          ...task.request.postDataJSON(),
          sources: [...task.pins].reverse(),
        },
      },
    );
    assert.equal((await retry.json()).id, task.body.id);
    const bad = await page.request.post("/api/" + task.path + "/suggestions", {
      headers: { "Idempotency-Key": task.request.headers()["idempotency-key"] },
      data: { ...task.request.postDataJSON(), sources: [] },
    });
    assert.equal(bad.status(), 409);
    const changed = await page.request.post("/api/" + task.path + "/plan", {
      data: {
        expected: state.revision,
        suggestion: task.body.id,
        brief: "替换资料范围",
        agents: result.agents,
        agentRevisions: result.revisions,
        sources: [],
      },
    });
    assert.equal(changed.status(), 409);
    const adopt = page.getByRole("button", { name: "采用为待确认方案" });
    const proposed = page.locator("section").filter({ has: adopt }).last();
    await proposed.screenshot({
      path: out + "/planning-" + mode + "-" + kind + ".png",
    });
    await adopt.click();
    await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
    const plan = (await get(task.path)).plan;
    assert.deepEqual(
      plan.sources.map((p) => p.id).sort(),
      task.pins.map((p) => p.id).sort(),
    );
    assert.ok(plan.brief.includes(recorded.at(-1).question));
    const calls = kind === "aggregate" ? 1 : 2;
    if (kind === "calculation")
      assert.equal(plan.nodes[1].lookupPhase, "answer_calc");
    assert.equal(plan.nodes.length, calls);
    await page.getByLabel(new RegExp("授权本次 " + calls)).check();
    await page.getByRole("button", { name: "确认并执行" }).click();
    const review = page.getByRole("button", { name: "审核此版本" });
    await review.first().waitFor({ timeout: 90000 });
    const finished = await get(task.path);
    const item = finished.artifacts.find((a) => a.ordinal === calls);
    Object.assign(recorded.at(-1), {
      plan: plan.nodes.map((n) => [n.operation, n.lookupPhase]),
      run: finished.run.id,
      output: finished.artifacts.map((a) => ({
        step: a.ordinal,
        text: a.body,
      })),
    });
    await writeFile(
      out + "/planning-inputs-" + mode + ".json",
      JSON.stringify(recorded, null, 2),
    );
    if (kind === "calculation") {
      assert.match(item.body, /差额为 78\.50 元/);
      assert.ok(item.body.includes("原文依据"));
    } else if (kind === "lookup") {
      const [answer, quotes] = item.body.split("原文依据");
      assert.ok(quotes, item.body);
      for (const term of sample.answerTerms)
        assert.ok(answer.includes(term), answer);
    } else {
      assert.ok(item.body.includes(sample.rows.at(-1)[1]), item.body);
    }
    for (let n = 0; n < calls; n++) await review.first().click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    await page.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
    const archive = await page.request.get(
      "/api/" + task.path + "/delivery/package",
    );
    assert.ok(archive.ok());
    await writeFile(
      out + "/planning-" + mode + "-" + kind + ".zip",
      await archive.body(),
    );
    Object.assign(recorded.at(-1), {
      plan: plan.nodes.map((n) => [n.operation, n.lookupPhase]),
      answer: item.body,
      run: finished.run.id,
    });
  }
  const query = sample.question;
  const procedure = [sample.procedure];
  await finish(
    await scope(
      query,
      [
        ...procedure,
        [unrelatedName, "另行保管的收货密钥 PRIVATE-879。不应进入执行。"],
      ],
      [unrelatedName],
    ),
    "lookup",
  );
  await finish(await scope(sample.sumRequest, sample.sheets), "aggregate");
  const another = unrelatedName;
  const inquire = await scope(
    "我有附件需要处理。请先询问交付目标。",
    [...procedure, [another, "PRIVATE-879 不用于本次任务"]],
    [another],
  );
  assert.equal(
    inquire.body.result?.kind,
    "question",
    JSON.stringify(inquire.body),
  );
  const supplement = sample.supplement;
  await page
    .getByRole("textbox", { name: "消息", exact: true })
    .fill(supplement);
  const sent = page.waitForResponse(
    (r) => r.url().endsWith("/messages") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "发送消息" }).click();
  assert.ok((await sent).ok());
  await page.reload();
  const panel = page
    .locator("details")
    .filter({
      has: page.getByRole("button", {
        name: "请助手规划",
        includeHidden: true,
      }),
    })
    .last();
  await panel.locator("summary").first().click();
  const form = panel.locator("form");
  assert.ok(await form.getByLabel(new RegExp(sample.procedure[0])).isChecked());
  assert.ok(!(await form.getByLabel(/unselected-internal/).isChecked()));
  await form.getByLabel(/授权一次规划调用/).check();
  const revised = await requestPlan(form);
  assert.equal(revised.body.status, "ready");
  assert.deepEqual(revised.body.result.lookupSteps, [1]);
  assert.ok(revised.body.result.executionBrief.includes(supplement));
  assert.deepEqual(
    revised.response.request().postDataJSON().sources,
    inquire.pins,
  );
  const current = await get(inquire.path);
  assert.equal(current.costSummary.calls, 2);
  assert.equal(current.run, null);
  Object.assign(recorded.at(-1), {
    supplement,
    replanned: revised.body.result,
    persistedSources: current.previousPlanningSources,
    planningCalls: current.costSummary.calls,
  });
  if (!real) {
    for (const marker of ["直接回答探针", "错误步骤探针", "错误格式探针"]) {
      const t = await scope(marker, procedure);
      assert.equal(t.body.status, "invalid");
      assert.equal((await get(t.path)).run, null);
    }
    const stale = await scope(query, procedure);
    const historical = stale.pins[0];
    const replaced = await page.request.put(
      "/api/" +
        stale.path +
        "/sources/" +
        historical.id +
        "?name=updated-handbook.txt&version=1",
      {
        data: Buffer.from("新版规程的冷链阈值已改为 4°C。"),
        headers: { "Content-Type": "application/octet-stream" },
      },
    );
    assert.ok(replaced.ok(), await replaced.text());
    await page.reload();
    const proposal = page
      .locator("section")
      .filter({ has: page.getByRole("button", { name: "采用为待确认方案" }) })
      .last();
    await proposal
      .getByText(/updated-handbook.txt（当前 v2）· 本次使用 v1/)
      .waitFor();
    const oldLink = await proposal
      .getByRole("link", { name: "查看本次使用的 v1 原件" })
      .getAttribute("href");
    assert.equal(
      await (await page.request.get(oldLink)).text(),
      procedure[0][1],
    );
    const info = await get(stale.path);
    assert.equal(info.suggestion.status, "ready");
    const wrongVersion = await page.request.post(
      "/api/" + stale.path + "/plan",
      {
        data: {
          expected: info.revision,
          suggestion: stale.body.id,
          brief: query,
          agents: [reader.id],
          sources: [{ id: historical.id, version: 2 }],
        },
      },
    );
    assert.equal(wrongVersion.status(), 409);
    recorded.at(-1).updatedFile = {
      current: 2,
      selected: historical.version,
      openedOriginal: true,
    };
    const file = await page.request.delete(
      "/api/" + stale.path + "/sources/" + stale.pins[0].id,
    );
    assert.ok(file.ok(), await file.text());
    const after = await get(stale.path);
    assert.equal(after.suggestion.status, "stale");
    assert.equal(after.suggestion.result, null);
    assert.match(after.messages.at(-1).body, /失效/);
    const res = await page.request.post("/api/" + stale.path + "/plan", {
      data: {
        expected: info.revision,
        suggestion: stale.body.id,
        brief: query,
        agents: [reader.id],
        sources: stale.pins,
      },
    });
    assert.equal(res.status(), 409);
    const denied = await page.request.post(
      "/api/" + stale.path + "/suggestions",
      {
        headers: { "Idempotency-Key": "retry-with-withdrawn-source" },
        data: { ...stale.request.postDataJSON(), expected: after.revision },
      },
    );
    assert.equal(denied.status(), 409);
    assert.deepEqual((await get(stale.path)).costSummary, after.costSummary);
    recorded.at(-1).postWithdrawal = after.suggestion.status;
  }
  if (scenario === "handoff") {
    await finish(
      await scope(
        "住宿结算实付比单晚住宿报销标准超出多少元？列出差额和原文依据。",
        [
          ["住宿结算.txt", "本次住宿结算实付 528.50 元。"],
          ["差旅标准.txt", "单晚住宿报销标准为 450 元。"],
        ],
      ),
      "calculation",
    );
  }
  await writeFile(
    out + "/planning-inputs-" + mode + ".json",
    JSON.stringify(recorded, null, 2),
  );
  console.log(
    "Planning selected a sourced answer or CSV aggregation before execution, both reviewed and delivered (" +
      mode +
      ")",
  );
} finally {
  await browser.close();
}

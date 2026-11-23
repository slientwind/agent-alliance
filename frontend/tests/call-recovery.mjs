import { chromium } from "playwright";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
const browser = await chromium.launch();
try {
  const owner = await browser.newContext({ baseURL: process.env.SMOKE_ORIGIN });
  const guest = await browser.newContext({ baseURL: process.env.SMOKE_ORIGIN });
  async function call(
    path,
    method = "GET",
    data,
    expected = 200,
    key,
    ctx = owner,
  ) {
    const r = await ctx.request.fetch("/api/" + path, {
      method,
      data,
      headers: key ? { "Idempotency-Key": key } : {},
    });
    assert.equal(
      r.status(),
      expected,
      method + " " + path + " " + (r.ok() ? "" : await r.text()),
    );
    return r.headers()["content-type"]?.includes("json") ? r.json() : null;
  }
  const password = process.env.BROWSER_PASSWORD;
  await call("login", "POST", { email: "review@sample.invalid", password });
  const invitation = await call("admin/invitations", "POST", {}, 201);
  const participant = await call(
    "register",
    "POST",
    {
      name: "讲义复核人",
      email: "operation-review@sample.invalid",
      token: invitation.token,
      password,
    },
    201,
    null,
    guest,
  );
  const project = (
    await call("projects", "POST", { goal: "两步讲义复核" }, 201)
  ).id;
  const base = "projects/" + project,
    detail = () => call(base);
  await call(
    base + "/participants",
    "POST",
    {
      member: participant.id,
      review: true,
      expected: (await detail()).revision,
    },
    204,
  );
  await call(
    base + "/plan",
    "POST",
    {
      expected: (await detail()).revision,
      brief: "CALL_RECOVERY_823",
      agents: [process.env.RECOVERY_AGENT, process.env.RECOVERY_AGENT],
      dependencies: [[], [1]],
    },
    204,
  );
  const { id: run } = await call(
    base + "/runs",
    "POST",
    { expected: (await detail()).revision, authorizeCalls: 2 },
    202,
    "uncertain-run",
  );
  async function until(accept) {
    for (let n = 0; n < 150; n++) {
      const s = await detail();
      if (accept(s)) return s;
      await new Promise((r) => setTimeout(r, 200));
    }
    throw Error("Recovery stage did not advance");
  }
  const before = await until((s) => s.run?.status === "reconcile");
  assert.equal(before.costSummary.calls, 2);
  const path = "runs/" + run + "/calls/2/review";
  const note = {
    attempt: 1,
    expected: before.run.controlRevision,
    decision: "hold",
    evidence: "值班人员核对供应商记录，仍无回执。",
    authorizeOneCall: false,
  };
  await call(path, "POST", note, 404, "ordinary-reviewer", guest);
  await call(path, "POST", { ...note, decision: "redo" }, 422, "implicit-redo");
  const h = await call(path, "POST", note, 202, "first-review");
  assert.deepEqual(await call(path, "POST", note, 202, "first-review"), h);
  await call(
    path,
    "POST",
    { ...note, evidence: "其他意见" },
    409,
    "first-review",
  );
  await call(path, "POST", note, 409, "outdated-review");
  assert.equal((await detail()).run.status, "reconcile");
  const budget = async (limit) =>
    call(
      base + "/budget",
      "PUT",
      {
        expected: (await detail()).budget.revision,
        limit,
        warningPercent: 80,
        reason: "验证新调用重新检查预算",
      },
      204,
    );
  await budget("0");
  const page = await owner.newPage();
  await page.goto("/#" + project);
  await page.getByText(/调用记录 2 项/).click();
  let box = page.getByText("步骤 2 · 调用核对", { exact: true });
  await box.click();
  await page.getByText(/值班人员核对供应商记录/).waitFor();
  await page.getByLabel("核对决定").selectOption("redo");
  await page
    .getByLabel(/核对依据（项目成员可见）/)
    .fill("已核实缺少回执，可接受再生成一份讲义和额外费用。");
  const authorize = page.getByLabel(/我已核对重复执行/);
  assert.equal(await authorize.isChecked(), false);
  await page.getByRole("button", { name: "记录核对决定" }).click();
  assert.equal((await detail()).run.status, "reconcile");
  await authorize.check();
  await box
    .locator("..")
    .screenshot({ path: process.env.VISUAL_EVIDENCE + "/call-recovery.png" });
  await page.getByRole("button", { name: "记录核对决定" }).click();
  const parked = await until((s) => s.run?.status === "blocked");
  assert.equal(parked.costSummary.calls, 2);
  assert.equal(parked.run.attempts[1].callReview.attempt, 1);
  const trace = base + "/runs/" + run + "/trace";
  assert.deepEqual(
    (await call(trace)).trace.spans[1].attempts.map((a) => a.status),
    ["unknown"],
  );
  await budget("10");
  await call(
    "runs/" + run + "/control",
    "POST",
    {
      expected: parked.run.controlRevision,
      action: "resume",
      reason: "预算已核实",
    },
    202,
    "resume-verified-call",
  );
  const complete = await until((s) => s.stage === "review");
  assert.equal(complete.artifacts[1].body, "已按新增授权完成复核");
  assert.equal(complete.costSummary.calls, 3);
  assert.equal(complete.costDetails.filter((x) => x.step === 2).length, 2);
  assert.equal(complete.artifacts[0].version, 1);
  const spans = (await call(trace)).trace.spans;
  assert.deepEqual(
    spans.map((s) => s.attempts.map((a) => a.status)),
    [["completed"], ["unknown", "completed"]],
  );
  const receipt = complete.run.attempts[1].callReview;
  assert.equal(receipt.attempt, 2);
  assert.deepEqual(
    receipt.history.map((x) => x.decision),
    ["hold", "redo"],
  );
  assert(!JSON.stringify(spans).includes(note.evidence));
  const events = (await call(trace)).trace.events;
  assert.deepEqual(
    events.filter((e) => e.kind === "call_review").map((e) => e.code),
    ["call_hold", "call_redo"],
  );
  await call(
    path,
    "POST",
    {
      ...note,
      expected: complete.run.controlRevision,
      decision: "redo",
      authorizeOneCall: true,
    },
    409,
    "finished-attempt",
  );
  for (const a of complete.artifacts)
    await call(
      base + "/artifacts/" + a.id + "/review",
      "POST",
      { version: a.version },
      204,
    );
  const item = base + "/artifacts/" + complete.artifacts[1].id;
  const feedback = (
    await call(item + "/feedback", "POST", {
      version: 1,
      kind: "change",
      body: "UNCERTAIN_EDIT_823 核对新增段落",
    })
  ).id;
  const change = (
    await call(
      item + "/feedback/" + feedback + "/change-runs",
      "POST",
      { version: 1, expected: 1, authorizeOneCall: true },
      202,
      "revision-recovery",
      guest,
    )
  ).run;
  const changeState = (s) => s.artifacts[1].feedback[0].changes[0];
  const uncertain = changeState(
    await until((s) => changeState(s)?.status === "reconcile"),
  );
  assert.equal(uncertain.callReview.step, 2);
  const revisionPath = "runs/" + change + "/calls/2/review";
  const consent = {
    attempt: 1,
    expected: uncertain.controlRevision,
    decision: "redo",
    authorizeOneCall: true,
    evidence: "核实局部改稿未收到回执，允许重复执行一次。",
  };
  async function rights(review) {
    await call(
      base + "/participants",
      "POST",
      { member: participant.id, review, expected: (await detail()).revision },
      204,
    );
  }
  await rights(false);
  await call(revisionPath, "POST", consent, 404, "change-consent", guest);
  await rights(true);
  await call(revisionPath, "POST", consent, 202, "change-consent", guest);
  const revised = await until((s) => s.artifacts[1].version === 2);
  assert.equal(revised.run.id, run);
  assert.equal(revised.run.status, "completed");
  assert.equal(revised.artifacts[0].approved, true);
  assert.equal(revised.artifacts[1].body, "核对后的修订讲义");
  assert.equal(revised.costSummary.calls, 5);
  assert.deepEqual(
    revised.costDetails
      .filter((c) => c.scope === "revision")
      .map((c) => c.attempt),
    [1, 2],
  );
  assert.deepEqual(
    changeState(revised).callReview.history.map((h) => h.actor),
    ["讲义复核人"],
  );
  await call(item + "/feedback/" + feedback, "POST", {
    version: 2,
    expected: 2,
    action: "resolve",
    reason: "已复核新增段落",
  });
  await call(item + "/review", "POST", { version: 2 }, 204);
  const delivery = await call(
    base + "/deliver",
    "POST",
    { expected: (await detail()).revision },
    201,
  );
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/call-recovery.json",
    JSON.stringify(
      {
        run,
        delivery: delivery.id,
        steps: spans.map((s) => s.attempts),
        originalRunCalls: complete.costSummary.calls,
        totalProjectCalls: revised.costSummary.calls,
        revision: {
          run: change,
          approvedVersions: [1, 2],
          history: changeState(revised).callReview.history,
        },
        review: receipt.history,
        checks: [
          "non-owner denied",
          "hold/replay/stale review",
          "risk consent separate",
          "budget blocked before additional spend",
          "prior unknown and prior step preserved",
          "revision actor loses and regains explicit review permission",
          "new revision requires separate approval and retains upstream review",
          "separate actual attempt receipts",
          "version review and confirmed delivery",
        ],
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

import { checkSourceRetention } from "./source-retention.mjs";
import { chromium } from "playwright";
import { execFileSync } from "node:child_process";
import { writeFile } from "node:fs/promises";
import assert from "node:assert/strict";
const browser = await chromium.launch();
try {
  const baseURL = process.env.SMOKE_ORIGIN,
    password = process.env.BROWSER_PASSWORD;
  const owner = await browser.newContext({ baseURL }),
    reader = await browser.newContext({ baseURL });
  async function call(ctx, path, method = "GET", data, expected = 200, key) {
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
    return r.headers()["content-type"]?.includes("json") ? r.json() : undefined;
  }
  await call(owner, "login", "POST", {
    email: "review@sample.invalid",
    password,
  });
  const token = (await call(owner, "admin/invitations", "POST", {}, 201)).token;
  const guest = await call(
    reader,
    "register",
    "POST",
    {
      name: "资料观察员",
      email: "source-observer@sample.invalid",
      password,
      token,
    },
    201,
  );
  const { id } = await call(
      owner,
      "projects",
      "POST",
      { goal: "核对讲义和独立摘要的培训依据" },
      201,
    ),
    base = "projects/" + id;
  const detail = () => call(owner, base);
  const { id: source } = await call(
    owner,
    base +
      "/sources?name=旧口径.txt".replace(
        "旧口径",
        encodeURIComponent("旧口径"),
      ),
    "POST",
    Buffer.from("培训时长为一小时"),
    201,
  );
  const url = base + "/sources/" + source;
  await call(reader, url + "/history", "GET", undefined, 404);
  await call(
    owner,
    base + "/participants",
    "POST",
    { member: guest.id, review: false, expected: (await detail()).revision },
    204,
  );
  assert.equal((await call(reader, base + "/sources"))[0].canMaintain, false);
  await call(
    owner,
    base + "/plan",
    "POST",
    {
      expected: (await detail()).revision,
      brief: "根据培训依据编写两种材料",
      agents: [
        process.env.SOURCE_IMPACT_AGENT,
        process.env.SOURCE_IMPACT_AGENT,
      ],
      dependencies: [[], []],
      sources: [{ id: source, version: 1 }],
    },
    204,
  );
  await call(
    owner,
    url + "?name=rename.txt&version=1",
    "PUT",
    Buffer.from("培训时长为一小时"),
    201,
  );
  await call(
    owner,
    base + "/runs",
    "POST",
    { expected: (await detail()).revision, authorizeCalls: 2 },
    202,
    "source-impact",
  );
  let p;
  for (let i = 0; i < 150; i++) {
    p = await detail();
    if (p.stage === "review") break;
    await new Promise((r) => setTimeout(r, 200));
  }
  assert.equal(p.stage, "review");
  const [a, b] = p.artifacts;
  assert(p.artifacts.every((a) => !a.sourceChanges.length));
  for (const x of p.artifacts)
    await call(
      owner,
      base + "/artifacts/" + x.id + "/review",
      "POST",
      { version: 1 },
      204,
    );
  const page = await owner.newPage();
  await page.goto("/#" + id);
  await page.getByText("项目资料 · 1", { exact: true }).click();
  await page.getByText("资料版本与引用影响", { exact: true }).click();
  await page.getByLabel("提交新版本").setInputFiles({
    name: "revised.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("培训时长为两小时"),
  });
  assert.equal((await call(owner, url + "/history")).current, 2);
  await page.getByRole("button", { name: "确认提交新版本" }).click();
  await page.getByText("revised.txt · v3", { exact: true }).waitFor();
  const snap = await call(owner, url + "/history");
  assert.deepEqual(
    snap.plans.map((p) => p.pinned),
    [1],
  );
  assert.equal(snap.versions.at(-1).name, "旧口径.txt");
  assert(snap.artifacts.every((a) => a.changed));
  const original = await owner.request.get(
    "/api/" + url + "/content?version=1",
  );
  assert.equal(await original.text(), "培训时长为一小时");
  await call(owner, url + "/content?version=abc", "GET", undefined, 422);
  assert(
    (await call(owner, "projects/review-inbox")).some(
      (t) => t.project === id && t.kind === "review",
    ),
  );
  await call(
    owner,
    base + "/deliver",
    "POST",
    { expected: (await detail()).revision },
    409,
  );
  const assess = (x) =>
    base + "/artifacts/" + x.id + "/source-assessments/" + source;
  const retain = {
    version: 1,
    pinned: 1,
    observed: 3,
    revoked: false,
    decision: "retain",
    reason: "该摘要未引用培训时长，沿用当前成果",
  };
  await call(reader, assess(a), "POST", retain, 403);
  await call(owner, assess(a), "POST", { ...retain, observed: 2 }, 409);
  await page.goto("/#" + id);
  const cards = page.locator("article.card");
  const card = cards.nth(0);
  await card.getByText(/资料依据核对 · 1/).click();
  await card.getByText("对照文本变化", { exact: true }).click();
  await card.getByText("培训时长为一小时", { exact: true }).waitFor();
  await card.getByText("培训时长为两小时", { exact: true }).waitFor();
  await card.getByRole("combobox", { name: "处理方式" }).selectOption("retain");
  await card.getByLabel("核对依据或所需修改").fill(retain.reason);
  await card.getByRole("button", { name: "记录资料影响" }).click();
  await card.getByText(new RegExp("已核对沿用.*" + retain.reason)).waitFor();
  await card
    .getByText(new RegExp("已核对沿用.*" + retain.reason))
    .locator("..")
    .screenshot({
      path: process.env.VISUAL_EVIDENCE + "/source-assessment.png",
    });
  await call(
    owner,
    assess(b),
    "POST",
    { ...retain, decision: "change", reason: "请把一小时更正为两小时" },
    204,
  );
  const art = base + "/artifacts/" + b.id,
    feedback = (await detail()).artifacts[1].feedback[0];
  assert.equal(feedback.state, "required");
  const path = art + "/feedback/" + feedback.id + "/change-runs";
  const grant = {
    version: 1,
    expected: 1,
    authorizeOneCall: true,
    sourceAssessment: (await detail()).artifacts[1].sourceChanges[0]
      .selectableAssessment,
  };
  await call(
    owner,
    path,
    "POST",
    { ...grant, sourceAssessment: source },
    409,
    "invalid-source",
  );
  await call(reader, path, "POST", grant, 403, "reader-source");
  await page.reload();
  const changeCard = cards.nth(1);
  await changeCard.locator("details.feedback > summary").click();
  await changeCard.getByText(/^交给 .+ 修改$/).click();
  const choice = changeCard.getByLabel(/仅本次改稿使用/);
  assert.equal(await choice.isChecked(), false);
  await choice.check();
  await changeCard.getByLabel(/授权负责此成果的 Agent/).check();
  await changeCard.getByRole("button", { name: "启动局部修改" }).click();
  await changeCard
    .getByText("按新版资料生成的培训讲义", { exact: true })
    .first()
    .waitFor();
  const versions = await call(owner, art + "/versions");
  assert.equal(versions[0].sourceInput.selected, 3);
  assert.equal(versions[1].sourceInput, null);
  const inputReceipt = versions[0].sourceInput;
  assert.equal(
    (await call(owner, url + "/history")).runs.find(
      (r) => r.id === inputReceipt.run,
    ).pinned,
    3,
  );
  assert.equal((await detail()).artifacts[0].version, 1);
  await call(owner, art + "/feedback/" + feedback.id, "POST", {
    version: 2,
    expected: 2,
    action: "resolve",
    reason: "审核人已核实",
  });
  await call(owner, art + "/review", "POST", { version: 2 }, 409);
  await call(
    owner,
    assess(b),
    "POST",
    { ...retain, version: 2, reason: "核对两份资料，已更正为两小时" },
    204,
  );
  await call(owner, art + "/review", "POST", { version: 2 }, 204);
  const noteText = "请复核资料引文 NOTE-513";
  const submitted = await call(
    owner,
    base + "/feedback-assignments",
    "POST",
    {
      original: "跨成果审核原文 ORIGINAL-922",
      items: [
        {
          body: noteText,
          kind: "suggestion",
          targets: [
            { artifact: a.id, version: 1 },
            { artifact: b.id, version: 2 },
          ],
        },
      ],
    },
    201,
    "review-note-visibility",
  );
  const annotations = await call(reader, base);
  assert.equal(
    annotations.feedbackOriginals[submitted.id],
    "跨成果审核原文 ORIGINAL-922",
  );
  assert(
    annotations.artifacts.every(
      (x) =>
        x.reviewNotesReadable && x.feedback.some((f) => f.body === noteText),
    ),
  );
  const readerPage = await reader.newPage();
  await readerPage.goto("/#" + id);
  const readerFeedback = readerPage.locator("details.feedback").first();
  await readerFeedback.locator(":scope > summary").click();
  await readerFeedback.getByText(noteText, { exact: true }).waitFor();
  const seal = await call(
    owner,
    base + "/deliver",
    "POST",
    { expected: (await detail()).revision },
    201,
  );
  async function manifest() {
    const zip = await owner.request.get("/api/" + base + "/delivery/package");
    assert.equal(zip.status(), 200);
    return JSON.parse(
      execFileSync(
        "python3",
        [
          "-c",
          "import io,json,sys,zipfile;sys.stdout.buffer.write(zipfile.ZipFile(io.BytesIO(sys.stdin.buffer.read())).read('manifest.json'))",
        ],
        { input: await zip.body() },
      ),
    );
  }
  await call(
    reader,
    base + "/delivery/revisions",
    "POST",
    {
      expected: (await detail()).revision,
      edition: 1,
      version: a.version,
      artifact: a.id,
      reason: "无审核权限",
    },
    403,
  );
  const frozen = await manifest();
  assert.equal(frozen.delivery, seal.id);
  assert.equal(frozen.sources[0].pinned, 1);
  assert.equal(frozen.sources[0].observed, 3);
  assert.equal(frozen.sources[0].assessments.length, 2);
  assert.deepEqual(frozen.sources[0].revisionInputs, [inputReceipt]);
  await call(
    owner,
    url + "?name=after.txt&version=3",
    "PUT",
    Buffer.from("交付后更新"),
    201,
  );
  const history = await call(owner, url + "/history");
  assert(
    history.artifacts.every((a) => a.delivered && a.changed && !a.decision),
  );
  assert.equal(history.assessments.length, 3);
  await call(
    owner,
    url + "?version=4&expectedProject=" + (history.projectRevision - 1),
    "DELETE",
    undefined,
    409,
  );
  await call(reader, url, "DELETE", undefined, 404);
  await call(
    owner,
    url + "?version=4&expectedProject=" + history.projectRevision,
    "DELETE",
    undefined,
    204,
  );
  await call(reader, url + "/content?version=1", "GET", undefined, 404);
  assert((await call(owner, url + "/history")).revoked);
  await call(reader, url + "/compare?from=1&to=3", "GET", undefined, 404);
  async function checkRestrictedNotes() {
    const view = await call(reader, base);
    assert.equal(view.feedbackOriginals[submitted.id], undefined);
    assert(
      view.artifacts.every(
        (x) =>
          x.reviewNotesReadable === false &&
          x.feedback.some((f) => f.submission === submitted.id) &&
          x.feedback.every(
            (f) =>
              f.body === null &&
              f.position === null &&
              f.events.every((e) => e.reason === null) &&
              !f.changes.length,
          ),
      ),
    );
    const sourceHistory = await call(reader, url + "/history");
    assert.equal(sourceHistory.assessments.length, 3);
    assert(sourceHistory.assessments.every((c) => c.reason === null));
    const serialized = JSON.stringify(view);
    for (const forbidden of [
      noteText,
      "ORIGINAL-922",
      retain.reason,
      "请把一小时更正为两小时",
    ]) {
      assert(!serialized.includes(forbidden), forbidden);
    }
  }
  await checkRestrictedNotes();
  await readerPage.evaluate(() => window.dispatchEvent(new Event("focus")));
  await readerFeedback
    .getByText("关联资料已撤权，反馈正文受限。")
    .first()
    .waitFor();
  assert.equal(
    await readerPage.getByText(noteText, { exact: true }).count(),
    0,
  );
  await readerFeedback.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/review-note-access.png",
  });
  await readerPage.close();
  assert.equal(
    (await detail()).feedbackOriginals[submitted.id],
    "跨成果审核原文 ORIGINAL-922",
  );
  assert(
    (await call(owner, url + "/history")).assessments.some(
      (c) => c.reason === retain.reason,
    ),
  );
  const readImpact = async () => (await detail()).distributionCases;
  const cases = await readImpact();
  assert.deepEqual(
    cases.map((c) => [c.step, c.version, c.state]),
    [
      [1, 1, "pending"],
      [2, 2, "pending"],
    ],
  );
  const decision = (c, state) => ({
    source: c.source,
    artifact: c.artifact,
    version: c.version,
    expected: c.revision,
    decision: state,
    reason: "已核对独立材料",
  });
  const route = base + "/distribution-reviews";
  await call(reader, route, "POST", decision(cases[0], "reference"), 403);
  assert(
    (await call(reader, base)).artifacts.every(
      (a) => a.body === "此成果版本已暂停分发",
    ),
  );
  await call(
    reader,
    base + "/artifacts/" + a.id + "/versions?version=1",
    "GET",
    undefined,
    404,
  );
  await call(reader, base + "/delivery", "GET", undefined, 409);
  assert.equal(
    (await owner.request.get("/api/" + base + "/delivery/package")).status(),
    409,
  );
  await call(owner, route, "POST", decision(cases[1], "invalid"), 204);
  await call(owner, route, "POST", decision(cases[1], "reference"), 409);
  assert(
    (await call(reader, "projects/review-inbox")).some(
      (t) => t.project === id && t.kind === "distribution",
    ),
  );
  await page.reload();
  const distribution = page.locator("#delivery-distribution");
  await distribution.locator(":scope > summary").click();
  const scope = distribution.locator("section").first();
  await scope.getByText("核对分发范围", { exact: true }).click();
  await scope.getByLabel("分发决定").selectOption("reference");
  await scope.getByLabel("处理依据").fill("独立记录已核实第一步");
  await scope.getByRole("button", { name: "保存分发决定" }).click();
  await distribution
    .getByText(/仅原资料无法访问，成果可分发/)
    .first()
    .waitFor();
  assert.equal(
    (await call(reader, base)).artifacts[1].body,
    "此成果版本已暂停分发",
  );
  assert(
    (await call(reader, base)).artifacts[0].body !== "此成果版本已暂停分发",
  );
  await distribution.screenshot({
    path: process.env.VISUAL_EVIDENCE + "/distribution.png",
  });
  const remain = (await readImpact())[1];
  await call(owner, route, "POST", decision(remain, "reference"), 204);
  assert(
    !(await call(reader, "projects/review-inbox")).some(
      (t) => t.project === id && t.kind === "distribution",
    ),
  );
  assert.deepEqual(await manifest(), frozen);
  assert.equal((await detail()).costSummary.calls, p.costSummary.calls + 1);
  await checkRestrictedNotes();
  const clearedVersion = await call(reader, art + "/versions?version=2");
  assert.equal(clearedVersion.length, 1);
  assert.equal(clearedVersion[0].reason, null);
  assert(clearedVersion[0].body);
  assert((await call(owner, art + "/versions?version=2"))[0].reason);
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/review-note-access.json",
    JSON.stringify(
      {
        project: id,
        submission: submitted.id,
        assignments: 2,
        assessments: 3,
        readableBeforeWithdrawal: true,
        openPageRefreshed: true,
        readerFeedbackText: null,
        readerEventReasons: null,
        readerGroupedOriginal: "withheld",
        reviewerEvidencePreserved: true,
        independentlyClearedVersionReadable: true,
        clearedVersionInternalReason: null,
        stillWithheldAfterDistributionClearance: true,
      },
      null,
      2,
    ),
  );
  const active = (
    await call(
      owner,
      "projects",
      "POST",
      { goal: "资料撤权后的独立依据核对" },
      201,
    )
  ).id;
  const second = "projects/" + active,
    state = () => call(owner, second);
  const sid = (
    await call(
      owner,
      second + "/sources?name=training.txt",
      "POST",
      Buffer.from("合成资料"),
      201,
    )
  ).id;
  const sourceURL = second + "/sources/" + sid;
  await call(
    owner,
    second + "/plan",
    "POST",
    {
      expected: (await state()).revision,
      brief: "核实依据",
      agents: [process.env.SOURCE_IMPACT_AGENT],
      sources: [{ id: sid, version: 1 }],
    },
    204,
  );
  await call(
    owner,
    second + "/runs",
    "POST",
    { expected: (await state()).revision, authorizeCalls: 1 },
    202,
    "revoked-source",
  );
  let live;
  for (let i = 0; i < 150; i++) {
    live = await state();
    if (live.stage === "review") break;
    await new Promise((r) => setTimeout(r, 200));
  }
  assert.equal(live.stage, "review");
  const item = second + "/artifacts/" + live.artifacts[0].id;
  await call(owner, item + "/review", "POST", { version: 1 }, 204);
  await page.goto("/#" + active);
  await page.getByText("项目资料 · 1", { exact: true }).click();
  await page.getByText("资料版本与引用影响", { exact: true }).click();
  await page.getByRole("button", { name: "撤销此资料访问" }).click();
  assert(!(await call(owner, sourceURL + "/history")).revoked);
  await page.getByRole("button", { name: "确认撤权" }).click();
  await page.getByText("已撤权", { exact: true }).waitFor();
  assert((await state()).artifacts[0].sourceChanges[0].revoked);
  await call(
    owner,
    second + "/deliver",
    "POST",
    { expected: (await state()).revision },
    409,
  );
  await call(
    owner,
    item + "/source-assessments/" + sid,
    "POST",
    { ...retain, observed: 1, revoked: false },
    409,
  );
  await call(
    owner,
    item + "/source-assessments/" + sid,
    "POST",
    {
      ...retain,
      observed: 1,
      revoked: true,
      reason: "已从另行核准的制度核实该结论，稿件可独立使用",
    },
    204,
  );
  await call(
    owner,
    second + "/deliver",
    "POST",
    { expected: (await state()).revision },
    201,
  );
  await call(owner, sourceURL + "/content?version=1", "GET", undefined, 404);
  // Park a revision before dispatch, then change its approved input twice.
  const queued =
    "projects/" +
    (
      await call(
        owner,
        "projects",
        "POST",
        { goal: "修订等待预算期间资料变化" },
        201,
      )
    ).id;
  const inspect = () => call(owner, queued);
  const ref = (
    await call(
      owner,
      queued + "/sources?name=scope.txt",
      "POST",
      Buffer.from("第一版"),
      201,
    )
  ).id;
  const resource = queued + "/sources/" + ref;
  await call(
    owner,
    queued + "/plan",
    "POST",
    {
      expected: (await inspect()).revision,
      brief: "根据资料制作讲义",
      agents: [process.env.SOURCE_IMPACT_AGENT],
      sources: [{ id: ref, version: 1 }],
    },
    204,
  );
  await call(
    owner,
    queued + "/runs",
    "POST",
    { expected: (await inspect()).revision, authorizeCalls: 1 },
    202,
    "scope-initial",
  );
  async function until(read, accept) {
    for (let n = 0; n < 150; n++) {
      const value = await read();
      if (accept(value)) return value;
      await new Promise((r) => setTimeout(r, 200));
    }
    throw Error("timed out waiting for scoped revision");
  }
  const draft = (await until(inspect, (v) => v.stage === "review"))
    .artifacts[0];
  const target = queued + "/artifacts/" + draft.id;
  await call(
    owner,
    resource + "?version=1&name=second.txt",
    "PUT",
    Buffer.from("第二版"),
    201,
  );
  await call(
    owner,
    target + "/source-assessments/" + ref,
    "POST",
    { ...retain, observed: 2, decision: "change" },
    204,
  );
  const initial = (await inspect()).artifacts[0];
  const request =
    target + "/feedback/" + initial.feedback[0].id + "/change-runs";
  const authorize = {
    version: 1,
    expected: 1,
    authorizeOneCall: true,
    sourceAssessment: initial.sourceChanges[0].selectableAssessment,
  };
  async function budget(limit) {
    await call(
      owner,
      queued + "/budget",
      "PUT",
      {
        expected: (await inspect()).budget.revision,
        warningPercent: 80,
        limit,
        reason: "测试排队中的修订授权",
      },
      204,
    );
  }
  await budget("0");
  const { run } = await call(
    owner,
    request,
    "POST",
    authorize,
    202,
    "scope-revision",
  );
  assert.equal(
    (await call(owner, request, "POST", authorize, 202, "scope-revision")).run,
    run,
  );
  await call(
    owner,
    request,
    "POST",
    { ...authorize, sourceAssessment: "" },
    409,
    "scope-revision",
  );
  const activeChange = async () =>
    (await inspect()).artifacts[0].feedback[0].changes[0];
  const waitBlocked = () => until(activeChange, (c) => c.status === "blocked");
  const control = async (action, key) =>
    call(
      owner,
      "runs/" + run + "/control",
      "POST",
      {
        action,
        reason: "核验当前资料授权",
        expected: (await activeChange()).controlRevision,
      },
      202,
      key,
    );
  await waitBlocked();
  await call(
    owner,
    resource + "?version=2&name=third.txt",
    "PUT",
    Buffer.from("第三版"),
    201,
  );
  await budget("5");
  await control("resume", "scope-new-version");
  await waitBlocked();
  assert.equal((await inspect()).costSummary.calls, 1);
  await call(
    owner,
    resource + "?version=3&expectedProject=" + (await inspect()).revision,
    "DELETE",
    undefined,
    204,
  );
  await control("resume", "scope-revoked");
  await waitBlocked();
  assert.equal((await inspect()).artifacts[0].version, 1);
  assert.equal((await inspect()).costSummary.calls, 1);
  await control("cancel", "scope-cancel");
  await call(owner, request, "POST", authorize, 409, "scope-obsolete-grant");
  await writeFile(
    process.env.VISUAL_EVIDENCE + "/source-impact.json",
    JSON.stringify(
      {
        project: id,
        source,
        delivery: seal.id,
        original: 1,
        observedAtDelivery: 3,
        latest: 4,
        delivered: frozen.items.map((i) => i.version),
        assessments: frozen.sources[0].assessments,
        revisionInputs: frozen.sources[0].revisionInputs,
        checks: [
          "renamed identical bytes",
          "old original readable by version",
          "authorized text comparison and revocation",
          "member and reviewer permissions",
          "stale input rejected",
          "previous approval held",
          "retained standalone artifact",
          "model received only the selected revision",
          "idempotent authorization and scoped version receipt",
          "updated or revoked input blocked on resume without a charge",
          "change requires new version and reassessment",
          "snapshot remains fixed after later update and revocation",
          "prior raw content denied after revocation",
          "no additional model calls on assessment",
          "browser upload and revoke show scope before mutation",
          "sealing blocked after in-review revocation until independent assessment",
        ],
      },
      null,
      2,
    ),
  );
  await checkSourceRetention({
    owner,
    reader,
    call,
    page,
    url,
    base,
    detail,
    decision,
    route,
  });
} finally {
  await browser.close();
}

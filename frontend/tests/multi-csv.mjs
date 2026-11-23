import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";
const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: evidence,
} = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const page = await browser.newPage({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  async function post(p, path, data) {
    const response = await p.request.post("/api/" + path, { data });
    assert.ok(response.ok(), await response.text());
    return response.status() === 204 ? null : response.json();
  }
  await post(admin, "login", { email: "review@sample.invalid", password });
  const agent = await post(admin, "agents/tools", {
    name: "核准金额合并助手",
    description: "按区域汇总全部 CSV",
    tool: {
      operation: "aggregate",
      config: {
        group: "region",
        amount: "amount",
        filterColumn: "status",
        filterValue: "ok",
      },
    },
  });
  const endpoint = "agents/" + agent.id;
  const east = "region,amount,status\nEast,30.10,ok\nWest,10.00,pending";
  const west = "status,amount,region\nok,62.20,West\nok,-2.00,West";
  const excluded = "region,amount,status\nEast,99.00,pending";
  assert.equal(
    (
      await post(admin, endpoint + "/validate", {
        revision: 1,
        input: east,
        expected: "30.10",
        authorizeOneCall: true,
      })
    ).status,
    "passed",
  );
  await post(admin, endpoint + "/publish", { revision: 1 });
  const invite = await post(admin, "admin/invitations", {});
  await post(page, "register", {
    email: "sum-user@sample.invalid",
    name: "报表使用者",
    token: invite.token,
    password,
  });
  async function select(files, goal) {
    await page.goto("/");
    await page.getByRole("button", { name: "选择或创建 Agent" }).click();
    await page.getByRole("button", { name: /核准金额合并助手/ }).click();
    await page.getByRole("textbox", { name: "消息", exact: true }).fill(goal);
    await page.locator('.composer input[type="file"]').setInputFiles(
      files.map(([name, data]) => ({
        name,
        mimeType: "text/csv",
        buffer: Buffer.from(data),
      })),
    );
    await page.getByRole("button", { name: "发送消息" }).click();
  }
  let lost = false;
  await page.route("**/sources?name=west.csv", async (route) => {
    if (lost) return route.continue();
    const saved = await route.fetch();
    assert.equal(saved.status(), 201);
    lost = true;
    await route.abort("failed");
  });
  await select(
    [
      ["east.csv", east],
      ["west.csv", west],
      ["archive.csv", excluded],
    ],
    "合并核准金额，保留来源和排除行数。",
  );
  await page
    .getByRole("alert")
    .filter({ hasText: /fetch|network|载入|网络/i })
    .waitFor();
  assert.ok(lost);
  await page.getByRole("button", { name: "发送消息" }).click();
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const base = "projects/" + new URL(page.url()).hash.slice(1);
  const get = async (path) => (await page.request.get("/api/" + path)).json();
  const sources = await get(base + "/sources");
  assert.equal(sources.length, 3);
  const inputList = page.getByText("汇总输入 · 3 份资料", { exact: true });
  await inputList.click();
  const panel = inputList.locator("..");
  for (const s of sources) {
    const link = panel.getByRole("link", { name: s.name + " · v1" });
    assert.ok(
      (await link.getAttribute("href")).endsWith(s.id + "/content?version=1"),
    );
  }
  await panel.screenshot({ path: join(evidence, "multi-csv-inputs.png") });
  assert.equal((await get(base)).plan.sources.length, 3);
  await page.getByLabel(/授权本次 1/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  await page.getByRole("button", { name: "审核此版本" }).waitFor();
  const artifact = (await get(base)).artifacts[0];
  assert.match(artifact.body, /合计 · 90\.30/);
  assert.match(artifact.body, /3 份输入，共 5 行，纳入 3 行/);
  await page.getByRole("button", { name: "审核此版本" }).click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const zip = page.getByRole("link", { name: "下载交付包（ZIP）" });
  const downloadEvent = page.waitForEvent("download");
  await zip.click();
  await (await downloadEvent).saveAs(join(evidence, "multi-csv-delivery.zip"));
  await select(
    [
      ["east.csv", east],
      ["broken.csv", "region,price,status\nWest,62.20,ok"],
    ],
    "检查缺失金额列",
  );
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  await page.getByLabel(/授权本次 1/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  await page.getByText(/汇总未生成成果。第 .*缺少配置的列：amount/).waitFor();
  const bad = "projects/" + new URL(page.url()).hash.slice(1);
  const rejected = await get(bad);
  assert.equal(rejected.run.canConcludeFailure, true);
  assert.equal(rejected.artifacts.length, 0);
  await page.getByText("控制运行", { exact: true }).click();
  await page.locator('select[name="action"]').selectOption("confirm_failure");
  await page
    .getByLabel("原因", { exact: true })
    .filter({ visible: true })
    .fill("已核对原件缺列");
  await page.getByRole("button", { name: "确认控制动作" }).click();
  await page.waitForFunction(
    async (path) =>
      (await (await fetch("/api/" + path)).json()).stage === "failed",
    bad,
  );
  // Review both missing columns against the pinned sources before authorizing a single retry.
  await select(
    [
      ["east.csv", "region,Amount,status\nEast,30.10,ok"],
      ["west.csv", "region,net_west,status\nWest,60.20,ok"],
    ],
    "核对各地金额字段并在原任务交付",
  );
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  await page.getByLabel(/授权本次 1/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  const repairProject = "projects/" + new URL(page.url()).hash.slice(1);
  const team = (await (await admin.request.get("/api/teams")).json()).find(
    (t) => t.default,
  );
  const teamPath = "/api/teams/" + team.id;
  if (!team.budgetOwner) {
    const member = await (await admin.request.get("/api/me")).json();
    const appointed = await admin.request.put(teamPath + "/budget-owner", {
      data: {
        member: member.id,
        previous: "",
        reason: "测试实例指定预算负责人",
      },
    });
    assert.equal(appointed.status(), 204, await appointed.text());
  }
  async function resourceBudget() {
    return (
      await (await admin.request.get(teamPath + "/budget")).json()
    ).resources.find((r) => r.agent === agent.id);
  }
  async function setLimit(resource, callLimit) {
    const saved = await admin.request.put(
      teamPath + "/resource-budgets/" + resource.id,
      {
        data: {
          limit: resource.limit,
          callLimit,
          expected: resource.revision,
          warningPercent: 80,
          reason: "补正测试预算",
        },
      },
    );
    assert.equal(saved.status(), 204, await saved.text());
  }
  const fields = page.locator("fieldset").filter({
    has: page.getByLabel("amount 对应的原表列"),
  });
  await fields.filter({ hasText: "east.csv" }).waitFor();
  await fields.filter({ hasText: "west.csv" }).waitFor();
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  const current = (await get(repairProject)).run;
  const sameRun = current.id;
  const review = current.attempts[0].callReview;
  assert.equal(review.attempt, 1);
  assert.equal(review.inputIssue, null);
  const mappings = review.inputIssues.map((issue) => {
    const amount = issue.name === "east.csv" ? "Amount" : "net_west";
    assert.deepEqual(issue.choices, [amount]);
    return { source: issue.source, version: issue.version, name: issue.name, amount };
  });
  assert.equal(mappings.length, 2);
  const reviewEndpoint = "/api/runs/" + sameRun + "/calls/1/review";
  const data = {
    attempt: 1,
    expected: current.controlRevision,
    decision: "redo",
    evidence: "已核对原表列与汇总字段的对应关系",
    authorizeOneCall: true,
    inputMappings: mappings.map(({ source, version, amount }) => ({
      source, version, columns: { amount },
    })),
  };
  async function attemptReview(inputMappings, expected, key, requester = page) {
    const response = await requester.request.post(reviewEndpoint, {
      data: { ...data, inputMappings },
      headers: { "Idempotency-Key": key },
    });
    assert.equal(response.status(), expected, await response.text());
  }
  await attemptReview([data.inputMappings[0]], 409, "incomplete-fields");
  await attemptReview([data.inputMappings[0], data.inputMappings[0]], 422, "duplicate-file-fields");
  await attemptReview(
    [data.inputMappings[0], { ...data.inputMappings[1], columns: { amount: "unknown" } }],
    422,
    "invalid-original-field",
  );
  await attemptReview(data.inputMappings, 404, "wrong-person", admin);
  const budgetAtBlock = await resourceBudget();
  await setLimit(budgetAtBlock, budgetAtBlock.calls);
  for (const { name, amount } of mappings) {
    const choose = fields.filter({ hasText: name }).getByLabel("amount 对应的原表列");
    assert.equal(await choose.inputValue(), "");
    if (name === "east.csv")
      assert.equal(await choose.locator('option[value="Amount"]').textContent(), "Amount（同名候选）");
    await choose.selectOption(amount);
  }
  await page.getByLabel(/我已核对字段含义/).check();
  await fields.first().locator("xpath=ancestor::form")
    .screenshot({ path: join(evidence, "column-correction-mobile.png") });
  const submitted = page.waitForRequest(
    (r) => r.method() === "POST" && new URL(r.url()).pathname === reviewEndpoint,
  );
  await page.getByRole("button", { name: "按对应字段重新计算" }).click();
  const receipt = await submitted;
  const response = await receipt.response();
  assert.equal(response.status(), 202, await response.text());
  await page.getByText(/资源调用次数已用完/).first().waitFor();
  const blocked = (await get(repairProject)).run;
  assert.equal(blocked.status, "blocked");
  assert.equal(blocked.attempts[0].callReview.attempt, 1);
  assert.equal(blocked.attempts[0].callReview.authorized, true);
  const limit = await resourceBudget();
  assert.equal(limit.calls, budgetAtBlock.calls);
  await setLimit(limit, null);
  const resumed = await page.request.post("/api/runs/" + sameRun + "/control", {
    data: {
      action: "resume",
      expected: blocked.controlRevision,
      reason: "预算已恢复，沿用已确认字段",
    },
    headers: { "Idempotency-Key": "resume-budget" },
  });
  assert.equal(resumed.status(), 202, await resumed.text());
  await page.getByRole("button", { name: "审核此版本" }).waitFor();
  const solved = await get(repairProject);
  const preview = page.locator("article.card details.file-preview").first();
  const previewRoute = "**/artifacts/*/file?*preview=1*";
  await page.route(previewRoute, async (route) => {
    const response = await route.fetch();
    const bytes = await response.body();
    assert.equal(response.status(), 200);
    assert.equal(bytes.subarray(0, 5).toString(), "%PDF-");
    await writeFile(join(evidence, "column-correction-preview.pdf"), bytes);
    await route.fulfill({ response, body: bytes });
  });
  await preview.locator("summary").first().click();
  await preview.getByText("第 1 / 4 页").waitFor();
  for (let number = 2; number <= 4; number++) {
    await preview.getByRole("button", { name: "下一页" }).click();
    await preview.getByText("第 " + number + " / 4 页").waitFor();
  }
  await preview.getByText("本页可读取文字").click();
  await preview.getByText(/Amount/).waitFor();
  assert.match(await preview.innerText(), /net_west/);
  await preview.locator('canvas[data-rendered="true"]').screenshot({
    path: join(evidence, "column-correction-browser-preview.png"),
  });
  const canvas = await preview
    .locator('canvas[data-rendered="true"]')
    .evaluate((el) => el.toDataURL("image/png").split(",")[1]);
  await writeFile(
    join(evidence, "column-correction-canvas.png"),
    Buffer.from(canvas, "base64"),
  );
  await preview.locator("summary").first().click();
  assert.equal(solved.run.id, sameRun);
  assert.equal(
    solved.run.attempts[0].callReview.history.filter((x) => x.corrected).length,
    1,
  );
  assert.match(solved.artifacts[0].body, /合计 · 90\.30/);
  const replay = receipt.postDataJSON();
  replay.inputMappings.reverse();
  assert.equal((await page.request.post(reviewEndpoint, {
    data: replay,
    headers: { "Idempotency-Key": receipt.headers()["idempotency-key"] },
  })).status(), 202);
  await page.getByRole("button", { name: "审核此版本" }).click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const correctedZip = page.waitForEvent("download");
  await page.getByRole("link", { name: "下载交付包（ZIP）" }).click();
  await (
    await correctedZip
  ).saveAs(join(evidence, "column-correction-delivery.zip"));
  assert.equal((await resourceBudget()).calls, budgetAtBlock.calls + 1);
  // Keep the old form open while another tab withdraws its pinned source.
  await select(
    [
      ["withdraw.csv", "region,net,status\nEast,30.10,ok"],
      ["remain.csv", "region,revenue,status\nWest,60.20,ok"],
    ],
    "撤权后的补正",
  );
  await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  await page.getByLabel(/授权本次 1/).check();
  await page.getByRole("button", { name: "确认并执行" }).click();
  await fields.filter({ hasText: "withdraw.csv" }).waitFor();
  const withdrawnProject = "projects/" + new URL(page.url()).hash.slice(1);
  const pending = (await get(withdrawnProject)).run;
  const withdrawnIssues = pending.attempts[0].callReview.inputIssues;
  const oldIssue = withdrawnIssues.find((issue) => issue.name === "withdraw.csv");
  const revoked = await page.request.delete(
    "/api/" + withdrawnProject + "/sources/" + oldIssue.source,
  );
  assert.equal(revoked.status(), 204);
  const oldForm = await page.request.post(
    "/api/runs/" + pending.id + "/calls/1/review",
    {
      data: {
        decision: "redo",
        attempt: 1,
        expected: pending.controlRevision,
        authorizeOneCall: true,
        evidence: "原页面",
        inputMappings: withdrawnIssues.map((issue) => ({
          source: issue.source,
          version: issue.version,
          columns: { amount: issue.choices[0] },
        })),
      },
      headers: { "Idempotency-Key": "withdrawn-mapping" },
    },
  );
  assert.equal(oldForm.status(), 409);
  const withdrawn = await get(withdrawnProject);
  assert.equal(withdrawn.artifacts.length, 0);
  assert.equal(withdrawn.run.attempts[0].callReview.inputIssues, null);
  // Erase one accepted source and the uncorrected rejected source.
  for (const [project, source] of [
    [repairProject, mappings[0].source],
    [withdrawnProject, oldIssue.source],
  ]) {
    const path = project + "/sources/" + source;
    for (const action of ["recycle", "purge"]) {
      const history = await get(path + "/history");
      await post(page, path + "/lifecycle", {
        action,
        reason: "核对后清理样例数据",
        version: history.current,
        expectedProject: history.projectRevision,
      });
    }
  }
  const remaining = (await get(repairProject)).run.attempts[0].callReview
    .history;
  assert.equal(remaining.filter((h) => h.corrected).length, 1);
  const retained = remaining.flatMap((h) => h.corrections ?? []);
  assert.equal(retained.length, 1);
  assert.equal(retained[0].name, mappings[1].name);
  await writeFile(
    join(evidence, "column-correction.json"),
    JSON.stringify(
      {
        run: sameRun,
        attempts: solved.run.attempts[0].callReview.attempt,
        mappings,
        body: solved.artifacts[0].body,
        duplicateResponse: 202,
        unauthorized: 404,
        invalidColumn: 422,
        revoked: 409,
        purgedIssueRun: pending.id,
        purgedMappingSource: mappings[0].source,
        budgetBlockedBeforeNewAttempt: true,
        extraCalls: 1,
        missingFiles: mappings.length,
      },
      null,
      2,
    ),
  );
  const retryProject = await post(page, "projects", {
    goal: "检查同一附件的上传回执",
  });
  const uploads = "/api/projects/" + retryProject.id + "/sources";
  const keyed = (data) =>
    page.request.post(uploads + "?name=stable.csv", {
      data: Buffer.from(data),
      headers: { "Idempotency-Key": "same-attachment" },
    });
  const pair = await Promise.all([keyed(east), keyed(east)]);
  assert.ok(pair.every((r) => r.status() === 201));
  const replies = await Promise.all(pair.map((r) => r.json()));
  assert.equal(replies[0].id, replies[1].id);
  assert.equal((await (await page.request.get(uploads)).json()).length, 1);
  assert.equal((await keyed(west)).status(), 409);
  assert.equal(
    (await page.request.delete(uploads + "/" + replies[0].id)).status(),
    204,
  );
  assert.equal((await keyed(east)).status(), 409);
  await writeFile(
    join(evidence, "multi-csv.json"),
    JSON.stringify(
      {
        sources: sources.map(({ id, name, version }) => ({
          id,
          name,
          version,
        })),
        artifact: artifact.body,
        reviewedAndDelivered: true,
        replayedUpload: true,
        concurrentUploadsStored: 1,
        conflictingReplay: 409,
        revokedReplay: 409,
        invalidInput: "declared failure",
        invalidArtifacts: 0,
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

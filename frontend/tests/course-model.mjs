import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: dir,
  QUICK_ENDPOINT: endpoint,
} = process.env;
const cases = [
  {
    id: "training",
    goal: "用资料原文做三页员工课件，标题依次是领取设备、归还设备、遗失处理。写清领取前应填什么和谁确认，归还怎样登记，遗失怎么报告。只写原件中的规定。",
    filename: "员工制度.txt",
    source:
      "员工先填写设备用途和预计归还日，经部门负责人确认后领取。归还时由保管员核对设备编号并登记。遗失时立即报告保管员。",
    checks: [
      {
        title: "领取设备",
        facts: ["设备用途", "预计归还日", "部门负责人", "确认"],
      },
      { title: "归还设备", facts: ["保管员", "设备编号", "登记"] },
      { title: "遗失处理", facts: ["立即", "报告", "保管员"] },
    ],
  },
  {
    id: "product",
    goal: "请做两页新品内部评审简报，标题依次为已知规格、待补资料。第一页介绍编号、容量、颜色和包装清单，第二页写明零售价与保温时长的数据缺口及交付前的审核人。资料没写的数值一律标为待补。",
    filename: "新品资料.txt",
    source:
      "编号 CW-01，容量 480 mL，杯身海蓝色，包装含杯和说明卡。保温时长尚无测量资料。零售价暂未给出。交付前由产品审核人核对规格。",
    checks: [
      {
        title: "已知规格",
        facts: ["CW-01", "480mL", "海蓝色", "杯", "说明卡"],
      },
      { title: "待补资料", facts: ["零售价", "保温时长", "产品审核人"] },
    ],
  },
];
const archiveTransfer = {
  id: "archive",
  goal: "根据流程原件做一份三页档案数字化交接说明，标题依次为接收、扫描、归档。完整保留责任人、参数、个人信息处理范围和缺页的处理条件。",
  filename: "数字化交接流程.txt",
  source:
    "接收前由档案员与送交人共同核对箱号。扫描分辨率为 300 dpi。含个人信息的页只在馆内工作站处理。归档时由复核员核对页数。缺页须挂起，不得标为完成。",
  checks: [
    { title: "接收", facts: ["档案员", "送交人", "共同", "核对箱号"] },
    { title: "扫描", facts: ["300dpi", "含个人信息", "只在馆内工作站处理"] },
    {
      title: "归档",
      facts: ["复核员", "核对页数", "缺页", "挂起", "不得标为完成"],
    },
  ],
};
const originalInstructions =
  "你负责编写可编辑课件的分页提纲。每页用一行“## 页标题”开头，接着写本页正文。每页正文最多7行，每行最多36个汉字，必要时分行。严格按要求的页数和标题，只依据用户提供的资料。优先保留原文中的责任人、动作和数字。没有依据的内容标为待补。输出纯提纲，不写代码块、工具调用、封面或说明。";
const profile = process.env.COURSE_MODEL_PROFILE || "source-extract";
assert.ok(["baseline", "source-extract"].includes(profile));
const preset = JSON.parse(
  await readFile(new URL("../src/course-writer.json", import.meta.url), "utf8"),
);
const instructions =
  profile === "baseline" ? originalInstructions : preset.instructions;
// Evaluate the reviewer-selected source sentence after retaining v1 scores separately.
async function checkProductCorrection(base, artifact, pin, sentence) {
  const file = base + "/artifacts/" + artifact.id;
  const compare = (v) =>
    api(file + "/source-comparison/" + pin.id + "?version=" + v, "GET");
  const first = await compare(1);
  const row = first.rows.find((r) => r.text === sentence);
  assert.ok(
    row,
    "Expected reviewer-selected policy sentence in the pinned source",
  );
  if (row.matches.includes(2)) {
    await writeFile(
      dir + "/source-comparison-local-correction.json",
      JSON.stringify(
        { action: "original sentence already present", first },
        null,
        2,
      ),
    );
    return;
  }
  // The sample reviewer picked page 2 in the frozen brief, independently of the suggested page.
  const note = await api(file + "/feedback", "POST", {
    version: 1,
    kind: "change",
    position: "第 2 页",
    body: "核对交付前的责任人与核对动作。",
    sourceQuote: {
      source: pin.id,
      version: pin.version,
      start: row.start,
      end: row.end,
      hash: row.hash,
    },
  });
  const quote = await api(
    file + "/feedback/" + note.id + "/source-quote?version=1",
    "GET",
  );
  assert.equal(quote.text, sentence);
  const [title, ...lines] = artifact.body.split("\n\n")[1].split("\n");
  const reviewerLine = lines.findIndex(
    (l) => l.includes("审核人") && !/保温|零售价|容量/.test(l),
  );
  if (reviewerLine >= 0) lines[reviewerLine] = quote.text;
  else lines.push(quote.text);
  await api(
    file + "/feedback/" + note.id + "/change-runs",
    "POST",
    {
      version: 1,
      expected: 1,
      authorizeOneCall: true,
      slide: { page: 2, title: title.slice(3), body: lines.join("\n") },
    },
    "local-source-correction",
  );
  let latest;
  for (let i = 0; i < 75; i++) {
    latest = (await api(base, "GET")).artifacts.find(
      (a) => a.id === artifact.id,
    );
    if (latest.version === 2) break;
    await new Promise((r) => setTimeout(r, 350));
  }
  assert.equal(latest.version, 2);
  assert.equal(latest.approved, false);
  const final = await compare(2);
  assert.deepEqual(final.rows.find((r) => r.text === sentence)?.matches, [2]);
  const r = await page.request.get("/api/" + file + "/file?version=2");
  assert.equal(r.status(), 200);
  await writeFile(
    dir + "/source-comparison-local-corrected.pptx",
    await r.body(),
  );
  await writeFile(
    dir + "/source-comparison-local-correction.json",
    JSON.stringify(
      {
        action: "reviewer requested single-page revision",
        project: base,
        first,
        final,
        pendingReview: !latest.approved,
      },
      null,
      2,
    ),
  );
  const openNote = latest.feedback.find((f) => f.id === note.id);
  await api(file + "/feedback/" + note.id, "POST", {
    expected: openNote.revision,
    version: 2,
    action: "resolve",
    reason: "已核对原文中的交付条件、责任人及核对规格要求。",
  });
  const writer = (await api(base, "GET")).artifacts.find(
    (a) => a.ordinal === 1,
  );
  await api(base + "/artifacts/" + writer.id + "/revise", "POST", {
    version: writer.version,
    body: latest.body,
    reason: "同步审核已核对的原句，保持提纲与课件一致。",
  });
  const revised = await api(base, "GET");
  assert.deepEqual(
    revised.artifacts.map((a) => [a.version, a.approved]),
    [
      [2, false],
      [3, false],
    ],
  );
  assert.equal(revised.artifacts[0].body, revised.artifacts[1].body);
  const stopped = await page.request.post("/api/" + base + "/deliver", {
    data: { expected: revised.revision },
  });
  assert.equal(stopped.status(), 409);
  const last = await compare(3);
  assert.deepEqual(last.rows.find((r) => r.text === sentence)?.matches, [2]);
  assert.match(latest.body, /零售价.*(?:未|待补)/);
  assert.match(latest.body, /保温时长.*(?:无|未|待补)/);
  await page.goto("/#" + revised.id);
  for (let i = 0; i < 2; i++)
    await page.getByRole("button", { name: "审核此版本" }).first().click();
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = page.getByRole("link", { name: "下载交付包（ZIP）" });
  await download.waitFor();
  const pack = await page.request.get(await download.getAttribute("href"));
  assert.equal(pack.status(), 200);
  await writeFile(
    dir + "/source-comparison-local-delivery.zip",
    await pack.body(),
  );
  const delivered = await api(base, "GET");
  assert.equal(delivered.stage, "delivered");
  await writeFile(
    dir + "/source-comparison-local-delivery.json",
    JSON.stringify(
      {
        actor: "synthetic enterprise reviewer in isolated instance",
        project: delivered.id,
        stage: delivered.stage,
        approved: delivered.artifacts.map((a) => ({
          step: a.ordinal,
          version: a.version,
          approved: a.approved,
        })),
        blockedBeforeReview: stopped.status(),
        confirmedOriginal: sentence,
      },
      null,
      2,
    ),
  );
}
const browser = await chromium.launch();
const page = await browser.newPage({ baseURL });
async function api(path, method, data, key) {
  const r = await page.request.fetch("/api/" + path, {
    method,
    data,
    headers: key ? { "Idempotency-Key": key } : {},
  });
  assert.ok(r.ok(), path + " " + (await r.text()));
  if (r.headers()["content-type"]?.includes("json")) return r.json();
}
try {
  await api("login", "POST", { email: "review@sample.invalid", password });
  const team = (await api("admin/teams", "POST", { name: "本地模型课件验证" }))
    .id;
  const { id: model } = await api("resources", "POST", {
    name: "Qwen3 本地编稿",
    team,
    operation: "text",
    model: "local-course-qwen",
    endpoint,
    key: "loopback-only",
    estimate: "0",
  });
  const writer = await api("agents", "POST", {
    name: "课件编稿人",
    instructions,
    resource: model,
    resourceRevision: 1,
  });
  const tool = await api("agents/tools", "POST", {
    name: "分页课件制作人",
    description: "按上一步的逐页提纲排版",
    team,
    tool: { operation: "pptx", contract: "pptx-outline-v1" },
  });
  const trial = await api(
    "agents/" + writer.id + "/validate",
    "POST",
    {
      revision: 1,
      input: "制作一页课件，标题为登记。资料原文：领取前登记用途。",
      expected: "## 登记",
      authorizeOneCall: true,
    },
    "real-course-probe",
  );
  await writeFile(
    dir + "/course-local-trial.json",
    JSON.stringify(trial, null, 2),
  );
  assert.equal(trial.status, "passed");
  const check = await api(
    "agents/" + tool.id + "/validate",
    "POST",
    {
      revision: 1,
      input: "## 登记\n领取前登记用途。",
      expected: "登记",
      authorizeOneCall: true,
    },
    "real-course-tool-probe",
  );
  assert.equal(check.status, "passed");
  for (const a of [writer, tool])
    await api("agents/" + a.id + "/publish", "POST", { revision: 1 });
  // Freeze the input and per-page factual criteria before production runs.
  await writeFile(
    dir + "/course-local-protocol.json",
    JSON.stringify({ profile, instructions, cases }, null, 2),
  );
  const report = [];
  const transferReport = [];
  await writeFile(
    dir + "/source-comparison-transfer-protocol.json",
    JSON.stringify(
      {
        instructions,
        case: archiveTransfer,
        criteria:
          "First attempt only. Native three-page PPTX, specified titles, jointly checking roles, exact scan resolution, location restriction, missing-page hold and prohibition.",
      },
      null,
      2,
    ),
  );
  for (const item of [...cases, archiveTransfer]) {
    const { id } = await api("projects", "POST", { goal: item.goal, team });
    const base = "projects/" + id;
    const source = await page.request.post(
      "/api/" + base + "/sources?name=" + encodeURIComponent(item.filename),
      {
        data: Buffer.from(item.source),
        headers: { "content-type": "application/octet-stream" },
      },
    );
    assert.ok(source.ok(), await source.text());
    const pin = await source.json();
    const snapshot = await api(base, "GET");
    await api(base + "/plan", "POST", {
      expected: snapshot.revision,
      brief: item.goal,
      agents: [writer.id, tool.id],
      agentRevisions: [1, 1],
      sources: [{ id: pin.id, version: pin.version }],
    });
    const approved = await api(base, "GET");
    await api(
      base + "/runs",
      "POST",
      { expected: approved.revision, authorizeCalls: 2 },
      "course-real-start-" + item.id,
    );
    let current;
    for (let i = 0; i < 150; i++) {
      current = await api(base, "GET");
      if (
        ["review", "failed", "reconcile", "blocked", "cancelled"].includes(
          current.stage,
        )
      )
        break;
      await new Promise((r) => setTimeout(r, 650));
    }
    const draft = current.artifacts.find((a) => a.ordinal === 1),
      deck = current.artifacts.find((a) => a.ordinal === 2);
    const body = draft?.body || "";
    const blocks = body
      .trim()
      .split(/^## +/mu)
      .slice(1)
      .map((part) => {
        const [title, ...lines] = part.trim().split("\n");
        return {
          title: title.trim(),
          text: lines.join("").replace(/\s+/g, ""),
        };
      });
    const facts = item.checks.map((c, i) => ({
      title: c.title,
      titleMatches: blocks[i]?.title === c.title,
      missing: c.facts.filter((f) => !blocks[i]?.text.includes(f)),
    }));
    if (deck?.file) {
      const file = await page.request.get(
        "/api/" + base + "/artifacts/" + deck.id + "/file?version=1",
      );
      assert.equal(file.status(), 200);
      await writeFile(
        dir + "/course-local-" + item.id + ".pptx",
        await file.body(),
      );
    }
    if (deck?.file) {
      const comparison = await api(
        base +
          "/artifacts/" +
          deck.id +
          "/source-comparison/" +
          pin.id +
          "?version=1",
        "GET",
      );
      assert.equal(comparison.pin, pin.version);
      await writeFile(
        dir + "/source-comparison-local-" + item.id + ".json",
        JSON.stringify(comparison, null, 2),
      );
    }
    const batch = item.id === archiveTransfer.id ? transferReport : report;
    batch.push({
      case: item.id,
      project: id,
      sourceId: pin.id,
      sourceVersion: pin.version,
      stage: current.stage,
      actualText: body,
      artifact: deck?.file || null,
      attempts: current.run?.attempts?.map((a) => ({
        step: a.step,
        status: a.status,
        tokens: a.tokens,
      })),
      pages: blocks.length,
      facts,
    });
    await writeFile(
      dir +
        (item.id === archiveTransfer.id
          ? "/source-comparison-transfer-results.json"
          : "/course-local-results.json"),
      JSON.stringify(batch, null, 2),
    );
    if (item.id === "product" && deck?.file)
      await checkProductCorrection(
        base,
        deck,
        pin,
        "交付前由产品审核人核对规格。",
      );
  }
} finally {
  await browser.close();
}

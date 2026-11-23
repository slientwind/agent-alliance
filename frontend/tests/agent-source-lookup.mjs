import { checkServiceReceipts } from "./service-receipts.mjs";
import { checkCalculationReceipt } from "./calculation-receipt.mjs";
import { checkSourceCalculation } from "./source-calculation.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const { SMOKE_ORIGIN, BROWSER_PASSWORD, QUICK_ENDPOINT, VISUAL_EVIDENCE } =
  process.env;
const real = process.env.LOOKUP_REAL_MODEL === "1";
const mode = real ? "local-model" : "fixture";
const browser = await chromium.launch();
const page = await browser.newPage({
  baseURL: SMOKE_ORIGIN,
  viewport: { width: 420, height: 875 },
});
const post = async (path, data, key) => {
  const r = await page.request.post("/api/" + path, {
    data,
    headers: key ? { "Idempotency-Key": key } : {},
  });
  assert.ok(r.ok(), path + " " + r.status() + " " + (await r.text()));
  return r.status() === 204 ? null : r.json();
};
const get = async (path) => (await page.request.get("/api/" + path)).json();
const cases = [];
try {
  await post("login", {
    email: "review@sample.invalid",
    password: BROWSER_PASSWORD,
  });
  const model = await post("resources", {
    name: "资料问答 " + mode,
    endpoint: QUICK_ENDPOINT,
    model: real ? "local-course-qwen" : "source-lookup-fixture",
    key: "synthetic",
  });
  const name = real ? "规程核对助手" : "原文核对助手";
  const agent = await post("agents", {
    name,
    instructions:
      "依据已授权的资料回答业务问题，资料不足时请用户补充。按照本次任务指定的输出格式回答。",
    resource: model.id,
    resourceRevision: 1,
  });
  const trial = await post(
    "agents/" + agent.id + "/validate",
    {
      revision: 1,
      input: "请只回复两个字：收到",
      expected: real ? "收到" : "RECEIPT",
      authorizeOneCall: true,
    },
    "lookup-trial-" + mode,
  );
  assert.equal(trial.status, "passed");
  await post("agents/" + agent.id + "/publish", { revision: 1 });
  const documentAgent = await post("agents/tools", {
    name: "检索交付成稿 " + mode,
    description: "将上一步的完整回答及依据排成 Word",
    tool: { operation: "docx" },
  });
  assert.equal(
    (
      await post("agents/" + documentAgent.id + "/validate", {
        revision: 1,
        input: "交付原件",
        expected: "交付原件",
        authorizeOneCall: true,
      })
    ).status,
    "passed",
  );
  await post("agents/" + documentAgent.id + "/publish", { revision: 1 });
  const invitation = await post("admin/invitations", {});
  await post("logout", {});
  await post("register", {
    token: invitation.token,
    name: "交接员",
    email: "lookup-" + mode + "@sample.invalid",
    password: BROWSER_PASSWORD,
  });
  await page.goto("/");
  let source =
    "📋 交接柜台使用蓝色标签。\n冷链交接时由值班主管核对铅封编号，温度超过 8°C 时暂停接收。\n茶水间备用柜的编号为 PRIVATE-879。";
  async function run(
    question,
    documents = [{ name: "交接规程.txt", text: source }],
    includeWord = false,
    calculate = false,
    supplement = false,
  ) {
    await page.getByRole("button", { name: "选择或创建 Agent" }).click();
    const dialog = page.getByRole("dialog", { name: "Agent", exact: true });
    await dialog.getByLabel("搜索 Agent").fill(name);
    await dialog.getByRole("button", { name: new RegExp(name) }).click();
    await page
      .getByRole("textbox", { name: "消息", exact: true })
      .fill(question);
    await page
      .locator("input[type=file]")
      .last()
      .setInputFiles(
        documents.map((doc) => ({
          name: doc.name,
          mimeType: "text/plain",
          buffer: Buffer.from(doc.text),
        })),
      );
    await page.getByLabel(/检索原文后回答/).check();
    if (calculate) await page.getByLabel("允许按原文金额求和或算差额").check();
    await page.getByRole("button", { name: "发送消息" }).click();
    await page.getByRole("heading", { name: "协作方案 v1" }).waitFor();
    const path = "projects/" + new URL(page.url()).hash.slice(1);
    if (includeWord) {
      await page.locator("summary").filter({ hasText: "拟定协作方案" }).click();
      await page.getByLabel("添加执行步骤").selectOption(documentAgent.id);
      assert.ok(await page.getByLabel(/检索原文后回答/).isChecked());
      await page.getByRole("button", { name: "保存待确认方案" }).click();
      await page.getByRole("heading", { name: "协作方案 v2" }).waitFor();
    }
    if (calculate)
      await page.getByText(/从引文选择 2 至 6 项同币种金额/).waitFor();
    const plan = await get(path);
    assert.deepEqual(
      plan.plan.nodes.map((n) => [n.agent, n.lookupPhase, n.dependsOn]),
      [
        [agent.id, "search", []],
        [agent.id, calculate ? "answer_calc" : "answer", [1]],
        ...(includeWord ? [[documentAgent.id, "", [2]]] : []),
      ],
    );
    const denied = await page.request.post("/api/" + path + "/runs", {
      data: { expected: plan.revision, authorizeCalls: 1 },
      headers: { "Idempotency-Key": "too-few-" + plan.id },
    });
    assert.ok(!denied.ok());
    if (calculate && !supplement)
      await page.getByLabel("缺少金额时等待我补件").uncheck();
    await page.getByLabel(includeWord ? /授权本次 3/ : /授权本次 2/).check();
    await page.getByRole("button", { name: "确认并执行" }).click();
    if (supplement)
      return checkCalculationReceipt({
        page,
        get,
        path,
        before: plan,
        directory: VISUAL_EVIDENCE,
        real,
      });
    await page
      .getByRole("button", { name: "审核此版本" })
      .first()
      .waitFor({ timeout: 100000 });
    let p = await get(path);
    assert.equal(p.stage, "review");
    const [evidence, answer] = p.artifacts.sort(
      (a, b) => a.ordinal - b.ordinal,
    );
    if (includeWord) {
      const doc = p.artifacts[2];
      assert.equal(doc.body, answer.body);
      const file = await page.request.get(
        "/api/" + path + "/artifacts/" + doc.id + "/file?version=1",
      );
      assert.ok(file.ok());
      const fileName =
        "agent-source-lookup-" +
        mode +
        (p.run.attempts.length === 2 ? "-missing" : "-purchasing") +
        ".docx";
      await writeFile(VISUAL_EVIDENCE + "/" + fileName, await file.body());
    }
    cases.push({
      question,
      search: evidence.body,
      answer: answer.body,
      attempts: p.run.attempts.map((i) => ({
        step: i.step,
        status: i.status,
        tokens: i.tokens,
      })),
    });
    await writeFile(
      VISUAL_EVIDENCE + "/agent-source-lookup-" + mode + ".json",
      JSON.stringify(cases, null, 2),
    );
    assert.doesNotMatch(answer.body, /PRIVATE-879/);
    return { p, path, answer, evidence };
  }
  async function verifyExistingLookup() {
    const hit = await run(
      "冷链交接由谁核对铅封编号？温度超过多少时要暂停接收？",
    );
    assert.match(hit.evidence.body, /【S1v1:\d+-\d+】冷链交接/);
    assert.match(hit.answer.body, /值班主管/);
    assert.match(hit.answer.body, /8\s*°?C/);
    assert.match(hit.answer.body, /原文依据/);
    const citation = page
      .getByRole("link", { name: /查看交接规程.txt v1 原件/ })
      .last();
    const download = await page.request.get(
      await citation.getAttribute("href"),
    );
    assert.equal(await download.text(), source);

    await page.getByRole("button", { name: "审核此版本" }).first().click();
    await page.getByRole("button", { name: "审核此版本" }).first().click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    await page.waitForFunction(() =>
      document.body.textContent.includes("已交付"),
    );
    assert.equal((await get(hit.path)).stage, "delivered");
    await page.screenshot({
      path: VISUAL_EVIDENCE + "/agent-source-lookup-" + mode + ".png",
    });
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    const miss = await run("报销住宿费的上限是多少？");
    assert.match(miss.answer.body, /补充/);
    assert.equal(miss.p.costSummary.calls, 1);
    assert.equal(miss.p.run.attempts.length, 1);
    assert.doesNotMatch(miss.answer.body, /9999|原文依据/);
    await page.getByRole("button", { name: "新对话", exact: true }).click();
    const purchasing = await run(
      "南仓和北仓各自的加急采购审批门槛是多少？分别注明来自哪份规程。",
      [
        {
          name: "南仓采购规程.txt",
          text: "南仓加急采购达到 1200 元，须由主管审批。",
        },
        {
          name: "北仓采购规程.txt",
          text: "北仓加急采购达到 3500 元，须由主管审批。",
        },
      ],
      true,
    );
    const claim = purchasing.answer.body.split("原文依据")[0];
    assert.match(claim, /南仓[^。；\n]{0,60}1[,]?200/);
    assert.match(claim, /北仓[^。；\n]{0,60}3[,]?500/);
    assert.match(purchasing.answer.body, /南仓采购规程.txt v1 【S/);
    assert.match(purchasing.answer.body, /北仓采购规程.txt v1 【S/);
    assert.equal(purchasing.p.costSummary.calls, 3);
    await page
      .getByRole("link", { name: /查看南仓采购规程.txt v1 原件/ })
      .last()
      .waitFor();
    await page
      .getByRole("link", { name: /查看北仓采购规程.txt v1 原件/ })
      .last()
      .waitFor();
    await page
      .locator("article")
      .filter({ has: page.getByRole("heading", { name: /据原文回答/ }) })
      .screenshot({
        path:
          VISUAL_EVIDENCE + "/agent-source-lookup-" + mode + "-citations.png",
      });
    for (let step = 0; step < 3; step++)
      await page.getByRole("button", { name: "审核此版本" }).first().click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    await page.waitForFunction(() =>
      document.body.textContent.includes("已交付"),
    );
    const archive = await page.request.get(
      "/api/" + purchasing.path + "/delivery/package",
    );
    assert.ok(archive.ok());
    await writeFile(
      VISUAL_EVIDENCE + "/agent-source-lookup-" + mode + "-delivery.zip",
      await archive.body(),
    );

    if (!real) {
      await page.getByRole("button", { name: "新对话", exact: true }).click();
      source += "\n冷链还需按备用单操作。\n备用单第 3 条要求填写批次 B-761。";
      const missingDoc = await run(
        "报销住宿费的上限是多少？请交接结果。",
        undefined,
        true,
      );
      assert.equal(missingDoc.p.costSummary.calls, 2);
      assert.match(missingDoc.p.artifacts[2].body, /补充相关资料/);
      await page.getByRole("button", { name: "新对话", exact: true }).click();
      const more = await run("补查场景：冷链交接还需要核对哪张单据的条款？");
      assert.match(more.answer.body, /追加检索/);
      assert.match(more.answer.body, /B-761/);
      assert.doesNotMatch(more.evidence.body, /B-761/);
      assert.equal(more.p.costSummary.calls, 2);
      await page.getByRole("button", { name: "新对话", exact: true }).click();
      const wrongValue = await run(
        "冷链交接时的温度界限是多少？请做数值核验。",
      );
      assert.match(wrongValue.answer.body, /原文无法对应/);
      assert.doesNotMatch(wrongValue.answer.body, /18°C/);
      assert.match(wrongValue.answer.body, /8°C/);
      await page.getByRole("button", { name: "新对话", exact: true }).click();
      const invalid = await run("请核对冷链交接，同时测试检索指令格式。");
      assert.match(invalid.answer.body, /有效的检索指令/);
      assert.doesNotMatch(invalid.answer.body, /没有找到可引用的原文/);
      assert.equal(invalid.p.run.attempts.length, 1);
      await page.getByRole("button", { name: "新对话", exact: true }).click();
      const bad = await run("冷链交接由谁核对铅封？做越界引用核验。");
      assert.match(bad.answer.body, /引用编号无法核对/);
      assert.doesNotMatch(bad.answer.body, /伪造结论/);
    }
  }
  if (process.env.LOOKUP_CASE !== "receipt") {
    if (process.env.LOOKUP_CASE !== "calculation") await verifyExistingLookup();
    await checkSourceCalculation({
      run,
      page,
      get,
      real,
      directory: VISUAL_EVIDENCE,
    });
  }
  const fresh = page.getByRole("button", { name: "新对话", exact: true });
  if (await fresh.isVisible()) await fresh.click();
  await run(
    "住宿结算实付比单晚住宿报销标准超出多少元？列出差额和原文依据。",
    [{ name: "差旅标准.txt", text: "单晚住宿报销标准为 450 元。" }],
    true,
    true,
    true,
  );
  await checkServiceReceipts(page, agent.id, real, VISUAL_EVIDENCE);
} finally {
  await browser.close();
}

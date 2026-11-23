import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkSourceCalculation({
  run,
  page,
  get,
  real,
  directory,
}) {
  const checks = [];
  const stay = { name: "住宿结算.txt", text: "本次住宿结算实付 528.50 元。" };
  const rule = { name: "差旅标准.txt", text: "单晚住宿报销标准为 450 元。" };
  const samples = [
    {
      question:
        "住宿结算实付比单晚住宿报销标准超出多少元？列出差额和原文依据。",
      files: [stay, rule],
      expected: /差额为 78\.50 元/,
      values: ["528.50 元", "450 元"],
    },
    {
      question: "两张运费收据合计多少元？列出算式和两处原文。",
      files: [
        { name: "去程收据.txt", text: "去程运费为 16.85 元。" },
        { name: "返程收据.txt", text: "返程运费为 9.20 元。" },
      ],
      expected: /合计为 26\.05 元/,
      values: ["16.85 元", "9.20 元"],
    },
    {
      question:
        "请核对住宿结算实付比单晚住宿报销标准超出多少元？金额未齐时说明还缺什么。",
      files: [
        {
          name: "住宿结算.txt",
          text: "本次住宿实付金额等酒店退房后才能确认。",
        },
        rule,
      ],
      calls: 1,
      expected: /金额不足两项/,
    },
  ];
  if (!real)
    samples.push(
      {
        question: "两张运费收据合计多少？",
        files: [
          { name: "本地收据.txt", text: "运费为 20 元。" },
          { name: "海外收据.txt", text: "运费为 5 美元。" },
        ],
        calls: 1,
        expected: /金额币种不同/,
      },
      {
        question: "住宿结算实付比单晚标准超出多少？重复取数核验。",
        files: [stay, rule],
        expected: /无法按这些引文核实计算口径/,
      },
    );
  if (!real)
    samples.push({
      question: "两张运费收据合计多少元？把带负号的退款也算在内。",
      files: [
        { name: "结算.txt", text: "运费为 9,876,543,210,987.09 元。" },
        { name: "退款.txt", text: "运费调整为 -0.19 元。" },
      ],
      values: ["9,876,543,210,987.09 元", "-0.19 元"],
      expected: /合计为 9876543210986\.90 元/,
    });
  if (!real)
    samples.push(
      {
        question: "两张运费收据合计多少元？请核对带前缀的币种。",
        files: [
          { name: "甲.txt", text: "运费为港币 600 元。" },
          { name: "乙.txt", text: "运费为人民币 90 元。" },
        ],
        calls: 1,
        expected: /金额币种不同/,
      },
      {
        question: "两张运费收据合计多少元？原件含角分。",
        files: [
          { name: "甲.txt", text: "运费为 3 元 5 角。" },
          { name: "乙.txt", text: "运费为 7 元。" },
        ],
        calls: 1,
        expected: /金额不足两项/,
      },
    );
  const mode = real ? "local" : "fixture";
  for (const [i, sample] of samples.entries()) {
    const newChat = page.getByRole("button", { name: "新对话", exact: true });
    if (await newChat.isVisible()) await newChat.click();
    const started = Date.now();
    const { answer, path, p } = await run(
      sample.question,
      sample.files,
      false,
      true,
    );
    const entry = {
      question: sample.question,
      answer: answer.body,
      run: p.run.id,
      calls: p.costSummary.calls,
      elapsedToReviewMs: Date.now() - started,
    };
    checks.push(entry);
    await writeFile(
      directory + "/source-calculation-" + mode + ".json",
      JSON.stringify(checks, null, 2),
    );
    assert.match(answer.body, sample.expected);
    assert.equal(entry.calls, sample.calls ?? 2);
    if (!sample.values) {
      assert.doesNotMatch(answer.body, /差额为 |合计为 /);
      continue;
    }
    const [formula, citations] = answer.body.split("原文依据");
    for (const value of sample.values) assert.ok(formula.includes(value));
    for (const file of sample.files) {
      assert.ok(citations.includes(file.name + " v1"));
      const link = page
        .getByRole("link", { name: "查看" + file.name + " v1 原件" })
        .last();
      assert.equal(
        await (await page.request.get(await link.getAttribute("href"))).text(),
        file.text,
      );
    }
    if (i < 2)
      await page
        .locator("article")
        .filter({ has: page.getByRole("heading", { name: /原文与金额核算/ }) })
        .screenshot({
          path:
            directory + "/source-calculation-" + mode + "-" + (i + 1) + ".png",
        });
    await page.getByRole("button", { name: "审核此版本" }).first().click();
    await page.getByRole("button", { name: "审核此版本" }).first().click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    await page.waitForFunction(() =>
      document.body.textContent.includes("已交付"),
    );
    assert.equal((await get(path)).stage, "delivered");
    const zip = await page.request.get("/api/" + path + "/delivery/package");
    assert.ok(zip.ok());
    await writeFile(
      directory + "/source-calculation-" + mode + "-" + (i + 1) + ".zip",
      await zip.body(),
    );
  }
  if (real && process.env.LOOKUP_COMPARE === "1") {
    const comparison = [];
    for (const [index, sample] of samples.slice(0, 2).entries()) {
      await page.getByRole("button", { name: "新对话", exact: true }).click();
      const started = Date.now();
      const { answer, p } = await run(
        sample.question,
        sample.files,
        false,
        false,
      );
      comparison.push({
        question: sample.question,
        expected: index === 0 ? "78.50" : "26.05",
        agent: p.plan.nodes[0].agent,
        revision: p.plan.nodes[0].revision,
        baseline: {
          phase: p.plan.nodes[1].lookupPhase,
          answer: answer.body,
          run: p.run.id,
          calls: p.costSummary.calls,
          elapsedToReviewMs: Date.now() - started,
        },
        calculation: checks[index],
      });
      await writeFile(
        directory + "/source-calculation-baseline.json",
        JSON.stringify(comparison, null, 2),
      );
    }
  }
}

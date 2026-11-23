import {checkPlanningImage} from "./planning-image-gap.mjs";
import {checkMultipleFiles} from "./planning-multiple-files.mjs";
import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";

// The team already has a Word formatter. A Word draft or an unrelated
// verified Agent must not satisfy a requested Excel deliverable.
export async function checkPlanningOutput(member, admin, out, ctx, real) {
  const { agent: word, resource } = ctx;
  const resultName = "planning-output-" + (real ? "local" : "fixture");
  await member.getByRole("button", { name: "新对话", exact: true }).click();
  await member
    .getByRole("textbox", { name: "消息", exact: true })
    .fill(
      (real ? "" : "OUTPUT_FORMAT_PROBE ") +
        "请把已确认的物品清单逐行原样写进可编辑的 Excel 工作簿，每行一格，保留前导零。",
    );
  const source = "00017\n投影仪";
  await member
    .locator("input[type=file]")
    .last()
    .setInputFiles({
      name: "盘点明细.txt",
      mimeType: "text/plain",
      buffer: Buffer.from(source),
    });
  await member.getByRole("button", { name: "发送消息" }).click();
  await member.getByLabel("规划助手模型").selectOption(resource);
  if (real) {
    await member.getByLabel(/授权一次规划调用/).check();
    const pending = member.waitForResponse(
      (r) =>
        r.url().endsWith("/suggestions") && r.request().method() === "POST",
    );
    await member.getByRole("button", { name: "请助手规划" }).click();
    const natural = await (await pending).json();
    await writeFile(
      out + "/planning-output-natural.json",
      JSON.stringify(natural, null, 2),
    );
    assert.equal(natural.status, "ready", JSON.stringify(natural));
    assert.equal(natural.result.outputFormat, "xlsx", JSON.stringify(natural));
    assert.deepEqual(natural.result.agents || [], []);
    assert.deepEqual(natural.result.missing, [natural.result.outputGap]);
    await member.reload();
    const toggle = member.getByText("根据对话推荐方案或直接回答", {
      exact: true,
    });
    if (!(await member.getByLabel("规划助手模型").isVisible()))
      await toggle.click();
  }
  await member.getByText("指定交付文件格式", { exact: true }).click();
  await member.getByRole("checkbox",{name:"Excel 工作簿（XLSX）"}).check();
  await member.getByLabel(/授权一次规划调用/).check();
  const reply = member.waitForResponse(
    (r) => r.url().endsWith("/suggestions") && r.request().method() === "POST",
  );
  await member.getByRole("button", { name: "请助手规划" }).click();
  const response = await reply;
  const suggestion = await response.json();
  assert.equal(suggestion.status, "ready", JSON.stringify(suggestion));
  const result = suggestion.result;
  assert.equal(result.outputFormat, "xlsx");
  assert.deepEqual(result.missing, [result.outputGap], JSON.stringify(result));
  assert.ok(result.outputGap);
  assert.deepEqual(
    result.agents || [],
    [],
    "原文已备好，不能追加无关的 Word 排版",
  );
  const project = new URL(member.url()).hash.slice(1);
  const route = "/api/projects/" + project;
  const read = async () => (await member.request.get(route)).json();
  const before = await read();
  const gap = member
    .getByRole("heading", { name: "待补能力 · " + result.outputGap })
    .locator("..");
  const wrong = await member.request.put(
    route + "/suggestions/" + suggestion.id + "/gaps/0",
    {
      data: {
        mode: "reuse",
        expected: before.revision,
        bindingRevision: 0,
        agent: word,
        revision: 1,
      },
    },
  );
  assert.equal(wrong.status(), 422, await wrong.text());
  const bind = route + "/suggestions/" + suggestion.id + "/gaps/0";
  const list = async () => (await member.request.get("/api/agents")).json();
  const count = (await list()).length;
  const rejectedTool = await member.request.put(bind, {
    data: {
      mode: "tool",
      expected: before.revision,
      bindingRevision: 0,
      draft: { name: "不适合本次明细的计算", instructions: "需要 CSV" },
      tool: {
        operation: "aggregate",
        config: { group: "部门", amount: "金额" },
      },
      example: "部门,金额\nA,2",
      expectedOutput: "2",
    },
  });
  assert.equal(rejectedTool.status(), 422, await rejectedTool.text());
  assert.equal((await list()).length, count);
  const wrongPlan = await member.request.post(route + "/plan", {
    data: {
      expected: before.revision,
      suggestion: suggestion.id,
      brief: result.text,
      sources: before.suggestion.inputSources,
      agents: [word],
      agentRevisions: [1],
    },
  });
  assert.equal(wrongPlan.status(), 422, await wrongPlan.text());
  const call = response.request();
  const key = await call.headerValue("idempotency-key");
  for (const [format, status] of [
    ["xlsx", 200],
    ["docx", 409],
  ]) {
    const duplicate = await member.request.post(call.url(), {
      headers: { "Idempotency-Key": key },
      data: { ...call.postDataJSON(), requiredOutput: format },
    });
    assert.equal(duplicate.status(), status, await duplicate.text());
    if (status === 200)
      assert.equal((await duplicate.json()).id, suggestion.id);
  }
  assert.equal((await read()).revision, before.revision);
  assert.equal((await read()).costSummary.calls, real ? 2 : 1);
  await member.reload();
  await gap.getByLabel("补齐方式").selectOption("tool");
  assert.deepEqual(
    await gap
      .getByLabel("工具操作")
      .locator("option")
      .evaluateAll((opts) => opts.map((o) => o.value)),
    ["", "xlsx"],
  );
  await gap.getByLabel("工具操作").selectOption("xlsx");
  await gap.getByLabel("草稿名称").fill("盘点逐行交付");
  await gap
    .getByLabel("输入、处理规则与交付要求")
    .fill("保留编号和文本，每行写入一个单元格。");
  await gap.getByLabel("合成试运行样例").fill("00019\n样例纸箱");
  await gap.getByLabel("预期产出应包含").fill("00019");
  await gap.getByRole("heading").scrollIntoViewIfNeeded();
  await member.screenshot({ path: out + "/" + resultName + "-gap.png" });
  const fits = await gap.evaluate(
    (el) =>
      el.scrollWidth <= el.clientWidth + 1 &&
      document.documentElement.scrollWidth <= innerWidth,
  );
  assert.ok(fits, "补齐界面应适应手机宽度");
  const bound = member.waitForResponse(
    (r) => r.request().method() === "PUT" && r.url().endsWith("/gaps/0"),
  );
  await gap.getByRole("button", { name: "保存为待验证草稿" }).click();
  const agent = (await (await bound).json()).agent;
  assert.ok(agent);
  await gap.getByText("验证并发布此草稿").click();
  await gap.getByLabel(/授权一次试运行/).check();
  await gap.getByRole("button", { name: "执行缺口试运行" }).click();
  await gap.getByText(/已验证，待授权发布/).waitFor();
  const released = await admin.request.post(
    "/api/agents/" + agent + "/publish",
    { data: { revision: 1 } },
  );
  assert.equal(released.status(), 204);
  await member.reload();
  await member.getByRole("button", { name: "采用为待确认方案" }).click();
  await member.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const planned = await read();
  assert.equal(planned.plan.nodes.at(-1).operation, "xlsx");
  assert.deepEqual(
    planned.plan.sources.map((s) => [s.id, s.version]),
    before.suggestion.inputSources.map((s) => [s.id, s.version]),
  );
  await member.getByLabel(/授权本次/).check();
  await member.getByRole("button", { name: "确认并执行" }).click();
  await member.getByRole("button", { name: "审核此版本" }).first().waitFor();
  for (let i = 0; i < planned.plan.nodes.length; i++) {
    await member.getByRole("button", { name: "审核此版本" }).first().click();
  }
  await member.getByRole("button", { name: "确认并固定以上版本" }).click();
  const link = member.getByRole("link", { name: "下载交付包（ZIP）" });
  await link.waitFor();
  const bytes = await member.request.get(await link.getAttribute("href"));
  assert.ok(bytes.ok());
  await writeFile(out + "/" + resultName + ".zip", await bytes.body());
  const delivered = await read();
  assert.equal(delivered.stage, "delivered");
  assert.equal(
    delivered.costSummary.calls,
    (real ? 3 : 2) + planned.plan.nodes.length,
  );
  await writeFile(
    out + "/" + resultName + ".json",
    JSON.stringify(
      {
        model: real ? "Qwen3-4B" : "controlled",
        nonCsvAggregate: rejectedTool.status(),
        wrongReuse: wrong.status(),
        wrongPlan: wrongPlan.status(),
        differentFormatReplay: 409,
        calls: delivered.costSummary.calls,
        chosen: planned.plan.nodes.map((n) => n.operation),
        suggested: result,
        run: delivered.run.id,
        required: before.suggestion.requiredOutput,
      },
      null,
      2,
    ),
  );
  if (real) {
    const describe = async (goal, filename, bytes, expectedFormat) => {
      const created = await member.request.post("/api/projects", {
        data: { goal, team: before.team },
      });
      assert.ok(created.ok(), await created.text());
      const path = "/api/projects/" + (await created.json()).id;
      const upload = await member.request.post(
        path + "/sources?name=" + encodeURIComponent(filename),
        {
          headers: { "Content-Type": "application/octet-stream" },
          data: bytes,
        },
      );
      assert.ok(upload.ok(), await upload.text());
      const input = (await upload.json()).id;
      const state = await (await member.request.get(path)).json();
      const reply = await member.request.post(path + "/suggestions", {
        headers: { "Idempotency-Key": "planner-" + crypto.randomUUID() },
        data: {
          resource,
          revision: 1,
          expected: state.revision,
          sources: [{ id: input, version: 1 }],
          authorizeOneCall: true,
        },
      });
      const observed = await reply.json();
      assert.equal(observed.status, "ready", JSON.stringify(observed));
      assert.equal(
        observed.result.outputFormat || "",
        expectedFormat,
        JSON.stringify(observed),
      );
      return observed.result;
    };
    const reuse = await describe(
      "把清点结果逐行写入可编辑 Excel，每行一格，保留前导零。",
      "清点.txt",
      Buffer.from(source),
      "xlsx",
    );
    assert.deepEqual(reuse.agents, [agent]);
    assert.deepEqual(reuse.missing || [], []);
    // Re-use the actual Word produced in the preceding project as an input.
    const sourceOnly = await describe(
      "请阅读附件中的 Word 会议议程，以纯文本回答签到时间并给出依据。",
      "会议议程.docx",
      await readFile(out + "/quick-gap-local-trial.docx"),
      "text",
    );
    assert.deepEqual(sourceOnly.agents || [], []);
    assert.deepEqual(sourceOnly.missing, [sourceOnly.outputGap]);
    const both = await describe(
      "使用已确认的领用清单，分别交付两份独立文件：第一份为保留原文的 Word，第二份为每行一个物品的 Excel。",
      "领用.txt",
      Buffer.from(source),
      "",
    );
    assert.deepEqual(
      new Set(both.agents),
      new Set([word, agent]),
      JSON.stringify(both),
    );
    assert.deepEqual(both.missing || [], []);
    assert.deepEqual(new Set(both.outputFormats),new Set(["docx","xlsx"]));
    await writeFile(
      out + "/planning-output-reuse.json",
      JSON.stringify({ reuse, sourceOnly, both }, null, 2),
    );
  }
  if (!real) {
    const created = await member.request.post("/api/projects", {
      data: { team: before.team, goal: "OUTPUT_DOUBLE_PROBE 整理成 Excel。" },
    });
    assert.ok(created.ok());
    const route = "/api/projects/" + (await created.json()).id;
    const checkpoint = await (await member.request.get(route)).json();
    const headers = { "Idempotency-Key": "duplicate-object-reply" };
    const body = {
      resource,
      revision: 1,
      expected: checkpoint.revision,
      authorizeOneCall: true,
      requiredOutput: "xlsx",
    };
    for (let attempt = 0; attempt < 2; attempt++) {
      const response = await member.request.post(route + "/suggestions", {
        headers,
        data: body,
      });
      const result = await response.json();
      assert.equal(result.status, "invalid");
      assert.equal(result.result ?? null, null);
    }
    const rejected = await (await member.request.get(route)).json();
    assert.equal(rejected.costSummary.calls, 1);
    assert.equal(rejected.plan, null);
    await writeFile(
      out + "/planning-output-invalid.json",
      JSON.stringify({
        state: rejected.suggestion.status,
        calls: 1,
        plan: rejected.plan,
      }),
    );
  }
  await checkMultipleFiles(member,out,{resource,word,sheet:agent},real);
  await checkPlanningImage(member,admin,out,resource,word,real);
}

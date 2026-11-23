import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkMultipleFiles(
  member,
  out,
  { resource, word, sheet },
  real,
) {
  const mode = real ? "local" : "fixture";
  const stem = out + "/planning-deliverables-" + mode;
  const goal =
    "按已确认的领用清单分别制作 Word 和 Excel。两份都保留完整原文，Excel 每行一个物品，保留前导零。";
  await member.goto("/");
  await member
    .getByRole("textbox", { name: "消息", exact: true })
    .fill((real ? "" : "MULTI_FILE_SCOPE ") + goal);
  await member
    .locator("input[type=file]")
    .last()
    .setInputFiles({
      name: "领用原件.txt",
      mimeType: "text/plain",
      buffer: Buffer.from("00037\n交接工具箱"),
    });
  await member.getByRole("button", { name: "发送消息" }).click();
  await member.getByLabel("规划助手模型").selectOption(resource);
  await member.getByText("指定交付文件格式", { exact: true }).click();
  const fileOption = (name) => member.getByRole("checkbox", { name });
  await fileOption("Word 文档（DOCX）").check();
  await fileOption("Excel 工作簿（XLSX）").check();
  await member.getByLabel(/授权一次规划调用/).check();
  const responsePromise = member.waitForResponse(
    (r) => r.url().endsWith("/suggestions") && r.request().method() === "POST",
  );
  await member.getByRole("button", { name: "请助手规划" }).click();
  const response = await responsePromise,
    proposal = await response.json();
  assert.equal(proposal.status, "ready", JSON.stringify(proposal));
  assert.deepEqual(proposal.result.outputFormats, ["docx", "xlsx"]);
  assert.equal(proposal.result.outputFormat, undefined);
  const route = "/api/projects/" + new URL(member.url()).hash.slice(1);
  const read = async () => (await member.request.get(route)).json();
  const project = await read();
  assert.deepEqual(project.suggestion.requiredOutputs, ["docx", "xlsx"]);
  const key = await response.request().headerValue("idempotency-key");
  for (const [formats, status] of [
    [["xlsx", "docx"], 200],
    [["docx"], 409],
    [["exe", "docx"], 422],
  ]) {
    const replay = await member.request.post(response.url(), {
      headers: { "Idempotency-Key": key },
      data: { ...response.request().postDataJSON(), requiredOutputs: formats },
    });
    assert.equal(replay.status(), status, await replay.text());
    if (status === 200) assert.equal((await replay.json()).id, proposal.id);
  }
  await member.reload();
  await member.getByText("根据对话推荐方案或直接回答", { exact: true }).click();
  await member.getByText("指定交付文件格式", { exact: true }).click();
  assert.ok(await fileOption("Word 文档（DOCX）").isChecked());
  assert.ok(await fileOption("Excel 工作簿（XLSX）").isChecked());
  const producers = { docx: word, xlsx: sheet };
  if (!real)
    assert.equal(Object.keys(proposal.result.outputGaps || {}).length, 2);
  for (const [gap, format] of Object.entries(
    proposal.result.outputGaps || {},
  )) {
    const card = member.getByRole("region", { name: "能力补齐 · " + gap });
    const field = card.getByLabel("已发布能力");
    const other = format === "docx" ? sheet : word;
    assert.equal(
      await field.locator('option[value="' + other + '"]').count(),
      0,
    );
    const slot = proposal.result.missing.indexOf(gap);
    const wrong = await member.request.put(
      route + "/suggestions/" + proposal.id + "/gaps/" + slot,
      {
        data: {
          mode: "reuse",
          expected: project.revision,
          bindingRevision: 0,
          agent: other,
          revision: 1,
        },
      },
    );
    assert.equal(wrong.status(), 422, await wrong.text());
    await field.selectOption(producers[format]);
    await card.getByRole("button", { name: "绑定所选版本" }).click();
    await card.getByRole("heading", { name: "已补齐 · " + gap }).waitFor();
  }
  for (const producer of [word, sheet]) {
    const rejected = await member.request.post(route + "/plan", {
      data: {
        expected: project.revision,
        suggestion: proposal.id,
        brief: goal,
        sources: project.suggestion.inputSources,
        agents: [producer],
        agentRevisions: [1],
      },
    });
    assert.equal(rejected.status(), 422, await rejected.text());
  }
  assert.equal((await read()).costSummary.calls, 1);
  for (const label of ["Word 文档（DOCX）", "Excel 工作簿（XLSX）"]) {
    await member.getByText("产出 · " + label, { exact: true }).waitFor();
  }
  await member.getByLabel("各文件分别使用已选原件制作").check();
  const adopt = member.getByRole("button", { name: "采用为待确认方案" });
  await member.getByText("交付成果", { exact: true }).scrollIntoViewIfNeeded();
  await member.screenshot({ path: stem + "-proposal.png" });
  await adopt.click();
  await member.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const plan = (await read()).plan;
  assert.deepEqual(
    new Set(plan.nodes.map((n) => n.operation)),
    new Set(["docx", "xlsx"]),
  );
  await member.getByLabel(/授权本次/).check();
  await member.getByRole("button", { name: "确认并执行" }).click();
  for (let i = 0; i < 2; i++)
    await member.getByRole("button", { name: "审核此版本" }).first().click();
  await member.getByRole("button", { name: "确认并固定以上版本" }).click();
  const link = member.getByRole("link", { name: "下载交付包（ZIP）" });
  await link.waitFor();
  const archive = await member.request.get(await link.getAttribute("href"));
  assert.ok(archive.ok());
  await writeFile(stem + ".zip", await archive.body());
  const delivered = await read();
  assert.equal(delivered.stage, "delivered");
  assert.equal(delivered.costSummary.calls, 3);
  await writeFile(
    stem + ".json",
    JSON.stringify(
      {
        mode,
        goal,
        formats: proposal.result.outputFormats,
        gaps: proposal.result.outputGaps || {},
        proposal: proposal.id,
        plan: plan.nodes,
        run: delivered.run.id,
        calls: delivered.costSummary.calls,
      },
      null,
      2,
    ),
  );
}

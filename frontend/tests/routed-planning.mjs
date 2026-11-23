import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkRoutedPlanning({
  admin, member, request, waitForState, model, candidates, csv, fileName, out, mode,
}) {
  const real = mode === "local";
  const goal = "请在已发布的按地区和按部门两种汇总口径中，根据本次用途自动选一种，只执行一次汇总并交付 Excel。本次报告要比较各部门的费用，以部门作为本次分析维度。";
  async function suggest(question, duringRequest) {
    await member.goto("/");
    await member.getByRole("textbox", { name: "消息", exact: true }).fill(question);
    await member.locator(".composer input[type=file]").setInputFiles({
      name: fileName,
      buffer: Buffer.from(csv),
      mimeType: "text/csv",
    });
    await member.getByRole("button", { name: "发送消息" }).click();
    const planner = member.getByText("根据对话推荐方案或直接回答", { exact: true });
    if (!(await planner.evaluate((el) => el.parentElement.open))) await planner.click();
    await member.getByLabel(fileName, { exact: false }).filter({ visible: true }).first().waitFor();
    await member.getByLabel("规划助手模型").selectOption(model.id);
    await member.getByLabel(/授权一次规划调用/).check();
    const response = member.waitForResponse(
      (r) => r.request().method() === "POST" && r.url().endsWith("/suggestions"),
    );
    await member.getByRole("button", { name: "请助手规划" }).click();
    const project = new URL(member.url()).hash.slice(1);
    const path = "projects/" + project;
    if (duringRequest) await duringRequest(path);
    const received = await response;
    assert.equal(received.status(), 200, await received.text());
    const result = await received.json();
    return { result, project, path };
  }

  const main = await suggest(goal);
  const result = main.result;
  assert.equal(result.status, "ready", JSON.stringify(result));
  assert.equal(result.result.outputFormat, "xlsx");
  const route = result.result.toolChoice;
  assert.ok(route, JSON.stringify(result));
  assert.equal(result.result.agents.length, 2);
  assert.equal(route.step, 2);
  assert.equal(route.selector, 1);
  assert.deepEqual([route.primaryScope.group, route.alternativeScope.group].sort(), ["地区", "部门"].sort());
  const preview = member.getByRole("region", { name: "建议自动选择汇总口径" });
  await preview.getByText(/按「部门」分组/).waitFor();
  await preview.getByText(/按「地区」分组/).waitFor();
  await preview.screenshot({ path: out + "/routed-planning-" + mode + ".png" });
  await member.getByRole("button", { name: "采用为待确认方案" }).click();
  await member.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const saved = await request(member, main.path);
  assert.equal(saved.plan.nodes[1].toolChoice.agent, route.alternative);
  assert.equal(saved.plan.nodes[1].toolChoice.revision, route.revision);
  assert.equal(saved.costSummary.calls, 1);
  await member.getByLabel(/授权本次 2/).check();
  await member.getByRole("button", { name: "确认并执行" }).click();
  const ready = await waitForState(member, main.path, (value) => value.stage === "review");
  assert.equal(ready.costSummary.calls, 3);
  const trace = await request(member, main.path + "/runs/" + ready.run.id + "/trace");
  assert.equal(trace.trace.spans[1].agent, candidates[1].id);
  assert.ok(ready.artifacts[1].body.includes("培训 · 10.35"), ready.artifacts[1].body);
  const reviews = member.getByRole("button", { name: "审核此版本" });
  await reviews.first().click();
  await reviews.first().click();
  await member.getByRole("button", { name: "确认并固定以上版本" }).click();
  const link = member.getByRole("link", { name: "下载交付包（ZIP）" });
  await link.waitFor();
  const archive = await member.request.get(await link.getAttribute("href"));
  assert.ok(archive.ok());
  const file = "tool-choice-" + mode + "-conversation.zip";
  await writeFile(out + "/" + file, await archive.body());

  let boundaries;
  if (!real) {
    const direct = await suggest("固定按部门分组汇总已核准的费用金额，交付 Excel。");
    assert.equal(direct.result.status, "ready");
    assert.equal(direct.result.result.toolChoice, undefined);
    assert.deepEqual(direct.result.result.agents, [candidates[1].id]);
    const invalid = await suggest("TOOL_PLAN_UNKNOWN 请选择一种受控汇总口径。");
    assert.equal(invalid.result.status, "invalid");
    const stale = await suggest(goal);
    const current = await request(member, stale.path);
    const suggestion = current.suggestion.result;
    const { step, selector, alternative, revision } = suggestion.toolChoice;
    const pin = { step, selector, alternative, revision };
    const submission = {
      suggestion: current.suggestion.id,
      expected: current.revision,
      brief: suggestion.executionBrief,
      agents: suggestion.agents,
      agentRevisions: suggestion.revisions,
      sources: current.suggestion.inputSources,
      toolChoices: [pin],
    };
    await request(member, stale.path + "/plan", { ...submission, toolChoices: [] }, 409);
    await request(member, stale.path + "/plan", {
      ...submission, toolChoices: [{ ...pin, alternative: candidates[0].id === pin.alternative ? candidates[1].id : candidates[0].id }],
    }, 409);
    await request(member, stale.path + "/plan", {
      ...submission, brief: "汇总另一批数据",
    }, 409);
    const group = suggestion.toolChoice.alternativeScope.group;
    await request(member, "agents/tools", {
      id: alternative, expected: revision, name: "新版替代汇总",
      description: "保留原可核对的口径",
      tool: { operation: "aggregate", config: { group, amount: "金额" } },
    });
    const proof = await request(member, "agents/" + alternative + "/validate", {
      revision: revision + 1, input: group + ",金额\n甲,2.10", expected: "2.10", authorizeOneCall: true,
    }, 200);
    assert.equal(proof.status, "passed");
    await request(admin, "agents/" + alternative + "/publish", { revision: revision + 1 }, 204);
    await request(member, stale.path + "/plan", submission, 409);
    const prevented = await request(member, stale.path);
    assert.equal(prevented.costSummary.calls, 1);
    assert.equal(prevented.plan, null);
    await member.reload();
    await member.getByText("备选汇总已换版或暂不可用，请重新规划。", { exact: true }).waitFor();
    assert.ok(await member.getByRole("button", { name: "采用为待确认方案" }).isDisabled());
    await request(admin, "agents/" + alternative + "/publish", { revision }, 204);
    const toggle = "admin/catalog/agents/" + alternative;
    const during = await suggest("TOOL_PLAN_HOLD " + goal, async (path) => {
      await waitForState(member, path, (value) => value.suggestion?.status === "submitted");
      await request(admin, toggle, {action:"disable",reason:"验证生成建议期间撤掉备选汇总"},204);
    });
    assert.equal(during.result.status, "stale");
    const withheld = await request(member, during.path);
    assert.equal(withheld.suggestion.result, null);
    assert.equal(withheld.costSummary.calls, 1);
    await request(admin, toggle, {action:"enable",reason:"恢复测试候选"},204);
    boundaries = {
      directUsesOneTool: true, unknownActionRejected: true, scopeChangeRejected: true,
      newerCandidateRequiresReplan: true, unavailableBeforeProposalReturned: true,
    };
  }
  return {
    project: main.project, run: ready.run.id, file, choice: 2, fromConversation: true,
    runtimeChoice: ready.plan.nodes[1].toolChoice.selected, pinnedAlternative: route.alternative,
    calls: ready.costSummary.calls, boundaries,
  };
}

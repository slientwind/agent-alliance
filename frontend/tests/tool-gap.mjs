import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

// Accept a planning suggestion with a missing capability, then fill the gap in
// the same project using a versioned native document tool.
export async function checkToolGap(member, admin, endpoint, out, realTeam) {
  const stem = realTeam ? "quick-gap-local" : "quick-gap";
  const model = await admin.request.post("/api/resources", {
    data: {
      name: "试点规划",
      endpoint,
      model: realTeam ? "local-course-qwen" : "fixture",
      team: realTeam,
      key: "synthetic",
    },
  });
  assert.equal(model.status(), 201);
  const resource = (await model.json()).id;
  await member.reload();
  if (!realTeam)
    await member.getByRole("button", { name: "新对话", exact: true }).click();
  const goal =
    (realTeam ? "" : "DRAFT_GAP_PROBE ") +
    "请把已确认的会议议程排版成可编辑的 Word 文件，原文和顺序保持不变。以后还要用这个助手整理其他议程。";
  await member.getByRole("textbox", { name: "消息", exact: true }).fill(goal);
  const body = "09:00 复核文案\n10:00 核对物料清单";
  await member
    .locator("input[type=file]")
    .last()
    .setInputFiles({
      name: "已确认的议程.txt",
      mimeType: "text/plain",
      buffer: Buffer.from(body),
    });
  await member.getByRole("button", { name: "发送消息" }).click();
  const planModel = member.getByLabel("规划助手模型");
  await planModel.waitFor();
  await planModel.selectOption(resource);
  await member.getByLabel(/授权一次规划调用/).check();
  await member.getByRole("button", { name: "请助手规划" }).click();
  const gap = member
    .getByRole("heading", {
      name: realTeam ? /待补能力/ : "待补能力 · 会议时程",
    })
    .locator("..");
  const suggestedGap = await gap.getByRole("heading").first().innerText();
  if (realTeam) assert.match(suggestedGap, /word|文档|排版|议程/i);
  await gap.getByLabel("补齐方式").selectOption("tool");
  await gap.getByLabel("工具操作").selectOption("docx");
  assert.equal(await gap.getByLabel("使用模型").count(), 0);
  await gap.getByLabel("草稿名称").fill("会议议程成稿助手");
  await gap
    .getByLabel("输入、处理规则与交付要求")
    .fill("把已确认的议程排版成 Word 文件，保留事项顺序。");
  await gap.getByLabel("合成试运行样例").fill("09:00 试点组签到");
  await gap.getByLabel("预期产出应包含").fill("试点组签到");
  const bind = member.waitForResponse(
    (r) => r.url().includes("/gaps/0") && r.request().method() === "PUT",
  );
  await gap.getByRole("button", { name: "保存为待验证草稿" }).click();
  const reply = await bind;
  assert.equal(reply.status(), 200, await reply.text());
  const { agent } = await reply.json();
  const path = "/api/projects/" + new URL(member.url()).hash.slice(1);
  const read = async () => (await member.request.get(path)).json();
  const before = await read();
  assert.equal(before.run, null);
  if (realTeam) {
    assert.equal(before.suggestion.result.text, goal);
    assert.equal(before.suggestion.result.executionBrief, goal);
    assert.deepEqual(before.suggestion.result.agents || [], []);
  }
  assert.equal(before.suggestion.bindings[0].agent, agent);
  assert.equal(before.suggestion.bindings[0].ready, false);
  const ownAgents = async () =>
    (await member.request.get("/api/agents")).json();
  const boundAgent = (await ownAgents()).find((a) => a.id === agent);
  assert.equal(boundAgent.team, before.team);
  const count = (await ownAgents()).length;
  const replay = await member.request.put(reply.url(), {
    data: reply.request().postDataJSON(),
  });
  assert.equal(replay.status(), 409);
  assert.equal((await ownAgents()).length, count);
  await gap.getByText("验证并发布此草稿").click();
  await gap.getByLabel(/授权一次试运行/).check();
  await gap.getByRole("button", { name: "执行缺口试运行" }).click();
  await gap.getByText(/已验证，待授权发布/).waitFor();
  assert.ok(
    await member.getByRole("button", { name: "采用为待确认方案" }).isDisabled(),
  );
  await gap.getByText("试运行记录与回执", { exact: true }).click();
  const trialFile = gap.getByRole("link", { name: "下载试运行原件" });
  await trialFile.waitFor();
  const checked = await member.request.get(
    await trialFile.getAttribute("href"),
  );
  assert.ok(checked.ok());
  await writeFile(out + "/" + stem + "-trial.docx", await checked.body());
  const release = await admin.request.post(
    "/api/agents/" + agent + "/publish",
    { data: { revision: 1 } },
  );
  assert.equal(release.status(), 204);
  await member.reload();
  await member.getByRole("button", { name: "采用为待确认方案" }).click();
  await member.getByRole("heading", { name: "协作方案 v1" }).waitFor();
  const chosen = await read();
  assert.equal(chosen.plan.nodes[0].agent, agent);
  assert.equal(chosen.plan.nodes[0].operation, "docx");
  assert.equal(chosen.plan.sources.length, 1);
  assert.equal(chosen.run, null);
  await member.getByLabel(/授权本次 1/).check();
  await member.getByRole("button", { name: "确认并执行" }).click();
  await member.getByRole("button", { name: "审核此版本" }).click();
  await member.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = member.getByRole("link", { name: "下载交付包（ZIP）" });
  await download.waitFor();
  const project = await read();
  assert.equal(project.stage, "delivered");
  assert.equal(project.costSummary.calls, 3);
  assert.equal(project.artifacts[0].body, body);
  const zip = await member.request.get(await download.getAttribute("href"));
  assert.ok(zip.ok());
  await writeFile(out + "/" + stem + ".zip", await zip.body());
  await writeFile(
    out + "/" + stem + ".json",
    JSON.stringify(
      {
        resource,
        suggestedGap,
        planner: realTeam ? "local-course-qwen" : "fixture",
        team: project.team,
        agent,
        run: project.run.id,
        operation: "docx",
        calls: project.costSummary.calls,
        answer: body,
        replacedBindingStatus: replay.status(),
      },
      null,
      2,
    ),
  );
  return { agent, resource };
}

import { releaseInBrowser } from "./agent-release.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkProjectDraftRevision({
  admin,
  user,
  model,
  post,
  get,
  out,
  mode,
}) {
  await user.getByRole("button", { name: "新对话", exact: true }).click();
  await user
    .getByRole("textbox", { name: "消息", exact: true })
    .fill("DRAFT_REVISE_PROJECT 从维修记录里提取工单编号。今日报修 SRV-951。");
  await user.getByRole("button", { name: "发送消息" }).click();
  await user.getByText("根据对话推荐方案或直接回答").click();
  await user.getByLabel("规划助手模型").selectOption({ label: "Stub TLS v1" });
  await user.getByLabel(/授权一次规划调用/).check();
  await user.getByRole("button", { name: "请助手规划" }).click();
  const gap = user.getByRole("region", { name: "能力补齐 · 工单提取" });
  await gap.getByLabel("补齐方式").selectOption("draft");
  await gap.getByLabel("使用模型").selectOption(model.id);
  await gap.getByLabel(/整段输出逐字一致/).check();
  await gap.getByRole("button", { name: "保存为待验证草稿" }).click();
  await gap.getByText("验证并发布此草稿", { exact: true }).click();
  const project = "projects/" + new URL(user.url()).hash.slice(1);
  const before = await get(user, project);
  const first = before.suggestion.bindings[0];
  assert.ok(before.suggestion.result.executionBrief.includes("SRV-951"));
  assert.ok(!before.suggestion.result.text.includes("SRV-951"));
  const url = project + "/suggestions/" + before.suggestion.id + "/gaps/0";
  const trial = async () => {
    await gap.getByLabel(/授权一次试运行/).check();
    const completed = user.waitForResponse((r) =>
      r.url().endsWith("/validate"),
    );
    await gap.getByRole("button", { name: "执行缺口试运行" }).click();
    const response = await completed;
    assert.equal(response.status(), 200, await response.text());
    return response.json();
  };
  const initial = await trial();
  assert.ok(first.exactCheck);
  assert.equal(initial.status, "failed", initial.output);
  await gap.getByText("调整本步 Agent 职责", { exact: true }).click();
  const instructions = "只输出工单编号，保留前导字母。";
  await gap.getByLabel("调整后的职责").fill(instructions);
  const data = {
    mode: "revise",
    expected: before.revision,
    bindingRevision: first.revision,
    revision: first.version,
    instructions,
  };
  const mutated = await user.request.put("/api/" + url, {
    data: { ...data, example: "换一个简单样例" },
  });
  assert.equal(mutated.status(), 422, await mutated.text());
  const unauthorized = await admin.request.put("/api/" + url, { data });
  assert.equal(unauthorized.status(), 404, await unauthorized.text());
  await gap.getByRole("button", { name: "保存新修订到此缺口" }).click();
  await gap.getByText(/项目工单助手 v2/).waitFor();
  const stale = await user.request.put("/api/" + url, { data });
  assert.equal(stale.status(), 409, await stale.text());
  const after = await get(user, project);
  const next = after.suggestion.bindings[0];
  assert.equal(next.agent, first.agent);
  assert.equal(next.revision, first.revision + 1);
  assert.equal(next.version, 2);
  assert.equal(next.example, first.example);
  assert.equal(next.expected, first.expected);
  assert.equal(next.validated, false);
  assert.equal(after.costSummary.calls, 2);
  assert.equal(await gap.getByLabel(/授权一次试运行/).isChecked(), false);
  assert.equal(
    await gap.getByText("调整本步 Agent 职责", { exact: true }).count(),
    1,
  );
  const exampleForm = gap
    .locator("form")
    .filter({ has: user.getByRole("button", { name: "执行缺口试运行" }) });
  await exampleForm.screenshot({
    path: out + "/draft-project-gap-" + mode + ".png",
  });
  const revised = await trial();
  const trials = [initial, revised];
  let version = 2;
  const recordTrials = () =>
    writeFile(
      out + "/draft-project-trials-" + mode + ".json",
      JSON.stringify(
        {
          sample: first.example,
          expected: first.expected,
          trials,
        },
        null,
        2,
      ),
    );
  await recordTrials();
  assert.ok(next.exactCheck && next.exactCheck !== first.exactCheck);
  if (revised.status === "failed") {
    assert.equal(
      (
        await admin.request.post("/api/agents/" + first.agent + "/publish", {
          data: { revision: 2 },
        })
      ).status(),
      409,
    );
    await gap.getByText("调整本步 Agent 职责", { exact: true }).click();
    await gap
      .getByLabel("调整后的职责")
      .fill(
        "提取工单编号。识别 SRV- 后接数字的完整串，保留连字符和全部数字，只输出原串。例如记录包含 SRV-095 则只输出 SRV-095。",
      );
    await gap.getByRole("button", { name: "保存新修订到此缺口" }).click();
    await gap.getByText(/项目工单助手 v3/).waitFor();
    assert.equal(await gap.getByLabel("验证输入").inputValue(), first.example);
    trials.push(await trial());
    await recordTrials();
    version = 3;
  }
  assert.equal(trials.at(-1).status, "passed", JSON.stringify(trials));
  assert.equal(trials.at(-1).output, first.expected);
  const history = gap.locator("details").filter({
    has: user.locator(":scope > summary", { hasText: "试运行记录与回执" }),
  });
  await gap.getByText("试运行记录与回执", { exact: true }).click();
  for (const [index, run] of trials.entries()) {
    const row = history.locator("section").filter({
      has: user.locator("strong", {
        hasText: "v" + (index + 1) + " · " + (run.status === "passed" ? "验证通过" : "验证未通过"),
      }),
    });
    await row.getByText(run.output, { exact: true }).waitFor();
  }
  const publication = await releaseInBrowser({
    admin, user, view: gap, id: first.agent, name: first.name, revision: version, get,
  });
  await user.reload();
  await user.getByRole("button", { name: "采用为待确认方案" }).click();
  await user.getByLabel(/授权本次 1/).check();
  await user.getByRole("button", { name: "确认并执行" }).click();
  const review = user.getByRole("button", { name: "审核此版本" });
  await review.waitFor();
  const pending = await get(
    user,
    "projects/" + new URL(user.url()).hash.slice(1),
  );
  assert.equal(pending.artifacts[0].body.trim(), "SRV-951");
  await review.click();
  await user.getByRole("button", { name: "确认并固定以上版本" }).click();
  const download = user.getByRole("link", { name: "下载交付包（ZIP）" });
  await download.waitFor();
  const archive = await user.request.get(await download.getAttribute("href"));
  assert.ok(archive.ok(), await archive.statusText());
  await writeFile(
    out + "/draft-project-gap-" + mode + ".zip",
    await archive.body(),
  );
  const result = await get(user, project);
  assert.equal(result.plan.nodes[0].revision, version);
  assert.equal(result.artifacts[0].body, "SRV-951");
  assert.equal(result.costSummary.calls, trials.length + 2);
  assert.equal(result.stage, "delivered");
  return {
    planner: "HTTPS fixture",
    trialResults: trials.map(({ status, output }) => ({
      status,
      output,
    })),
    originalVersion: first.version,
    adoptedVersion: version,
    publication,
    stableAgent: next.agent === first.agent,
    sample: next.example,
    expected: next.expected,
    calls: result.costSummary.calls,
    answer: result.artifacts[0].body,
  };
}

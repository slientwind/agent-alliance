import { checkPlanningOutput } from "./planning-output.mjs";
import { checkToolGap } from "./tool-gap.mjs";
import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: out,
} = process.env;
const browser = await chromium.launch();
const admin = await browser.newPage({ baseURL });
const member = await browser.newPage({
  baseURL,
  viewport: { width: 390, height: 844 },
});
const panel = member.getByRole("dialog", { name: "Agent", exact: true });
const launch = member.getByRole("button", { name: "选择或创建 Agent" });
const examples = [
  {
    op: "aggregate",
    goal: "按中心汇总已收金额并导出 Excel",
    file: "账目.csv",
    body: '\ufeff"中心,区域",实收,状态,币种\n华东,12.35,通过,CNY\n华东,7.30,通过,CNY\n华北,100.00,待审,CNY',
    sample:
      '"中心,区域",实收,状态,币种\n样例,12.00,通过,CNY\n样例,8.00,通过,CNY',
    proof: "合计 · 20.00",
    final: "合计 · 19.65",
  },
  {
    op: "docx",
    goal: "将设备交接正文整理成 Word",
    file: "设备交接.txt",
    body: "经办人核对设备序列号。\n请接收人签收。",
    sample: "测试用的交接说明",
    proof: "交接说明",
    final: "请接收人签收。",
  },
  {
    op: "xlsx",
    goal: "制作 Excel 逐行清单",
    file: "盘点清单.txt",
    body: "00017\n=待核对\n投影仪",
    sample: "待确认\n已验收",
    proof: "已验收",
    final: "00017",
  },
  {
    op: "extract",
    goal: "从档案提取原文",
    file: "归档说明.txt",
    body: "由采购专员核对交付编号。",
    sample: "资料摘取样例",
    proof: "摘取样例",
    final: "归档说明.txt v1",
  },
];
async function api(page, path, method = "GET", data, status = 200) {
  const res = await page.request.fetch("/api/" + path, { method, data });
  assert.equal(
    res.status(),
    status,
    path + " " + (res.ok() ? "" : await res.text()),
  );
  if (res.headers()["content-type"]?.includes("json")) return res.json();
}
const results = [];
try {
  await api(admin, "login", "POST", {
    email: "review@sample.invalid",
    password,
  });
  const invite = await api(admin, "admin/invitations", "POST", {}, 201);
  await api(
    member,
    "register",
    "POST",
    {
      token: invite.token,
      email: "tools-author@sample.invalid",
      name: "业务制作人",
      password,
    },
    201,
  );
  await member.goto("/");
  for (const ex of examples) {
    if (results.length)
      await member.getByRole("button", { name: "新对话", exact: true }).click();
    await member
      .getByRole("textbox", { name: "消息", exact: true })
      .fill(ex.goal);
    await member
      .locator("input[type=file]")
      .last()
      .setInputFiles({
        name: ex.file,
        mimeType: "text/plain",
        buffer: Buffer.from(ex.body),
      });
    await launch.click();
    await panel.getByRole("button", { name: "创建 Agent" }).click();
    if (ex.op === "extract")
      await panel.getByLabel("成果类型").selectOption(ex.op);
    assert.equal(await panel.getByLabel("成果类型").inputValue(), ex.op);
    assert.equal(await panel.getByLabel("文本模型").count(), 0);
    await panel.getByText(/承担工具用量，草稿对该团队可见/).waitFor();
    assert.equal(
      await panel.getByLabel("工具归属团队", { exact: true }).count(),
      0,
    );
    if (ex.op === "aggregate") {
      await panel.getByText("从附件读取列名", { exact: true }).click();
      await panel.getByRole("button", { name: ex.file, exact: true }).click();
      await panel.getByText(/已读取 账目.csv/).waitFor();
      assert.deepEqual(
        await panel
          .locator("datalist option")
          .evaluateAll((opts) => opts.map((o) => o.value)),
        ["中心,区域", "实收", "状态", "币种"],
      );
      await panel.getByLabel("分组列", { exact: true }).fill("中心,区域");
      await panel.getByLabel("金额列", { exact: true }).fill("实收");
      await panel.getByText("单位与筛选条件", { exact: true }).click();
      await panel.getByLabel("金额单位列").fill("币种");
      await panel.getByLabel("筛选列").fill("状态");
      await panel.getByLabel("保留值").fill("通过");
      await panel.screenshot({ path: out + "/quick-tools-columns.png" });
    }
    const request = member.waitForResponse(
      (r) =>
        r.url().endsWith("/api/agents/tools") &&
        r.request().method() === "POST",
    );
    await panel.getByRole("button", { name: "创建并准备试运行" }).click();
    const created = await request;
    assert.equal(created.status(), 201, await created.text());
    const { id } = await created.json(),
      path = "agents/" + id;
    assert.ok(
      !(await api(admin, "agents")).find((a) => a.id === id)?.quickDraft,
    );
    await panel.getByRole("button", { name: "关闭", exact: true }).click();
    await launch.click();
    await panel.getByText(/我的制作草稿与验收/).click();
    await panel.getByRole("button", { name: new RegExp(ex.goal) }).click();
    const validate = async (input, expected) => {
      await panel.getByLabel("试运行输入").fill(input);
      await panel.getByLabel("结果应包含").fill(expected);
      await panel.getByRole("checkbox").check();
      const completion = member.waitForResponse(
        (r) =>
          r.url().endsWith(path + "/validate") &&
          r.request().method() === "POST",
      );
      await panel.getByRole("button", { name: "运行示例" }).click();
      return (await completion).json();
    };
    if (ex.op === "aggregate") {
      const failed = await validate("区域,总额\n北区,1.00", ex.proof);
      assert.equal(failed.status, "failed");
      await api(admin, path + "/publish", "POST", { revision: 1 }, 409);
    }
    assert.equal((await validate(ex.sample, ex.proof)).status, "passed");
    await panel.getByRole("button", { name: "申请发布此版本", exact: true }).waitFor();
    await panel.getByText("试运行记录与回执", { exact: true }).click();
    if (ex.op !== "extract") {
      const link = panel.getByRole("link", { name: "下载试运行原件" });
      await link.waitFor();
      const file = await member.request.get(await link.getAttribute("href"));
      assert.ok(file.ok());
      await writeFile(out + "/quick-trial-" + ex.op, await file.body());
    }
    await api(member, path + "/publish", "POST", { revision: 1 }, 403);
    await api(admin, path + "/publish", "POST", { revision: 1 }, 204);
    await panel.getByRole("button", { name: "查看发布状态" }).click();
    await panel.getByRole("button", { name: "在此对话使用已发布版本" }).click();
    await member.getByRole("button", { name: "发送消息" }).click();
    await member.getByRole("heading", { name: "协作方案 v1" }).waitFor();
    const proj = "projects/" + new URL(member.url()).hash.slice(1);
    const plan = await api(member, proj);
    assert.equal(plan.plan.nodes[0].operation, ex.op);
    assert.equal(plan.plan.sources.length, 1);
    await member.getByLabel(/授权本次 1/).check();
    await member.getByRole("button", { name: "确认并执行" }).click();
    await member.getByRole("button", { name: "审核此版本" }).click();
    await member.getByRole("button", { name: "确认并固定以上版本" }).click();
    const link = member.getByRole("link", { name: "下载交付包（ZIP）" });
    await link.waitFor();
    const project = await api(member, proj);
    assert.equal(project.stage, "delivered");
    assert.ok(
      project.artifacts[0].body.includes(ex.final),
      project.artifacts[0].body,
    );
    const zip = await member.request.get(await link.getAttribute("href"));
    assert.ok(zip.ok());
    await writeFile(
      out + "/quick-delivery-" + ex.op + ".zip",
      await zip.body(),
    );
    results.push({
      operation: ex.op,
      agent: id,
      plan: proj,
      charges: project.costSummary.calls,
      answer: project.artifacts[0].body,
    });
    await writeFile(
      out + "/quick-tools.json",
      JSON.stringify(results, null, 2),
    );
  }
  const context = await checkToolGap(member, admin, process.env.QUICK_ENDPOINT, out);
  await checkPlanningOutput(member, admin, out, context, false);
} catch (error) {
  await member.screenshot({ path: out + "/error-tools.png" });
  console.error((await member.locator("body").innerText()).slice(-3000));
  throw error;
} finally {
  await browser.close();
}

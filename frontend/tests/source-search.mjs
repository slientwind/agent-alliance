import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  RETRIEVAL_FIXTURES: docs,
  VISUAL_EVIDENCE: out,
  QUICK_ENDPOINT: endpoint,
} = process.env;
const browser = await chromium.launch();
try {
  const p = await browser.newPage({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  const req = p.request;
  assert.ok(
    (
      await req.post("/api/login", {
        data: { email: "review@sample.invalid", password },
      })
    ).ok(),
  );
  async function post(url, data) {
    const r = await req.post("/api/" + url, { data });
    assert.ok(r.ok(), await r.text());
    return r.status() === 204 ? null : await r.json();
  }
  const model = await post("resources", {
    name: "资料依据验证",
    model: "fixture",
    endpoint,
    key: "synthetic",
  });
  const agent = await post("agents", {
    name: "依据核对",
    instructions: "核对选定版本的依据",
    resource: model.id,
    resourceRevision: 1,
  });
  assert.equal(
    (
      await post("agents/" + agent.id + "/validate", {
        revision: 1,
        input: "verify",
        expected: "RECEIPT",
        authorizeOneCall: true,
      })
    ).status,
    "passed",
  );
  await post("agents/" + agent.id + "/publish", { revision: 1 });
  const project = await post("projects", {
    goal: "整理可追溯的培训依据",
    agent: agent.id,
    agentRevision: 1,
  });
  const base = "/api/projects/" + project.id;
  const search = async (q) => {
    const r = await req.get(base + "/retrieval", { params: { q } });
    assert.ok(r.ok(), await r.text());
    return r.json();
  };
  async function save(name, body, source, version) {
    const url = base + "/sources" + (source ? "/" + source : "");
    const r = await req.fetch(url, {
      method: source ? "PUT" : "POST",
      params: { name, ...(source ? { version: String(version) } : {}) },
      data: Buffer.from(body),
    });
    assert.equal(r.status(), 201, await r.text());
    return r.json();
  }
  const secretProject = await post("projects", { goal: "干扰资料" });
  await post(
    "projects/" + secretProject.id + "/sources?name=other.txt",
    Buffer.from("跨部门借用还需要谁确认？其他团队主管"),
  );
  const bodies = {},
    ids = {},
    sentences = {};
  for (const [key, filename] of [
    ["training", "T3-v2.md"],
    ["product", "P3-v2.md"],
  ]) {
    const body = await readFile(
      path.join(docs, "fixtures/phase1", filename),
      "utf8",
    );
    sentences[key] = body.split("\n\n")[1].trim().split("。").filter(Boolean);
    const s = await save(filename, "已作废的专属编号 OLD7823");
    ids[key] = s.id;
    bodies[s.id] = body + (key === "training" ? "\nsource72\n" : "");
    await save(filename, bodies[s.id], s.id, 1);
  }
  const expectSemantic = !!process.env.RETRIEVAL_URL;
  let snapshot;
  for (let i = 0; i < 80; i++) {
    snapshot = await search("设备归还");
    if (
      !expectSemantic ||
      (snapshot.mode === "hybrid" &&
        snapshot.result.indexed === snapshot.result.passages &&
        snapshot.result.passages > 0)
    )
      break;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  assert.equal(snapshot.mode, expectSemantic ? "hybrid" : "lexical");
  if (expectSemantic)
    assert.equal(snapshot.result.indexed, snapshot.result.passages);
  const cases = [];
  for (const file of [
    "cases.tsv",
    "additional-cases.tsv",
    "hybrid-new-cases.tsv",
  ]) {
    const data = await readFile(
      path.join(docs, "evidence/phase1-retrieval", file),
      "utf8",
    );
    for (const line of data.trim().split("\n").slice(1)) {
      const [id, query, expected] = line.split("\t");
      const { result } = await search(query);
      for (const h of result.hits) {
        assert.ok(h.id in bodies);
        assert.equal(h.version, 2);
        assert.equal(
          Array.from(bodies[h.id])
            .slice(h.start - 1, h.end)
            .join(""),
          h.excerpt,
        );
      }
      const h = result.hits[0],
        key = h?.id === ids.training ? "training" : "product";
      const actual = h
        ? key + "-v2." + (sentences[key].indexOf(h.excerpt) + 1)
        : "EMPTY";
      cases.push({
        id,
        query,
        expected,
        actual,
        correct: actual === expected,
        candidates: result.hits,
      });
    }
  }
  const report = {
    mode: snapshot.mode,
    indexed: snapshot.result.indexed,
    passages: snapshot.result.passages,
    firstOrEmptyCorrect: cases.filter((c) => c.correct).length,
    total: cases.length,
    cases,
  };
  await writeFile(
    out + "/retrieval-quality.json",
    JSON.stringify(report, null, 2),
  );
  assert.ok(cases.find((c) => c.id === "K02").correct);
  const guest = await browser.newPage({ baseURL });
  const invite = await post("admin/invitations", {});
  assert.equal(
    (
      await guest.request.post("/api/register", {
        data: {
          token: invite.token,
          name: "其他项目成员",
          email: "search-guest@sample.invalid",
          password,
        },
      })
    ).status(),
    201,
  );
  assert.equal(
    (await guest.request.get(base + "/retrieval?q=x")).status(),
    404,
  );
  const guestId = (await (await guest.request.get("/api/me")).json()).id;
  assert.ok(
    (
      await req.post(base + "/participants", {
        data: {
          member: guestId,
          review: false,
          expected: (await (await req.get(base)).json()).revision,
        },
      })
    ).ok(),
  );
  await guest.goto("/#" + project.id);
  await guest.getByText(/项目资料 · 2/).click();
  await guest.getByText("在资料中查找依据").click();
  await guest
    .getByLabel("问题或关键词")
    .fill("跨部门借用还需接收部门负责人确认");
  await guest.getByRole("button", { name: "查找原文" }).click();
  await guest.locator(".source-search article").first().waitFor();
  assert.equal(
    await guest.getByRole("button", { name: "在方案中引用整份资料" }).count(),
    0,
  );
  await guest.getByLabel("问题或关键词").fill("谁负责报销差旅费？");
  await guest.getByRole("button", { name: "查找原文" }).click();
  await guest.getByText("当前可读资料中未找到依据。").waitFor();
  await p.goto("/#" + project.id);
  await p.getByText(/项目资料 · 2/).click();
  await p.getByText("在资料中查找依据").click();
  await p.getByLabel("问题或关键词").fill("遗失时立即报告保管员");
  await p.getByRole("button", { name: "查找原文" }).click();
  const card = p
    .locator(".source-search article")
    .filter({ hasText: "遗失时立即报告保管员" })
    .first();
  await card.waitFor();
  const link = await card.getByRole("link").getAttribute("href");
  assert.ok(link.endsWith(ids.training + "/content?version=2"));
  await card.getByRole("button", { name: "在方案中引用整份资料" }).click();
  await card.getByRole("button", { name: "已在方案中引用" }).waitFor();
  await card.screenshot({ path: out + "/retrieval-citation.png" });
  await p.getByRole("button", { name: "保存待确认方案" }).click();
  await p.getByRole("heading", { name: "协作方案 v2" }).waitFor();
  const read = async () => await (await req.get(base)).json();
  const pinned = await read();
  assert.equal(pinned.run, null);
  assert.equal(pinned.plan.sources[0].version, 2);
  await save("T3-v3.md", "替代资料，当前版本已更新。", ids.training, 2);
  assert.ok(
    (await search("遗失时立即报告保管员")).result.hits.every(
      (h) => h.id !== ids.training || h.version === 3,
    ),
  );
  await p.reload();
  await p.getByLabel(/授权本次 1/).check();
  await p.getByRole("button", { name: "确认并执行" }).click();
  await p.getByText(/资料依据核对 · 1/).click();
  const assessment = p
    .locator("form")
    .filter({ has: p.getByLabel("核对依据或所需修改") });
  await assessment.getByLabel("处理方式").selectOption("retain");
  await assessment
    .getByLabel("核对依据或所需修改")
    .fill("此任务要求归档 v2 培训依据，已复核所选版本。");
  await assessment.getByRole("button", { name: "记录资料影响" }).click();
  await p.getByRole("button", { name: "审核此版本" }).click();
  await p.getByRole("button", { name: "确认并固定以上版本" }).click();
  await p.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
  assert.equal((await read()).artifacts[0].body, "RECEIPT source72");
  assert.equal(
    (await req.delete(base + "/sources/" + ids.training)).status(),
    204,
  );
  assert.ok(
    (await search("替代资料")).result.hits.every((h) => h.id !== ids.training),
  );
  assert.equal((await req.get(link)).status(), 404);
  await writeFile(
    out + "/retrieval-lifecycle.json",
    JSON.stringify(
      {
        mode: snapshot.mode,
        source: ids.training,
        pinned: 2,
        currentBeforeRevoke: 3,
        otherProjectStatus: 404,
        participantReadOnly: true,
        noEvidenceVisible: true,
        withdrawnOriginalStatus: 404,
        executedPinnedVersion: true,
        delivered: true,
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

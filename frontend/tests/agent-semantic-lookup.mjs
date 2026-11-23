import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const { SMOKE_ORIGIN: baseURL, BROWSER_PASSWORD: password, QUICK_ENDPOINT: endpoint, VISUAL_EVIDENCE: out } = process.env;
const online = process.argv[2] === "online";
const local = process.argv[2] === "local";
const prepareFresh = process.argv[2] === "prepare-fresh";
const fresh = process.argv[2] === "fresh";
const resumeBacklog = process.argv[2] === "resume-backlog";
const reportPath = out + (local ? "/agent-semantic-local.json" : "/agent-semantic-lookup.json");
const question = "活动现场没法用机器签到，要怎么登记？";
const rule = "签到设备发生故障时，现场负责人启用离线纸质名册登记";
const alternativeQuestion = "货还没发，换个收件地点该怎么办？";
const alternativeRule = "客户变更收货地址需在发货前提交工单，客服在工单中记录新地址";
const unrelated = "地铁公交费报销需要哪种票据？";
const browser = await chromium.launch();

try {
  const admin = await browser.newPage({ baseURL });
  const member = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  async function api(page, path, { method = "GET", data, headers, status = 200 } = {}) {
    const response = await page.request.fetch("/api/" + path, { method, data, headers });
    const body = await response.text();
    assert.equal(response.status(), status, body);
    return body ? JSON.parse(body) : null;
  }
  const post = (page, path, data, status = 201) => api(page, path, { method: "POST", data, status });
  const detail = id => api(member, "projects/" + id);
  async function waitFor(get, done) {
    const deadline = Date.now() + 18000;
    let value;
    do {
      value = await get();
      if (done(value)) return value;
      await new Promise(resolve => setTimeout(resolve, 200));
    } while (Date.now() < deadline);
    assert.fail(JSON.stringify(value));
  }
  if (!online && !local) await post(member, "login", { email: "semantic-member@sample.invalid", password }, 200);
  const results = local ? { model: "local CPU BGE and local Qwen" } : online ?
    { model: "local CPU BGE with HTTPS search and answer fixture" } : JSON.parse(await readFile(reportPath, "utf8"));
  let agent = results.agent;
  async function source(project, name, text, previous) {
    const path = "projects/" + project + "/sources" + (previous ? "/" + previous.id : "");
    const query = new URLSearchParams({ name });
    if (previous) query.set("version", previous.version);
    return api(member, path + "?" + query, {
      method: previous ? "PUT" : "POST", data: Buffer.from(text), headers: { "Content-Type": "application/octet-stream" }, status: 201,
    });
  }
  async function draft(question) {
    return (await post(member, "projects", { goal: question })).id;
  }
  async function plan(project, goal, pin) {
    await post(member, "projects/" + project + "/plan", {
      expected: (await detail(project)).revision, brief: goal, agents: [agent], agentRevisions: [1],
      sources: [{ id: pin.id, version: pin.version }], sourceLookup: true,
    }, 204);
  }
  async function indexed(project) {
    await waitFor(() => api(member, "projects/" + project + "/retrieval?q=检查"),
      value => value.mode === "hybrid" && value.result.passages > 0 && value.result.indexed === value.result.passages);
  }
  async function download(page, filename) {
    const reviews = page.getByRole("button", { name: "审核此版本" });
    await reviews.first().click();
    await reviews.first().click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    const link = page.getByRole("link", { name: "下载交付包（ZIP）" });
    await link.waitFor();
    const pack = await page.request.get(await link.getAttribute("href"));
    assert.equal(pack.status(), 200);
    await writeFile(out + "/" + filename, await pack.body());
    return filename;
  }
  async function run(project, expected, calls, answer, requirement) {
    await member.goto("/#" + project);
    await member.getByLabel("找不到原文时等待我补件").check();
    await member.getByLabel(/授权本次 2/).check();
    await member.getByRole("button", { name: "确认并执行" }).click();
    const done = await waitFor(() => detail(project), p => p.stage === "review" || p.run?.receiptRequest?.kind === "lookup");
    const trace = await api(member, "projects/" + project + "/runs/" + done.run.id + "/trace");
    const codes = trace.trace.events.map(event => event.code);
    assert.ok(codes.includes(expected), JSON.stringify(codes));
    assert.equal(done.costSummary.calls, calls);
    let archive;
    if (answer) {
      assert.ok(done.artifacts[1].body.includes(answer), done.artifacts[1].body);
      assert.ok(done.artifacts[1].body.includes("原文依据"));
      if (requirement) assert.match(done.artifacts[1].body.split("\n")[0], requirement);
      assert.deepEqual(trace.trace.spans.map(span => span.attempts.length), [1, 1]);
      if (expected === "lookup_semantic_searched" || expected === "lookup_semantic_fresh") {
        archive = await download(member, "semantic-answer-" + project + ".zip");
      }
    } else {
      assert.equal(done.artifacts.length, 1);
      assert.equal(trace.trace.spans[1].attempts.length, 0);
      await member.getByRole("heading", { name: "补充引用原件" }).waitFor();
    }
    return { project, run: done.run.id, events: codes, calls, answer, archive, response: answer ? done.artifacts[1].body : undefined };
  }

  if (online || local) {
    await post(admin, "login", { email: "review@sample.invalid", password }, 200);
    const invitation = await post(admin, "admin/invitations", {});
    const email = local ? "semantic-qwen@sample.invalid" : "semantic-member@sample.invalid";
    await post(member, "register", { name: "活动资料经办人", email, token: invitation.token, password });
    const team = (await api(admin, "teams")).find(t => t.default).id;
    const resource = await post(admin, "resources", {
      name: "资料语义问答模型 " + (local ? "本地" : "协议"), endpoint,
      model: local ? "local-course-qwen" : "semantic-lookup-fixture", team, key: "synthetic", estimate: "0.20",
    });
    agent = (await post(member, "agents", {
      name: "资料语义问答 Agent", instructions: "连接验证时回复 READY。执行时准确使用项目原文。",
      resource: resource.id, resourceRevision: 1,
    })).id;
    const checked = await post(member, "agents/" + agent + "/validate", {
      revision: 1, input: "连接验证", expected: "READY", authorizeOneCall: true,
    }, 200);
    assert.equal(checked.status, "passed", checked.output);
    await post(admin, "agents/" + agent + "/publish", { revision: 1 }, 204);
    results.agent = agent;
  }

  if (local) {
    const cases = [
      ["address", "我不小心把快递买错了，想换地方收，该找谁？", alternativeRule, "工单|客服"],
      ["return", "我负责的摊位上的陈列架什么时候送回去？", "活动结束后由场务清点借出的展架，并在次日上午归还", "次日上午"],
    ];
    for (const [name, goal, original, expected] of cases) {
      const project = await draft(goal);
      await plan(project, goal, await source(project, "内部规程.txt", original + "。"));
      await indexed(project);
      results[name] = await run(project, "lookup_semantic_searched", 2, original, new RegExp(expected));
      results[name].position = 1;
      results[name].question = goal;
      await writeFile(reportPath, JSON.stringify(results, null, 2) + "\n");
    }
  } else if (online) {
    const otherProject = await draft("有自己的流程");
    await source(otherProject, "外项目.txt", question + " FOREIGN-CONTENT。");
    await indexed(otherProject);
    const current = await draft(question);
    const selected = await source(current, "现场制度.txt", "📌\n" + rule + "。");
    await source(current, "待核对个人说明.txt", question + " UNPINNED-CONTENT。");
    await plan(current, question, selected);
    await indexed(current);
    results.online = await run(current, "lookup_semantic_searched", 2, rule);
    results.online.position = 3;

    const alternative = await draft(alternativeQuestion);
    await plan(alternative, alternativeQuestion, await source(alternative, "客户支持.txt", alternativeRule + "。"));
    await indexed(alternative);
    results.alternative = await run(alternative, "lookup_semantic_searched", 2, alternativeRule);
    results.alternative.position = 1;

    const noEvidence = await draft(unrelated);
    const irrelevant = await source(noEvidence, "现场制度.txt", rule + "。");
    await source(noEvidence, "未授权报销票据.txt", unrelated + " UNPINNED-CONTENT。");
    await plan(noEvidence, unrelated, irrelevant);
    await indexed(noEvidence);
    results.unrelated = await run(noEvidence, "lookup_empty_search", 1);

    const historical = await draft(question);
    const original = await source(historical, "现场制度.txt", rule + "。");
    await plan(historical, question, original);
    const revision = await source(historical, "现场制度.txt", question + " NEW-REVISION-CONTENT。", original);
    assert.equal(revision.version, 2);
    await indexed(historical);
    results.historical = await run(historical, "lookup_empty_search", 1);

    for (const [key, goal] of [["offline", question], ["lexical", "LEXICAL-ANCHOR " + question]]) {
      const next = await draft(goal);
      const ready = await source(next, "现场制度.txt", rule + "。");
      await plan(next, goal, ready);
      await indexed(next);
      results[key] = { project: next };
    }
  } else if (prepareFresh) {
    await post(admin, "login", { email: "review@sample.invalid", password }, 200);
    const project = await draft(question);
    await plan(project, question, await source(project, "新上传制度.txt", "📌\n" + rule + "。"));
    await source(project, "不选的干扰项.txt", question + " UNPINNED-CONTENT。");
    const status = await api(member, "projects/" + project + "/retrieval?q=核对");
    assert.equal(status.mode, "unavailable");
    assert.equal(status.result.indexed, 0);
    results.freshProject = { project, indexedBeforeDispatch: status.result.indexed };

    const backlogProject = await draft(question);
    const backlog = Array.from({ length: 20 }, (_, index) => "茶水间洗杯槽标签编号 " + (index + 1) + "。").join("\n");
    await plan(backlogProject, question, await source(backlogProject, "较长新资料.txt", backlog + "\n" + rule + "。"));
    results.backlog = { project: backlogProject, position: Array.from(backlog).length + 2 };

    const seed = (await post(admin, "projects", { goal: question })).id;
    const seedPin = await api(admin, "projects/" + seed + "/sources?name=practice.txt", {
      method: "POST", data: Buffer.from("纸质名册在值班台。"), status: 201,
    });
    await post(admin, "projects/" + seed + "/plan", {
      expected: (await api(admin, "projects/" + seed)).revision, brief: question,
      agents: [agent], agentRevisions: [1], sources: [{ id: seedPin.id, version: 1 }], sourceLookup: true,
    }, 204);
    await api(admin, "projects/" + seed + "/runs", {
      method: "POST", data: { expected: (await api(admin, "projects/" + seed)).revision, authorizeCalls: 2 },
      headers: { "Idempotency-Key": "fresh-service-seed" }, status: 202,
    });
    const service = await post(admin, "services", {
      project: seed, name: "现场登记咨询", instructions: "根据本次附上的原文说明处理方式。",
    });
    results.service = { id: service.id };
  } else if (fresh) {
    await post(admin, "login", { email: "review@sample.invalid", password }, 200);
    const application = await post(admin, "services/" + results.service.id + "/applications", { name: "制度查询应用", days: 1 });
    const [freshProject, published, backlog, app] = await Promise.all([
      run(results.freshProject.project, "lookup_semantic_fresh", 2, rule),
      (async () => {
        await admin.goto("/?service=" + results.service.id);
        await admin.getByLabel("向服务提出任务").fill(question);
        await admin.getByLabel("附资料").setInputFiles({
          name: "本次上传制度.txt", mimeType: "text/plain", buffer: Buffer.from(rule + "。"),
        });
        await admin.getByLabel("找不到原文时在原任务等待我补件").check();
        await admin.getByLabel(/授权本次最多 2 次调用/).check();
        await admin.getByRole("button", { name: "确认并启动对话" }).click();
        await admin.waitForURL("**/#*");
        const project = new URL(admin.url()).hash.slice(1);
        const path = "projects/" + project;
        const done = await waitFor(() => api(admin, path),
          value => value.stage === "review" || value.run?.receiptRequest?.kind === "lookup");
        const trace = await api(admin, path + "/runs/" + done.run.id + "/trace");
        const events = trace.trace.events.map(event => event.code);
        assert.ok(events.includes("lookup_semantic_fresh"), JSON.stringify(events));
        assert.equal(done.costSummary.calls, 2);
        assert.ok(done.artifacts[1].body.includes(rule));
        const archive = await download(admin, "semantic-fresh-service.zip");
        return { project, run: done.run.id, events, calls: 2, archive, answer: rule, position: 1 };
      })(),
      (async () => {
        const project = results.backlog.project;
        const path = "projects/" + project;
        await api(member, path + "/runs", {
          method: "POST", data: { expected: (await detail(project)).revision, authorizeCalls: 2, awaitLookupReceipt: true },
          headers: { "Idempotency-Key": "fresh-backlog" }, status: 202,
        });
        const held = await waitFor(() => detail(project), value => value.stage === "review" || value.run?.receiptRequest?.kind === "lookup");
        const trace = await api(member, path + "/runs/" + held.run.id + "/trace");
        const events = trace.trace.events.map(event => event.code);
        assert.ok(events.includes("lookup_semantic_index_pending"), JSON.stringify(events));
        assert.match(held.run.receiptRequest.reason, /资料仍在建立语义索引/);
        assert.equal(held.run.indexWait.step, 2);
        assert.equal(held.run.indexWait.automatic, true);
        assert.equal(held.costSummary.calls, 1);
        assert.equal(trace.trace.spans[1].attempts.length, 0);
        assert.equal(held.artifacts.length, 1);
        return {
          project, run: held.run.id, events, calls: 1, reason: held.run.receiptRequest.reason,
          position: results.backlog.position,
          initialSearch: { id: held.artifacts[0].id, body: held.artifacts[0].body },
        };
      })(),
      (async () => {
        const auth = { Authorization: "Bearer " + application.token };
        const launch = await api(admin, "v1/services/" + results.service.id + "/runs", {
          method: "POST", headers: { ...auth, "Idempotency-Key": "fresh-signed-call" }, status: 202,
          data: {
            version: 1, input: question, authorizeCalls: 2, awaitLookupReceipt: true,
            attachment: { name: "本次接口原件.txt", data: Buffer.from(rule + "。").toString("base64") },
          },
        });
        const runPath = "v1/runs/" + launch.id;
        const ready = await waitFor(() => api(admin, runPath, { headers: auth }),
          value => value.stage === "review" || value.nextAction === "submit_lookup_receipt");
        assert.equal(ready.stage, "review", JSON.stringify(ready));
        assert.deepEqual(ready.steps.map(step => step.status), ["completed", "completed"]);
        const reviewer = await browser.newPage({ baseURL });
        await post(reviewer, "login", { email: "review@sample.invalid", password }, 200);
        await reviewer.goto(ready.humanActionUrl);
        const archive = await download(reviewer, "semantic-fresh-api.zip");
        const approved = await reviewer.request.get(ready.packageUrl, { headers: auth });
        assert.equal(approved.status(), 200, await approved.text());
        assert.ok((await approved.body()).equals(await readFile(out + "/" + archive)));
        return { project: ready.project, run: ready.id, steps: ready.steps.map(step => step.status), archive, answer: rule, position: 1 };
      })(),
    ]);
    results.freshProject = { ...results.freshProject, ...freshProject, position: 3 };
    results.service = { ...results.service, ...published };
    results.backlog = backlog;
    results.freshApi = app;
  } else if (resumeBacklog) {
    const held = results.backlog;
    await member.goto("/#" + held.project);
    const ready = await waitFor(() => detail(held.project), value => value.stage === "review");
    assert.equal(ready.run.id, held.run);
    assert.equal(ready.run.indexWait, null);
    assert.equal(ready.run.receiptRequest, null);
    assert.equal(ready.planVersion, 1);
    assert.equal(ready.costSummary.calls, 2, JSON.stringify(ready.artifacts.map(item => item.body)));
    assert.equal(ready.artifacts[0].id, held.initialSearch.id);
    assert.equal(ready.artifacts[0].body, held.initialSearch.body);
    assert.ok(ready.artifacts[1].body.includes(rule) && ready.artifacts[1].body.includes("原文依据"), ready.artifacts[1].body);
    const trace = await api(member, "projects/" + held.project + "/runs/" + held.run + "/trace");
    const events = trace.trace.events.map(event => event.code);
    assert.ok(events.includes("lookup_index_rechecked"), JSON.stringify(events));
    assert.deepEqual(trace.trace.spans.map(span => span.attempts.length), [1, 1]);
    results.resumedBacklog = {
      run: ready.run.id, project: held.project, position: held.position, answer: rule, events,
      calls: ready.costSummary.calls, archive: await download(member, "semantic-backlog-resumed.zip"),
    };
  } else {
    results.offline = await run(results.offline.project, "lookup_semantic_unavailable", 1);
    results.lexical = await run(results.lexical.project, "lookup_searched", 2, rule);
    assert.ok(!results.lexical.events.some(code => code.startsWith("lookup_semantic_")));
  }
  await writeFile(reportPath, JSON.stringify(results, null, 2) + "\n");
} finally {
  await browser.close();
}

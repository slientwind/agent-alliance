import assert from "node:assert/strict";
import { createHash, randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const {
  SMOKE_ORIGIN: baseURL, BROWSER_PASSWORD: password, QUICK_ENDPOINT: endpoint,
  OFFICE_INPUTS: documents, OFFICE_FIXTURES: samples, VISUAL_EVIDENCE: evidence,
} = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const member = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
  async function call(page, path, method = "GET", data, status, headers) {
    const response = await page.request.fetch("/api/" + path, { method, data, headers });
    if (status) assert.equal(response.status(), status, `${method} ${path}: ${await response.text()}`);
    else assert.ok(response.ok(), `${method} ${path}: ${await response.text()}`);
    return response.status() === 204 ? undefined : response.json();
  }
  const post = (page, path, data, status, headers) => call(page, path, "POST", data, status, headers);
  await post(admin, "login", { email: "review@sample.invalid", password });
  const team = (await call(admin, "teams")).find(row => row.default).id;
  const resource = await post(admin, "resources", {
    team, name: "办公原件阅读测试", endpoint, model: "fixture", key: "synthetic",
  });
  const agent = await post(admin, "agents", {
    name: "访谈回执核对", instructions: "根据实际资料正文核对访谈回执。", resource: resource.id, resourceRevision: 1,
  });
  await post(admin, `agents/${agent.id}/validate`, { revision: 1, input: "verify", expected: "RECEIPT", authorizeOneCall: true });
  await post(admin, `agents/${agent.id}/publish`, { revision: 1 });
  const service = await post(admin, "services", { name: "访谈材料核对", agent: agent.id, instructions: "整理访谈并返回审核回执" });
  const app = await post(admin, `services/${service.id}/applications`, { name: "归档系统", days: 1 });
  const auth = { Authorization: "Bearer " + app.token };
  assert.equal((await call(admin, `v1/services/${service.id}`, "GET", undefined, 200, auth)).allowDocumentAttachments, true);
  const invite = await post(admin, "admin/invitations", {});
  await post(member, "register", { name: "访谈运营人员", email: "service-office@sample.invalid", token: invite.token, password });

  const office = await Promise.all([
    [documents, "完整访谈记录.docx", "访谈回执", "段落 2"],
    [samples, "sales.xlsx", "12.50", "销售!D2"],
    [documents, "访谈摘要.pptx", "下一步由采购组确认交期", "幻灯片 2"],
    [samples, "training-doc.pdf", "核对设备编号", "PDF 第 1 页"],
  ].map(async ([dir, name, query, location]) => ({ name, query, location, bytes: await readFile(join(dir, name)) })));
  const original = (...files) => files.map(({ name, bytes }) => ({ name, data: bytes.toString("base64") }));
  const sha = bytes => createHash("sha256").update(bytes).digest("hex");
  const goal = "从已附的访谈材料核对实际回执。";
  const route = `v1/services/${service.id}/runs`;
  const start = (record, status = 202, key = randomUUID()) => post(admin, route, record, status, { ...auth, "Idempotency-Key": key });
  const request = { input: goal, version: 1, authorizeCalls: 1 };

  // A readable document mixed with an invalid one must leave the whole batch unaccepted.
  const invalid = { name: "损坏.docx", bytes: Buffer.from("invalid document") };
  const failed = await start({ ...request, attachments: original(office[0], invalid) }, 422);
  assert.match(failed.error, /损坏.docx/);
  const scan = { name: "scan.pdf", bytes: await readFile(join(documents, "scan.pdf")) };
  assert.match((await start({ ...request, attachment: original(scan)[0] }, 422)).error, /未找到可读取/);
  const large = { name: "large.docx", bytes: await readFile(join(documents, "large.docx")) };
  assert.match((await start({ ...request, attachment: original(large)[0] }, 422)).error, /超出可读取/);
  const partial = { name: "较长访谈.docx", bytes: await readFile(join(documents, "较长访谈.docx")) };
  const note = { name: "文字备忘.txt", bytes: Buffer.from("行".repeat(20000)) };
  assert.match((await start({ ...request, attachments: original(partial, note) }, 422)).error, /超过本次任务的 96 KiB/);
  const oversized = await post(admin, route, JSON.stringify(request) + " ".repeat(12 * 1024 * 1024), 413, {
    ...auth, "Content-Type": "application/json", "Idempotency-Key": randomUUID(),
  });
  assert.match(oversized.error, /大小/);

  const aggregation = await post(admin, "agents/tools", {
    team, name: "固定 CSV 汇总", description: "按地区汇总",
    tool: { operation: "aggregate", config: { group: "region", amount: "amount" } },
  });
  await post(admin, `agents/${aggregation.id}/validate`, { revision: 1, input: "region,amount\nEast,7.00", expected: "7.00", authorizeOneCall: true });
  await post(admin, `agents/${aggregation.id}/publish`, { revision: 1 });
  const csv = await post(admin, "services", { name: "固定 CSV 服务", agent: aggregation.id, instructions: "计算 CSV 汇总" });
  const csvApp = await post(admin, `services/${csv.id}/applications`, { name: "CSV 专用应用", days: 1 });
  const csvAuth = { Authorization: "Bearer " + csvApp.token };
  assert.equal((await call(admin, `v1/services/${csv.id}`, "GET", undefined, 200, csvAuth)).allowDocumentAttachments, false);
  assert.match((await post(admin, `v1/services/${csv.id}/runs`, { ...request, attachment: original(office[1])[0] }, 422, {
    ...csvAuth, "Idempotency-Key": randomUUID(),
  })).error, /需要 CSV/);

  const fixed = { ...request, attachments: original(...office) };
  assert.ok(Buffer.byteLength(JSON.stringify(fixed)) > 2 * 1024 * 1024);
  const signedKey = randomUUID();
  const signed = await start(fixed, 202, signedKey);
  assert.deepEqual(await start(fixed, 202, signedKey), signed);
  await start({ ...fixed, attachments: [...fixed.attachments].reverse() }, 409, signedKey);
  assert.deepEqual(await call(member, `projects/${signed.project}/sources`), []);
  await call(admin, `v1/runs/${signed.id}/delivery/package`, "GET", undefined, 409, auth);

  const webFile = { ...office[0], name: "访谈记录.docx", bytes: await readFile(join(documents, "访谈记录.docx")) };
  await member.goto("/?service=" + service.id);
  await member.getByLabel("向服务提出任务").fill(goal);
  const upload = member.getByLabel("附资料");
  assert.match(await upload.getAttribute("accept"), /\.docx.*\.pdf/);
  await upload.setInputFiles({ name: webFile.name, mimeType: "application/octet-stream", buffer: webFile.bytes });
  await member.getByRole("button", { name: "移除资料" }).waitFor();
  assert.ok(await member.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth));
  await member.locator(".service-form").screenshot({ path: join(evidence, "service-documents-mobile.png") });
  await member.getByLabel(/授权本次最多 1 次调用/).check();
  await member.getByRole("button", { name: "确认并启动对话" }).click();
  await member.waitForURL("**/#*");
  const webProject = (new URL(member.url())).hash.slice(1);

  async function filesFor(project, page, files) {
    const path = "projects/" + project;
    const recorded = await call(page, path + "/sources");
    assert.equal(recorded.length, files.length);
    for (const file of files) {
      const saved = recorded.find(item => item.name === file.name);
      assert.equal(saved?.version, 1, file.name);
      assert.equal(saved.extraction, "ready");
      const original = await page.request.get(`/api/${path}/sources/${saved.id}/content?version=1`);
      assert.equal(original.status(), 200, file.name);
      if (page === admin) {
        assert.equal((await member.request.get(`/api/${path}/sources/${saved.id}/content?version=1`)).status(), 404);
      }
      assert.equal(sha(await original.body()), sha(file.bytes));
      const { result: { hits } } = await call(page, path + "/retrieval?" + new URLSearchParams({ q: file.query }));
      assert.ok(hits.some(hit => hit.id === saved.id && hit.location === file.location && hit.version === 1), file.name);
    }
  }
  await filesFor(webProject, member, [webFile]);
  await filesFor(signed.project, admin, office);

  async function review(project, kind, headers) {
    const page = admin;
    await page.goto("/#" + project);
    await page.getByRole("button", { name: "审核此版本", exact: true }).click();
    await page.getByRole("button", { name: "确认并固定以上版本" }).click();
    await page.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
    const detail = await call(page, "projects/" + project);
    assert.equal(detail.costSummary.calls, 1);
    assert.equal(detail.artifacts[0].body, "RECEIPT source72");
    const url = headers ? `/api/v1/runs/${signed.id}/delivery/package` : `/api/projects/${project}/delivery/package`;
    const packageResult = await page.request.get(url, { headers });
    assert.ok(packageResult.ok(), await packageResult.text());
    await writeFile(join(evidence, `service-documents-${kind}.zip`), await packageResult.body());
    return detail.run.id;
  }
  const webRun = await review(webProject, "web");
  assert.equal(await review(signed.project, "api", auth), signed.id);
  await writeFile(join(evidence, "service-documents.json"), JSON.stringify({
    web: { project: webProject, run: webRun, sources: [webFile].map(({ name, location }) => ({ name, location })) },
    api: { ...signed, sources: office.map(({ name, location }) => ({ name, location })) },
    originalBytes: office.reduce((sum, file) => sum + file.bytes.length, 0),
    authorizationCalls: 1, byteIdentical: true, idempotent: true, originalVersion: 1,
  }, null, 2) + "\n");
} finally {
  await browser.close();
}

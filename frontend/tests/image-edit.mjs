import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const browser = await chromium.launch();
const context = await browser.newContext({
  baseURL: process.env.SMOKE_ORIGIN,
  viewport: { width: 390, height: 844 },
});
const page = await context.newPage();
const evidence = process.env.VISUAL_EVIDENCE;
const sha256 = bytes => createHash("sha256").update(bytes).digest("hex");

async function request(path, { method = "GET", status = 200, headers, data } = {}) {
  const response = await context.request.fetch(`/api/${path}`, { method, headers, data });
  assert.equal(response.status(), status, response.status() === status ? path : `${path}: ${await response.text()}`);
  return response;
}
async function post(path, data, status = 201, key) {
  const headers = key ? { "Idempotency-Key": key } : undefined;
  const response = await request(path, { data, status, headers, method: "POST" });
  return status === 204 ? null : response.json();
}
const detail = async project => (await request(project)).json();

async function waitForProject(project, predicate) {
  const deadline = Date.now() + 30000;
  let state = await detail(project);
  while (!predicate(state) && Date.now() < deadline) {
    await new Promise(resolve => setTimeout(resolve, 150));
    state = await detail(project);
  }
  assert.ok(predicate(state), JSON.stringify(state));
  return state;
}

async function feedback(file, version, body) {
  const { id } = await post(`${file}/feedback`, { version, kind: "change", body }, 200);
  return `${file}/feedback/${id}`;
}

const region = { x: 16, y: 32, width: 96, height: 80 };
const change = { version: 1, expected: 1, authorizeOneCall: true, imageRegion: region };

try {
  const email = "review@sample.invalid";
  await post("login", { email, password: process.env.BROWSER_PASSWORD }, 200);
  const resource = await post("resources", {
    name: "合成参考图编辑",
    operation: "image-edit",
    endpoint: process.env.QUICK_ENDPOINT,
    model: "synthetic-image-edit",
    estimate: "0",
    key: "synthetic-image-key",
  });
  const { team } = (await detail("resources")).find(item => item.id === resource.id);
  const probe = await post(`teams/${team}/setup/connections`, {
    resource: resource.id, revision: 1, authorizeOneCall: true,
  }, 200, "reference-edit-probe");
  assert.equal(probe.status, "passed");

  const original = Buffer.from(await page.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 640;
    canvas.height = 480;
    const graphics = canvas.getContext("2d");
    graphics.fillStyle = "#143d60";
    graphics.fillRect(0, 0, 640, 480);
    graphics.fillStyle = "#ee4718";
    graphics.fillRect(16, 32, 96, 80);
    return canvas.toDataURL("image/png").split(",")[1];
  }), "base64");
  const picture = { name: "reference.png", data: original.toString("base64") };
  const { id: agent } = await post("agents", {
    name: "参考图视觉修订", instructions: "按照反馈编辑参考图",
    resource: resource.id, resourceRevision: 1,
  });
  const agentPath = `agents/${agent}`;
  const sample = "EDIT_TRIAL 生成一份编辑后的样图";
  const expected = "PNG 图像";
  await request(`${agentPath}/checks/1`, { method: "PUT", status: 204, data: {
    version: 0, items: [{ name: "核验参考图编辑结果", sample, expected, match: "contains", reviewer: email }],
  } });
  const [check] = (await detail(`${agentPath}/checks/1`)).items;
  const trial = { revision: 1, input: sample, expected, checkID: check.id, authorizeOneCall: true };
  await post(`${agentPath}/validate`, trial, 422, "edit-trial-no-original");
  const result = await post(`${agentPath}/validate`, { ...trial, picture }, 200, "edit-trial");
  assert.equal(result.status, "passed");
  assert.deepEqual(await (await request(`${agentPath}/evidence/${result.id}/picture`)).body(), original);
  await post(`${agentPath}/publish`, { revision: 1 }, 409);
  await post(`${agentPath}/evidence/${result.id}/review`, {
    passed: true, reason: "已查看本次合成参考图及返回原件",
  }, 204);
  await post(`${agentPath}/publish`, { revision: 1 }, 204);

  const { id } = await post("projects", { goal: "EDIT_DIRECT 编辑新品视觉概念稿" });
  const project = `projects/${id}`;
  const { id: source } = await post(`${project}/sources?name=${picture.name}`, original);
  const plan = {
    expected: (await detail(project)).revision,
    brief: "EDIT_DIRECT 编辑固定图片并交付 PNG", agents: [agent],
    sources: [{ id: source, version: 1 }],
  };
  await post(`${project}/plan`, plan, 204);
  assert.equal((await detail(project)).plan.nodes[0].editsImage, true);
  await post(`${project}/runs`, {
    expected: (await detail(project)).revision, authorizeCalls: 1,
  }, 202, "edit-initial-run");
  const [artifact] = (await waitForProject(project, state => state.stage === "review")).artifacts;
  assert.equal(artifact.editMode, "image_edit");
  const file = `${project}/artifacts/${artifact.id}`;
  const base = await (await request(`${file}/file?version=1`)).body();
  const edit = await feedback(file, 1, "EDIT_LOCAL 把左上区域的标签换为浅色");
  await post(`${edit}/change-runs`, { ...change, imageRegion: { ...region, x: 639 } }, 422, "outside-base");

  await page.goto(`/#${id}`);
  await page.locator("details.feedback > summary").click();
  await page.getByText(`交给 ${artifact.name} 修改`, { exact: true }).click();
  await page.getByLabel("仅修改选区").check();
  await page.getByText("原图 640 × 480 像素", { exact: true }).waitFor();
  for (const [label, value] of [["左边距", region.x], ["上边距", region.y], ["选区宽度", region.width], ["选区高度", region.height]]) {
    await page.getByRole("spinbutton", { name: label, exact: true }).fill(String(value));
  }
  await page.getByLabel("授权负责此成果的 Agent", { exact: false }).check();
  const submission = page.waitForResponse(response => response.url().endsWith(`/${edit}/change-runs`));
  await page.getByRole("button", { name: "启动局部修改", exact: true }).click();
  const response = await submission;
  assert.equal(response.status(), 202, await response.text());
  const key = response.request().headers()["idempotency-key"];
  assert.deepEqual(response.request().postDataJSON().imageRegion, region);
  const resumed = await post(`${edit}/change-runs`, change, 202, key);
  assert.equal(resumed.run, (await response.json()).run);
  await post(`${edit}/change-runs`, { ...change, imageRegion: { ...region, x: 17 } }, 409, key);
  const revised = await waitForProject(project, state => state.artifacts[0].version === 2);
  assert.equal(revised.artifacts[0].approved, false);
  assert.equal(revised.costSummary.calls, 2);
  const revisedFile = await (await request(`${file}/file?version=2`)).body();
  const compared = await page.evaluate(async ({ base, revised, region }) => {
    async function decode(payload) {
      const blob = await (await fetch(`data:image/png;base64,${payload}`)).blob();
      const bitmap = await createImageBitmap(blob);
      const canvas = new OffscreenCanvas(bitmap.width, bitmap.height);
      const graphics = canvas.getContext("2d");
      graphics.drawImage(bitmap, 0, 0);
      return graphics.getImageData(0, 0, canvas.width, canvas.height);
    }
    const before = await decode(base);
    const after = await decode(revised);
    let inside = 0, outside = 0;
    for (let y = 0; y < before.height; y++) {
      for (let x = 0; x < before.width; x++) {
        const index = (y * before.width + x) * 4;
        if (!before.data.subarray(index, index + 4).some((value, channel) => value !== after.data[index + channel])) continue;
        if (x >= region.x && x < region.x + region.width && y >= region.y && y < region.y + region.height) inside++;
        else outside++;
      }
    }
    return { inside, outside, width: after.width, height: after.height };
  }, { base: base.toString("base64"), revised: revisedFile.toString("base64"), region });
  assert.deepEqual(compared, { inside: 7680, outside: 0, width: 640, height: 480 });
  assert.deepEqual(await (await request(`${file}/file?version=1`)).body(), base);
  await post(edit, { version: 2, expected: 2, action: "resolve", reason: "已核对选区外保持及指定区域的修订" }, 200);
  await page.reload();
  await page.getByRole("button", { name: "审核此版本", exact: true }).click();
  await page.getByRole("button", { name: "确认并固定以上版本", exact: true }).click();
  await page.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
  const delivered = await detail(project);
  assert.equal(delivered.stage, "delivered");
  await writeFile(`${evidence}/image-edit.zip`, await (await request(`${project}/delivery/package`)).body());
  await page.screenshot({ path: `${evidence}/image-edit-delivered.png` });

  const service = await post("services", { name: "编辑视觉概念稿", agent, instructions: "根据输入编辑参考图" });
  const app = await post(`services/${service.id}/applications`, { name: "新品样例", days: 1 });
  const auth = { Authorization: `Bearer ${app.token}` };
  const serviceAPI = `v1/services/${service.id}`;
  assert.equal((await (await request(serviceAPI, { headers: auth })).json()).editsImage, true);
  assert.equal((await detail(`services/${service.id}/entry`)).editsImage, true);
  const start = { version: 1, input: "EDIT_SERVICE 参考图替换", authorizeCalls: 1 };
  for (const attachments of [[], [picture, { ...picture, name: "extra.png" }]]) {
    await request(`${serviceAPI}/runs`, { method: "POST", status: 422,
      headers: { ...auth, "Idempotency-Key": `edit-invalid-count-${attachments.length}` }, data: { ...start, attachments } });
  }
  const serviceRun = await (await request(`${serviceAPI}/runs`, { method: "POST", status: 202,
    headers: { ...auth, "Idempotency-Key": "edit-service" }, data: { ...start, attachments: [picture] } })).json();
  const serviceProject = `projects/${serviceRun.project}`;
  const [serviceArtifact] = (await waitForProject(serviceProject, state => state.stage === "review")).artifacts;
  const serviceFile = `${serviceProject}/artifacts/${serviceArtifact.id}`;
  const denied = await feedback(serviceFile, 1, "EDIT_WRONG_SIZE 模拟供应商返回的尺寸不符");
  const blocked = await post(`${denied}/change-runs`, change, 202, "wrong-output-size");
  const failure = await waitForProject(serviceProject, state => state.artifacts[0].feedback.some(item =>
    item.changes.some(candidate => candidate.run === blocked.run && candidate.status === "reconcile")));
  assert.equal(failure.artifacts[0].version, 1);
  assert.deepEqual(await (await request(`${serviceFile}/file?version=1`)).body(), base);
  await writeFile(`${evidence}/image-edit.json`, JSON.stringify({
    project: id, service: service.id, original: sha256(original), base: sha256(base),
    modified: sha256(revisedFile), compared, deliveredVersion: 2,
    wrongSizeStatus: "reconcile",
  }, null, 2));
} finally {
  await browser.close();
}

import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const { SMOKE_ORIGIN: baseURL, QUICK_ENDPOINT: endpoint, BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: evidence, VISION_SAMPLES: samples } = process.env;
const browser = await chromium.launch();
const admin = await browser.newPage({ baseURL });
const member = await browser.newPage({ baseURL, viewport: { width: 390, height: 844 } });
const viewer = await browser.newPage({ baseURL });
const result = {};

async function request(page, path, { method = "GET", data, status = 200, headers } = {}) {
  const response = await page.request.fetch("/api/" + path, { method, data, headers });
  assert.equal(response.status(), status, path + " " + (await response.text()).slice(0, 500));
  return response;
}
async function post(page, path, data, status = 201, headers) {
  return (await request(page, path, { method: "POST", data, status, headers })).json();
}
const get = async (page, path) => (await request(page, path)).json();
const sample = async (name, mediaType) => ({ name, mimeType: mediaType, buffer: await readFile(join(samples, name)) });
const attachment = ({ name, buffer }) => ({ name, data: buffer.toString("base64") });

function uploadToComposer(file) {
  return member.locator('.composer input[type="file"]').setInputFiles(file);
}
const roleButton = (surface, name) => surface.getByRole("button", { name, exact: true });
const goal = member.getByRole("textbox", { name: "消息", exact: true });
const chooser = member.getByRole("dialog", { name: "Agent", exact: true });
const projectID = () => new URL(member.url()).hash.slice(1);

async function send() {
  await roleButton(member, "发送消息").click();
  await member.waitForURL(/#[0-9a-f-]+$/);
  return projectID();
}

async function approve(page, project, expected, calls = 1) {
  await roleButton(page, "审核此版本").click();
  await roleButton(page, "确认并固定以上版本").click();
  await page.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
  const detail = await get(page, "projects/" + project);
  assert.equal(detail.artifacts[0].body, expected);
  assert.equal(detail.costSummary.calls, calls);
  assert.equal(detail.stage, "delivered");
  return detail;
}

async function startFromPlan() {
  await member.getByLabel(/授权本次 1 次能力调用/).check();
  await roleButton(member, "确认并执行").click();
}

try {
  await post(admin, "login", { email: "review@sample.invalid", password }, 200);
  for (const [page, name, email] of [[member, "设备盘点经办", "vision-author@sample.invalid"], [viewer, "其他同事", "vision-viewer@sample.invalid"]]) {
    const { token } = await post(admin, "admin/invitations", {});
    await post(page, "register", { name, email, token, password });
  }

  const blue = await sample("board.png", "image/png");
  const red = await sample("replacement.png", "image/png");
  const green = await sample("board.jpg", "image/jpeg");
  await post(admin, "resources", { name: "合成视觉模型", endpoint, model: "vision-fixture", key: "synthetic", operation: "vision", estimate: "0" });
  const planner = await post(admin, "resources", { name: "图片任务规划模型", endpoint, model: "vision-planner-fixture", key: "synthetic", estimate: "0" });
  const { id: team } = (await get(admin, "teams")).find(t => t.default);
  await request(admin, `teams/${team}/setup/planner`, { method: "PUT", data: { resource: planner.id, revision: 1, expected: 0 }, status: 204 });

  await member.goto("/");
  await roleButton(member, "选择或创建 Agent").click();
  await roleButton(chooser, "创建 Agent").click();
  const description = "核对设备照片的标签颜色";
  await chooser.getByLabel("希望 Agent 完成什么任务？").fill(description);
  await chooser.getByLabel("成果类型").selectOption("vision");
  await chooser.getByText(/使用 合成视觉模型 v1/).waitFor();
  const [createdResponse] = await Promise.all([
    member.waitForResponse(r => r.url().endsWith("/api/agents") && r.request().method() === "POST"),
    roleButton(chooser, "创建并准备试运行").click(),
  ]);
  assert.equal(createdResponse.status(), 201);
  const { id: agent } = await createdResponse.json();
  const base = `agents/${agent}`;
  await chooser.getByLabel(/试运行图片/).setInputFiles(blue);
  await chooser.getByLabel("试运行输入").fill("指出标签颜色 VISION_TRIAL");
  await chooser.getByLabel("结果应包含").fill("蓝色标签");
  await chooser.getByLabel(/授权本次试运行/).check();
  const trialResponse = member.waitForResponse(r => r.url().endsWith(`/${base}/validate`));
  await roleButton(chooser, "运行示例").click();
  const trialRequest = await trialResponse;
  const trial = await trialRequest.json();
  assert.equal(trial.status, "passed", JSON.stringify(trial));
  assert.equal(trial.output, "蓝色标签");
  const key = trialRequest.request().headers()["idempotency-key"];
  const body = trialRequest.request().postDataJSON();
  const headers = { "Idempotency-Key": key };
  assert.equal((await post(member, `${base}/validate`, body, 200, headers)).id, trial.id);
  await post(member, `${base}/validate`, { ...body, picture: attachment(red) }, 409, headers);
  await post(member, `${base}/validate`, { ...body, picture: null }, 422);
  await post(member, `${base}/validate`, { ...body, picture: { name: "spoof.png", data: "aW52YWxpZA==" } }, 422);
  const trialPicture = `${base}/evidence/${trial.id}/picture`;
  assert.deepEqual(await (await request(member, trialPicture)).body(), blue.buffer);
  await request(viewer, trialPicture, { status: 404 });
  await chooser.getByText("试运行记录与回执", { exact: true }).click();
  await chooser.getByText("预览 board.png", { exact: true }).click();
  await member.waitForFunction(() => document.querySelector('img[alt="board.png"]')?.naturalWidth === 48);
  for (const width of [320, 390, 1440]) {
    await member.setViewportSize({ width, height: 844 });
    assert.ok(await chooser.evaluate(dialog => dialog.scrollWidth <= dialog.clientWidth), `vision trial dialog at ${width}px`);
  }
  await member.setViewportSize({ width: 390, height: 844 });
  await chooser.screenshot({ path: join(evidence, "vision-trial-mobile.png") });
  await request(admin, `${base}/publish`, { method: "POST", data: { revision: 1 }, status: 204 });
  result.trial = { privateOriginal: true, wrongBytesDenied: true, replay: true };

  const missing = await post(member, "projects", { goal: "补照片再核对", agents: [agent], agentRevisions: [1] });
  const missingProject = await get(member, "projects/" + missing.id);
  await post(member, `projects/${missing.id}/runs`, { expected: missingProject.revision, authorizeCalls: 1 }, 409,
    { "Idempotency-Key": randomUUID() });
  assert.equal((await get(member, "projects/" + missing.id)).run, null);

  const withheld = await post(member, "projects", { goal: "撤销输入后检查启动" });
  const withheldPath = `projects/${withheld.id}`;
  const withheldPicture = await post(member, `${withheldPath}/sources?name=board.png`, blue.buffer);
  const toConfirm = await get(member, withheldPath);
  await request(member, `${withheldPath}/plan`, { method: "POST", status: 204, data: {
    expected: toConfirm.revision, brief: "检查照片", agents: [agent], agentRevisions: [1],
    sources: [{ id: withheldPicture.id, version: 1 }],
  } });
  await request(member, `${withheldPath}/sources/${withheldPicture.id}`, { method: "DELETE", status: 204 });
  const withdrawn = await get(member, withheldPath);
  await post(member, `${withheldPath}/runs`, { expected: withdrawn.revision, authorizeCalls: 1 }, 409,
    { "Idempotency-Key": randomUUID() });
  const blocked = await get(member, withheldPath);
  assert.equal(blocked.run, null);
  assert.equal(blocked.costSummary.calls, 0);
  result.revocation = { blockedBeforeAdmission: true, chargedCalls: 0 };

  await member.goto("/");
  await goal.fill("核对设备照片的标签颜色 VISION_DIRECT");
  await uploadToComposer(blue);
  await roleButton(member, "选择或创建 Agent").click();
  await chooser.locator(".agent-choice").filter({ hasText: description }).click();
  const direct = await send();
  await member.getByText("读取本次引用的图片，提交可审核的文本。", { exact: true }).waitFor();
  const pinned = await get(member, "projects/" + direct);
  const [pin] = pinned.plan.sources;
  assert.deepEqual([pin.version, pin.picture], [1, true]);
  const source = `projects/${direct}/sources/${pin.id}`;
  const next = await request(member, `${source}?name=replacement.png&version=1`, { method: "PUT", data: red.buffer, status: 201 });
  assert.equal((await next.json()).version, 2);
  await request(viewer, `${source}/content?version=1`, { status: 404 });
  await member.reload();
  await startFromPlan();
  await member.getByText("资料依据核对 · 1", { exact: true }).click();
  const beforePicture = member.getByRole("region", { name: "方案所用图片" });
  const afterPicture = member.getByRole("region", { name: "当前版本图片" });
  await beforePicture.getByText("预览 board.png", { exact: true }).click();
  await afterPicture.getByText("预览 replacement.png", { exact: true }).click();
  await beforePicture.getByRole("img", { name: "board.png" }).waitFor();
  await afterPicture.getByRole("img", { name: "replacement.png" }).waitFor();
  await member.locator(".source-picture-versions").screenshot({ path: join(evidence, "vision-source-versions-mobile.png") });
  await member.getByLabel("处理方式").selectOption("retain");
  await member.getByLabel("核对依据或所需修改").fill("本次记录原设备状态，沿用方案中的原图。");
  await roleButton(member, "记录资料影响").click();
  await approve(member, direct, "蓝色标签");
  result.direct = { sourceIDs: [pin.id], pin: 1, currentSource: 2, reviewedText: "蓝色标签" };
  const downloaded = await request(member, `projects/${direct}/delivery/package`);
  await writeFile(join(evidence, "vision-direct.zip"), await downloaded.body());

  await member.goto("/");
  await goal.fill("按图片核对标签 VISION_PLANNING");
  await uploadToComposer(blue);
  const plannedProject = await send();
  const planningButton = roleButton(member, "请助手规划");
  const planning = member.locator("details").filter({ has: member.getByRole("button", { name: "请助手规划", exact: true, includeHidden: true }) }).last();
  if (!(await planning.evaluate(el => el.open))) await planning.locator("summary").first().click();
  const scope = member.getByRole("group", { name: "本次使用的资料" });
  await scope.getByLabel(/board.png/).waitFor();
  assert.ok(await scope.getByLabel(/board.png/).isChecked());
  const planningCall = member.waitForResponse(r => r.url().endsWith("/suggestions") && r.request().method() === "POST");
  await member.getByLabel(/授权一次规划调用/).check();
  await planningButton.click();
  assert.ok((await planningCall).ok());
  await roleButton(member, "采用为待确认方案").click();
  await startFromPlan();
  await roleButton(member, "审核此版本").waitFor();
  const planned = await get(member, "projects/" + plannedProject);
  assert.equal(planned.plan.nodes[0].agent, agent);
  assert.equal(planned.artifacts[0].body, "蓝色标签");
  result.planning = { reversibleScope: true, selectedPublishedAgent: true, imageAvailableOnlyToRunner: true };

  const plannedBase = "projects/" + plannedProject;
  const plannedSource = planned.plan.sources[0].id;
  const plannedItem = planned.artifacts[0].id;
  const plannedArtifact = `${plannedBase}/artifacts/${plannedItem}`;
  const revisedPicture = `${plannedBase}/sources/${plannedSource}?name=replacement.png&version=1`;
  await request(member, revisedPicture, { method: "PUT", data: red.buffer, status: 201 });
  const sourceDecision = `${plannedArtifact}/source-assessments/${plannedSource}`;
  const change = { version: 1, pinned: 1, observed: 2, revoked: false, decision: "change", reason: "使用新版图片核对当前标签 VISION_REVISION" };
  await request(member, sourceDecision, { method: "POST", data: change, status: 204 });
  await member.reload();
  await member.locator("details.feedback > summary").click();
  await member.getByText(/^交给 .+ 修改$/).click();
  await member.getByLabel(/仅本次改稿使用/).check();
  await member.getByLabel(/授权负责此成果的 Agent/).check();
  await roleButton(member, "启动局部修改").click();
  await member.getByText("红色标签", { exact: true }).first().waitFor();
  const updated = (await get(member, plannedBase)).artifacts[0];
  const feedback = updated.feedback[0];
  assert.equal(updated.version, 2);
  await request(member, `${plannedArtifact}/feedback/${feedback.id}`, { method: "POST", status: 200,
    data: { version: 2, expected: feedback.revision, action: "resolve", reason: "已核对新版标签" } });
  await request(member, sourceDecision, { method: "POST", status: 204,
    data: { ...change, version: 2, decision: "retain", reason: "修订已经核对引用的第二版图片。" } });
  await member.reload();
  await approve(member, plannedProject, "红色标签", 3);
  const versions = await get(member, `${plannedArtifact}/versions`);
  assert.equal(versions[0].sourceInput.selected, 2);
  assert.equal(versions[1].sourceInput, null);
  result.revision = { originalPin: 1, revisedInput: 2, reviewedRevision: 2 };

  const layout = await post(admin, "agents/tools", { team, name: "设备核对 Word 排版", description: "排版上一步的核对说明", tool: { operation: "docx" } });
  const layoutTrial = await post(admin, `agents/${layout.id}/validate`, { revision: 1, input: "设备核对样例", expected: "设备核对", authorizeOneCall: true }, 200);
  assert.equal(layoutTrial.status, "passed");
  await request(admin, `agents/${layout.id}/publish`, { method: "POST", data: { revision: 1 }, status: 204 });
  await member.goto("/");
  await goal.fill("核对照片，再生成 Word 和记载的文字 VISION_COMPOSE");
  await uploadToComposer(green);
  await roleButton(member, "选择或创建 Agent").click();
  await roleButton(chooser, "组合 Agent").click();
  await chooser.locator(".agent-choice").filter({ hasText: description }).click();
  await chooser.locator(".agent-choice").filter({ hasText: "设备核对 Word 排版" }).click();
  await roleButton(chooser, "使用此组合").click();
  const composed = await send();
  await member.getByLabel(/授权本次 2 次能力调用/).check();
  await roleButton(member, "确认并执行").click();
  for (let i = 0; i < 2; i++) await roleButton(member, "审核此版本").first().click();
  await roleButton(member, "确认并固定以上版本").click();
  await member.getByRole("link", { name: "下载交付包（ZIP）" }).waitFor();
  const combination = await get(member, `projects/${composed}`);
  assert.deepEqual(combination.plan.nodes.map(node => node.agent), [agent, layout.id]);
  assert.deepEqual(combination.plan.nodes[1].dependsOn, [1]);
  assert.equal(combination.costSummary.calls, 2);
  await writeFile(join(evidence, "vision-word.zip"), await (await request(member, `projects/${composed}/delivery/package`)).body());
  result.composition = { recognitionThenWord: true, separatelyReviewed: 2, calls: 2 };

  const service = await post(admin, "services", { name: "设备照片核对", agent, instructions: "核对照片标签" });
  const app = await post(admin, `services/${service.id}/applications`, { name: "资产归档样例", days: 1 });
  const auth = { Authorization: "Bearer " + app.token };
  assert.equal((await request(admin, `v1/services/${service.id}`, { headers: auth }).then(r => r.json())).requiresPictures, true);
  assert.equal((await get(member, `services/${service.id}/entry`)).requiresPictures, true);
  const runPath = `v1/services/${service.id}/runs`;
  const run = { input: "确认设备照片的标签颜色 VISION_API", version: 1, authorizeCalls: 1 };
  await post(admin, runPath, run, 422, { ...auth, "Idempotency-Key": randomUUID() });
  await post(admin, runPath, { ...run, attachment: { name: "broken.jpg", data: "aW52YWxpZA==" } }, 422, { ...auth, "Idempotency-Key": randomUUID() });
  const requestHeaders = { ...auth, "Idempotency-Key": randomUUID() };
  const signedBody = { ...run, attachments: [attachment(green), attachment(red)] };
  const signed = await post(admin, runPath, signedBody, 202, requestHeaders);
  assert.deepEqual(await post(admin, runPath, signedBody, 202, requestHeaders), signed);
  await post(admin, runPath, { ...run, attachments: [...signedBody.attachments].reverse() }, 409, requestHeaders);
  await request(admin, `v1/runs/${signed.id}/delivery/package`, { headers: auth, status: 409 });
  await admin.goto("/#" + signed.project);
  const signedProject = await approve(admin, signed.project, "绿色标签，红色标签");
  await writeFile(join(evidence, "vision-api.zip"), await (await request(admin, `v1/runs/${signed.id}/delivery/package`, { headers: auth })).body());

  await member.goto("/?service=" + service.id);
  await member.getByLabel("向服务提出任务").fill("确认标签颜色 VISION_WEB");
  const confirm = roleButton(member, "确认并启动对话");
  await member.getByLabel(/授权本次最多 1 次调用/).check();
  assert.ok(await confirm.isDisabled());
  await member.getByLabel("附资料").setInputFiles(green);
  await member.getByLabel(/授权本次最多 1 次调用/).check();
  await confirm.click();
  await member.waitForURL(/#[0-9a-f-]+$/);
  const web = projectID();
  await admin.goto("/#" + web);
  const webProject = await approve(admin, web, "绿色标签");
  await writeFile(join(evidence, "vision-web.zip"), await (await request(admin, `projects/${web}/delivery/package`)).body());
  result.service = { id: service.id, requiresPictures: true, deniedMissingOrCorrupt: true, signedReplay: true, humanApproval: true, webAndAPI: true };
  result.web = { sourceIDs: webProject.plan.sources.map(pin => pin.id) };
  result.api = { sourceIDs: signedProject.plan.sources.map(pin => pin.id), orderedPictures: true };
  await writeFile(join(evidence, "vision-browser.json"), JSON.stringify(result, null, 2) + "\n");
} catch (error) {
  console.error(await member.locator("body").innerText());
  throw error;
} finally {
  await browser.close();
}

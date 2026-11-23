import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { writeFile } from "node:fs/promises";
import { chromium } from "playwright";

const browser = await chromium.launch();
try {
  const context = await browser.newContext({
    baseURL: process.env.SMOKE_ORIGIN,
  });
  const out = process.env.VISUAL_EVIDENCE;
  async function call(path, method, data, status, key) {
    const rsp = await context.request.fetch("/api/" + path, {
      method,
      data,
      headers: key ? { "Idempotency-Key": key } : {},
    });
    assert.equal(
      rsp.status(),
      status,
      path + " " + (rsp.ok() ? "" : await rsp.text()),
    );
    if (rsp.headers()["content-type"]?.includes("json")) return rsp.json();
  }
  const email = "setup@sample.invalid";
  await call(
    "login",
    "POST",
    { email, password: process.env.BROWSER_PASSWORD },
    200,
  );
  const m = await call(
    "resources",
    "POST",
    {
      name: "新品视觉",
      operation: "png",
      endpoint: process.env.SETUP_ENDPOINT,
      model: "synthetic-image",
      estimate: "0",
      key: "synthetic-image-key",
    },
    201,
  );
  const { team, operation } = (
    await call("resources", "GET", undefined, 200)
  ).find((x) => x.id === m.id);
  assert.equal(operation, "png");
  const setup = "teams/" + team + "/setup";
  const probe = await call(
    setup + "/connections",
    "POST",
    { resource: m.id, revision: 1, authorizeOneCall: true },
    200,
    "png-probe",
  );
  assert.equal(probe.detail, "PNG 图像 · 320 × 192 像素");
  const state = (await call(setup, "GET", undefined, 200)).state;
  const badPlanner = await context.request.put("/api/" + setup + "/planner", {
    data: {
      resource: m.id,
      revision: 1,
      expected: state.planner?.version || 0,
    },
  });
  assert.ok([409, 422].includes(badPlanner.status()), await badPlanner.text());
  const { id: agent } = await call(
    "agents",
    "POST",
    {
      name: "视觉概念稿",
      instructions: "为新品制作一张概念图",
      resource: m.id,
      resourceRevision: 1,
    },
    201,
  );
  const path = "agents/" + agent;
  const sample = "为新品生成几何海报",
    expected = "PNG 图像";
  const trial = {
    revision: 1,
    input: sample,
    expected,
    authorizeOneCall: true,
  };
  await call(path + "/validate", "POST", trial, 422, "missing-image-reviewer");
  await call(
    path + "/checks/1",
    "PUT",
    {
      version: 0,
      items: [
        {
          name: "人工核验海报构图",
          sample,
          expected,
          match: "contains",
          reviewer: email,
        },
      ],
    },
    204,
  );
  const check = (await call(path + "/checks/1", "GET", undefined, 200))
    .items[0];
  for (const input of ["CORRUPT_IMAGE", "URL_ONLY_IMAGE"]) {
    const failed = await call(
      path + "/validate",
      "POST",
      { ...trial, input },
      200,
      input,
    );
    assert.equal(failed.status, "failed");
    assert.equal(
      (
        await context.request.get(
          "/api/" + path + "/evidence/" + failed.id + "/file",
        )
      ).status(),
      404,
    );
  }
  const evidence = await call(
    path + "/validate",
    "POST",
    { ...trial, checkID: check.id },
    200,
    "png-trial",
  );
  assert.equal(evidence.status, "passed");
  await call(path + "/publish", "POST", { revision: 1 }, 409);
  const checkFile = await context.request.get(
    "/api/" + path + "/evidence/" + evidence.id + "/file",
  );
  assert.equal(checkFile.status(), 200);
  const original = await checkFile.body();
  await call(
    path + "/evidence/" + evidence.id + "/review",
    "POST",
    { passed: true, reason: "样图解码成功，人工核对合成样例中的圆形构图" },
    204,
  );
  await call(path + "/publish", "POST", { revision: 1 }, 204);
  const { id } = await call(
    "projects",
    "POST",
    { goal: "为新品生成视觉概念稿并交付 PNG" },
    201,
  );
  const project = "projects/" + id;
  const detail = () => call(project, "GET", undefined, 200);
  const candidates = await call(
    project + "/candidates?output=png",
    "GET",
    undefined,
    200,
  );
  assert.ok(
    candidates.some(
      (c) => c.id === agent && c.operation === "png" && c.state === "ready",
    ),
  );
  await call(
    project + "/plan",
    "POST",
    { expected: (await detail()).revision, brief: sample, agents: [agent] },
    204,
  );
  assert.equal((await detail()).plan.nodes[0].operation, "png");
  const launch = await context.request.post("/api/" + project + "/runs", {
    data: { expected: (await detail()).revision, authorizeCalls: 1 },
    headers: { "Idempotency-Key": "image-project-run" },
  });
  assert.ok(launch.ok(), await launch.text());
  const page = await context.newPage();
  await page.goto("/#" + id);
  await page.getByText("预览 image.png", { exact: true }).first().click();
  const preview = page.locator('img[alt="image.png"]').first();
  await preview.waitFor();
  await page.waitForFunction(
    () => document.querySelector('img[alt="image.png"]')?.naturalWidth === 320,
  );
  await preview.screenshot({ path: out + "/image-preview.png" });
  const artifact = (await detail()).artifacts[0];
  assert.equal(artifact.editMode, "regenerate");
  const file = project + "/artifacts/" + artifact.id;
  const feedback = await call(
    file + "/feedback",
    "POST",
    { version: 1, kind: "change", body: "MOVE_RIGHT_PNG 把圆形向右移动" },
    200,
  );
  const change = file + "/feedback/" + feedback.id + "/change-runs";
  await call(
    change,
    "POST",
    { version: 1, expected: 1, authorizeOneCall: true, excerpt: "PNG" },
    422,
    "bad-png-excerpt",
  );
  await call(
    change,
    "POST",
    { version: 1, expected: 1, authorizeOneCall: true },
    202,
    "regenerate-png",
  );
  const deadline = Date.now() + 30000;
  let current = (await detail()).artifacts[0];
  while (current.version === 1 && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 150));
    current = (await detail()).artifacts[0];
  }
  assert.equal(current.version, 2);
  assert.equal(current.file, "image.png");
  assert.equal(current.approved, false);
  const newFile = await context.request.get("/api/" + file + "/file?version=2");
  assert.equal(newFile.status(), 200);
  assert.notDeepEqual(await newFile.body(), original);
  await call(
    file + "/feedback/" + feedback.id,
    "POST",
    {
      version: 2,
      expected: 2,
      action: "resolve",
      reason: "核对了重新生成的完整画面",
    },
    200,
  );
  const external = Buffer.from(
    await page.evaluate(() => {
      const c = document.createElement("canvas");
      c.width = 320;
      c.height = 192;
      const brush = c.getContext("2d");
      brush.fillStyle = "#555";
      brush.fillRect(0, 0, 320, 192);
      return c.toDataURL("image/png").split(",")[1];
    }),
    "base64",
  );
  async function upload(buffer) {
    return context.request.post("/api/" + file + "/file-revisions", {
      multipart: {
        version: "2",
        reason: "已在外部改善对比度",
        summary: "精修 PNG",
        file: { name: "edited.png", mimeType: "image/png", buffer },
      },
    });
  }
  assert.equal((await upload(Buffer.from("invalid"))).status(), 422);
  const accepted = await upload(external);
  assert.ok(accepted.ok(), await accepted.text());
  const first = await context.request.get("/api/" + file + "/file?version=1");
  assert.deepEqual(await first.body(), original);
  await page.reload();
  await page.getByRole("button", { name: "审核此版本" }).click();
  const confirmed = page.waitForResponse(
    (r) =>
      r.url().endsWith("/" + project + "/deliver") &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "确认并固定以上版本" }).click();
  const response = await confirmed;
  assert.ok(response.ok(), await response.text());
  await page.getByText("下载交付清单与成果正文", { exact: true }).waitFor();
  assert.equal((await detail()).stage, "delivered");
  const zip = await context.request.get(
    "/api/" + project + "/delivery/package",
  );
  assert.equal(zip.status(), 200);
  await writeFile(out + "/image-delivery.zip", await zip.body());
  const service = await call(
    "services",
    "POST",
    { name: "新品概念图服务", agent, instructions: "为新产品制作概念图" },
    201,
  );
  const app = await call(
    "services/" + service.id + "/applications",
    "POST",
    { name: "业务资料台", days: 1 },
    201,
  );
  const descriptor = await context.request.get(
    "/api/v1/services/" + service.id,
    { headers: { Authorization: "Bearer " + app.token } },
  );
  assert.equal(descriptor.status(), 200);
  assert.equal((await descriptor.json()).steps[0].operation, "png");
  await page.goto("/?service=" + service.id);
  await page.getByText("协作步骤 · 1", { exact: true }).click();
  await page.getByText(/PNG 图像 · 可预览、下载及外部修订回传/).waitFor();

  await writeFile(
    out + "/image-generation.json",
    JSON.stringify(
      {
        provider: "synthetic HTTPS",
        project: id,
        trial: evidence.id,
        operation,
        preview: [320, 192],
        versions: 3,
        serviceOperation: "png",
        sha256: createHash("sha256").update(external).digest("hex"),
      },
      null,
      2,
    ),
  );
} finally {
  await browser.close();
}

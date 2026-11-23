import assert from "node:assert/strict";

export async function checkImageRevision({
  admin,
  user,
  endpoint,
  team,
  post,
  get,
}) {
  const model = await post(admin, "resources", {
    team,
    name: "样图复验模型",
    operation: "png",
    endpoint,
    model: "synthetic-image",
    key: "synthetic-image-key",
  });
  const { id } = await post(user, "agents", {
    name: "样图版式整理",
    instructions: "在中央画一个圆形",
    resource: model.id,
    resourceRevision: 1,
  });
  const route = "agents/" + id;
  const check = {
    name: "查看样图圆形位置",
    sample: "绘制灰阶培训图",
    expected: "PNG 图像",
    match: "contains",
    reviewer: "review@sample.invalid",
  };
  const configured = await user.request.put("/api/" + route + "/checks/1", {
    data: { version: 0, items: [check] },
  });
  assert.equal(configured.status(), 204, await configured.text());
  await admin.goto("/");
  const pixels = [];
  for (const revision of [1, 2]) {
    if (revision === 2)
      await post(user, route + "/instructions", {
        expected: 1,
        instructions: "将圆形向右排版 MOVE_RIGHT_PNG",
      });
    const suite = await get(user, route + "/checks/" + revision);
    const item = suite.items[0];
    assert.equal(item.reviewer, check.reviewer);
    assert.equal(item.effectiveTrial, null);
    const trial = await post(
      user,
      route + "/validate",
      {
        revision,
        checkID: item.id,
        input: item.sample,
        expected: item.expected,
        authorizeOneCall: true,
      },
      200,
    );
    assert.equal(trial.status, "passed", trial.output);
    assert.equal(
      (
        await admin.request.post("/api/" + route + "/publish", {
          data: { revision },
        })
      ).status(),
      409,
    );
    const path = "/api/" + route + "/evidence/" + trial.id + "/file";
    const colors = await admin.evaluate(async (path) => {
      const file = await fetch(path);
      if (!file.ok) throw Error("缺少样图");
      const bitmap = await createImageBitmap(await file.blob());
      const canvas = new OffscreenCanvas(bitmap.width, bitmap.height);
      const ctx = canvas.getContext("2d");
      ctx.drawImage(bitmap, 0, 0);
      bitmap.close();
      return [100, 250].map((x) => ctx.getImageData(x, 96, 1, 1).data[0]);
    }, path);
    assert.deepEqual(colors, revision === 1 ? [36, 232] : [232, 36]);
    pixels.push(colors);
    await post(
      admin,
      route + "/evidence/" + trial.id + "/review",
      {
        passed: true,
        reason: revision === 1 ? "圆形位于中央" : "圆形已向右移动",
      },
      204,
    );
    await post(admin, route + "/publish", { revision }, 204);
  }
  return {
    provider: "synthetic PNG fixture",
    reviewedRevisions: [1, 2],
    pixels,
    unreviewedPublication: 409,
  };
}

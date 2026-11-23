import assert from "node:assert/strict";

export async function checkDraftBoundaries({
  admin,
  user,
  endpoint,
  team,
  post,
  get,
  samplePath,
}) {
  const resource = await post(admin, "resources", {
    team,
    name: "已固定的工单模型",
    endpoint,
    model: "setup-text",
    key: "synthetic-setup-key",
  });
  const { id } = await post(user, "agents", {
    name: "专用工单复核",
    instructions: "核对工单。",
    resource: resource.id,
    resourceRevision: 1,
    key: "synthetic-independent",
  });
  const route = "agents/" + id;
  const check = {
    name: "核对独立模型授权",
    sample: "CHECK_KEY_BINDING",
    expected: "independent",
    match: "exact",
  };
  const setup = await user.request.put("/api/" + route + "/checks/1", {
    data: { version: 0, items: [check] },
  });
  assert.equal(setup.status(), 204, await setup.text());
  async function verify(revision) {
    const suite = await get(user, route + "/checks/" + revision);
    assert.equal(suite.items.length, 1);
    const item = suite.items[0];
    assert.equal(item.effectiveTrial, null);
    const result = await post(
      user,
      route + "/validate",
      {
        revision,
        input: item.sample,
        expected: item.expected,
        checkID: item.id,
        authorizeOneCall: true,
      },
      200,
    );
    assert.equal(result.status, "passed", result.output);
    assert.equal(result.output, "independent");
    assert.equal(
      (
        await user.request.get(
          "/api/" + route + "/evidence/" + result.id + "/sample",
        )
      ).status(),
      404,
    );
    return item.id;
  }
  const oldCheck = await verify(1);
  await post(admin, route + "/publish", { revision: 1 }, 204);
  const original = await post(user, "projects", {
    team,
    goal: "核对工单修订",
    agent: id,
    agentRevision: 1,
  });
  const projectTrial = await post(
    user,
    route + "/validate",
    {
      revision: 1,
      input: "CHECK_KEY_BINDING",
      expected: "independent",
      project: original.id,
      authorizeOneCall: true,
    },
    200,
  );
  assert.equal(projectTrial.status, "passed");
  assert.equal(
    (
      await user.request.get(
        "/api/" + route + "/evidence/" + projectTrial.id + "/sample",
      )
    ).status(),
    404,
  );
  await post(admin, "resources", {
    id: resource.id,
    expected: 1,
    name: "工单模型新版本",
    endpoint,
    model: "fixture",
    key: "synthetic-new",
  });
  const updates = await Promise.all(
    ["核对工单并给出结论。", "先核对编号，再给出结论。"].map((instructions) =>
      user.request.post("/api/" + route + "/instructions", {
        data: { expected: 1, instructions },
      }),
    ),
  );
  assert.deepEqual(updates.map((r) => r.status()).sort(), [201, 409]);
  const current = (await get(user, "agents")).find((a) => a.id === id);
  assert.equal(current.revision, 2);
  assert.equal(current.resourceRevision, 1);
  assert.equal(current.validated, false);
  assert.equal(current.published, 1);
  assert.equal(current.publishedReady, true);
  const newCheck = await verify(2);
  assert.notEqual(oldCheck, newCheck);
  await post(admin, route + "/publish", { revision: 2 }, 204);
  assert.equal(
    (await get(user, "projects/" + original.id)).plan.nodes[0].revision,
    1,
  );

  const who = await get(user, "me");
  const removed = await admin.request.put(
    "/api/admin/teams/" + team + "/members/" + who.id,
    {
      data: { joined: false, reason: "结束草稿复测" },
    },
  );
  assert.equal(removed.status(), 204, await removed.text());
  assert.equal((await user.request.get("/api/" + samplePath)).status(), 404);
  assert.equal(
    (
      await user.request.post("/api/" + route + "/instructions", {
        data: { expected: 2, instructions: "失去归属后的修订" },
      })
    ).status(),
    404,
  );
  return {
    independentCredentialVerifiedOn: [1, 2],
    pinnedModelRevision: 1,
    concurrentSaves: updates.map((r) => r.status()).sort(),
    requiredCheckInherited: true,
    oldProjectRevision: 1,
    projectAndRequiredSampleAccess: 404,
    revokedSampleAccess: 404,
  };
}

export async function checkConcurrentEdit({ admin, user, model, post, get }) {
  const name = "多人修改工单职责";
  const { id } = await post(user, "agents", {
    name,
    instructions: "按工单归类。",
    resource: model.id,
    resourceRevision: 1,
  });
  await user.getByRole("button", { name: "新对话", exact: true }).click();
  await user.getByRole("button", { name: "选择或创建 Agent" }).click();
  const panel = user.getByRole("dialog", { name: "Agent", exact: true });
  await panel.getByText(/我的文本草稿/).click();
  await panel.getByRole("button", { name: new RegExp(name) }).click();
  await panel.getByText("调整 Agent 职责", { exact: true }).click();
  const field = panel.getByLabel("调整后的职责");
  const mine = "提取编号和报修地点。";
  const other = "提取编号，保留字母大小写。";
  await field.fill(mine);
  await post(admin, "agents/" + id + "/instructions", {
    expected: 1,
    instructions: other,
  });
  const save = panel.getByRole("button", { name: "保存职责为新草稿" });
  await save.click();
  await panel.getByText(other, { exact: true }).waitFor();
  assert.equal(await field.inputValue(), mine);
  assert.ok(await save.isDisabled());
  await panel.getByRole("button", { name: "以 v2 为基础继续编辑" }).click();
  const merged = "保留编号字母大小写，同时提取报修地点。";
  await field.fill(merged);
  await save.click();
  await panel.getByRole("heading", { name: name + " · v3" }).waitFor();
  const agent = (await get(user, "agents")).find((a) => a.id === id);
  assert.equal(agent.instructions, merged);
  assert.equal(agent.revision, 3);
  assert.equal(agent.published, 0);
}

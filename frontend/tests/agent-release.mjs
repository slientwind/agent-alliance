import assert from "node:assert/strict";

export async function releaseInBrowser({
  admin, user, view, id, name, revision, get, returnFirst = false, screenshot,
}) {
  const prefix = "/api/agents/" + id + "/release-requests";
  const find = async () => (await get(user, "agents")).find(agent => agent.id === id).release;
  async function request(label) {
    const response = user.waitForResponse(r => r.url().endsWith(prefix) && r.request().method() === "POST");
    await view.getByRole("button", { name: label, exact: true }).click();
    assert.equal((await response).status(), 200);
    await view.getByText("已申请发布 v" + revision, { exact: false }).waitFor();
    return (await find()).id;
  }
  async function openQueue() {
    await admin.goto("/");
    await admin.getByRole("button", { name: "我的工作区" }).click();
    await admin.getByRole("button", { name: "处理 Agent 发布申请" }).click();
    return admin.getByRole("region", { name: name + " v" + revision + " 发布申请", exact: true });
  }
  let current = await request("申请发布此版本");
  const duplicate = await user.request.post(prefix, { data: { revision } });
  assert.equal(duplicate.status(), 200);
  assert.equal((await duplicate.json()).id, current);
  const denied = await user.request.post(prefix + "/" + current, { data: { revision, action: "publish" } });
  assert.equal(denied.status(), 403);
  let card = await openQueue();
  const reason = "请核对本版工单编号的原始样例。";
  if (returnFirst) {
    await card.getByText("退回修改", { exact: true }).click();
    await card.getByLabel("需要修改的地方").fill(reason);
    await card.getByRole("button", { name: "确认退回" }).click();
    await card.waitFor({ state: "detached" });
    await view.getByRole("button", { name: "查看发布状态" }).click();
    await view.getByText("退回原因 · " + reason).waitFor();
    const stale = await user.request.post(prefix, { data: { revision } });
    assert.equal(stale.status(), 409);
    const old = current;
    current = await request("重新申请发布");
    assert.notEqual(current, old);
    const oldDecision = await admin.request.post(prefix + "/" + old, { data: { revision, action: "publish" } });
    assert.equal(oldDecision.status(), 409);
    card = await openQueue();
  }
  await card.getByText("试运行记录与回执", { exact: true }).click();
  await card.getByText("v" + revision + " · 验证通过", { exact: true }).waitFor();
  if (screenshot) await card.screenshot({ path: screenshot });
  await card.getByRole("button", { name: "查看此版验收记录" }).click();
  assert.equal(await admin.evaluate(() => document.activeElement.id), "library-agent-" + id);
  await card.getByRole("button", { name: "确认发布 v" + revision }).click();
  await card.waitFor({ state: "detached" });
  assert.equal((await find()).state, "published");
  return { version: revision, returned: returnFirst, publishedFromInbox: true, memberDecisionStatus: denied.status() };
}

export async function checkReleaseBoundaries({ admin, user, endpoint, post, get }) {
  const { id: team } = await post(admin, "admin/teams", { name: "发布申请权限验证" });
  async function membership(member, joined) {
    const response = await admin.request.put("/api/admin/teams/" + team + "/members/" + member, {
      data: { joined, reason: "核查管理员和成员当前归属" },
    });
    assert.equal(response.status(), 204, await response.text());
  }
  const member = await get(user, "me");
  await membership(member.id, true);
  const model = await post(admin, "resources", {
    team, name: "发布申请验证模型", endpoint, model: "revision-fixture", key: "synthetic",
  });
  const { id } = await post(user, "agents", {
    name: "发布申请边界验证", instructions: "从报修单中只输出工单编号。",
    resource: model.id, resourceRevision: 1,
  });
  const route = "agents/" + id;
  const requests = route + "/release-requests";
  const submit = (data) => user.request.post("/api/" + requests, { data });
  const decide = (id, data) => admin.request.post("/api/" + requests + "/" + id, { data });
  const listed = async (page) => (await get(page, "agents")).find(item => item.id === id);
  const sample = { revision: 1, input: "抽取编号 SRV-405", expected: "SRV-405", authorizeOneCall: true };
  assert.equal((await submit({ revision: 1 })).status(), 409);
  const proof = await post(user, route + "/validate", sample, 200);
  assert.equal(proof.status, "passed", proof.output);
  const [a, b] = await Promise.all([submit({ revision: 1 }), submit({ revision: 1 })]);
  assert.equal(a.status(), 200);
  assert.equal(b.status(), 200);
  const request = (await a.json()).id;
  assert.equal((await b.json()).id, request);
  assert.equal((await listed(user)).release.hasPublisher, true);
  const administrator = await get(admin, "me");
  await membership(administrator.id, false);
  assert.equal(await listed(admin), undefined);
  assert.equal((await listed(user)).release.hasPublisher, false);
  const unscoped = await decide(request, { revision: 1, action: "publish" });
  assert.equal(unscoped.status(), 404);
  await membership(administrator.id, true);
  await membership(member.id, false);
  const noMembership = await submit({ revision: 1 });
  assert.equal(noMembership.status(), 404);
  await membership(member.id, true);
  await post(user, route + "/evidence/" + proof.id + "/withdraw", { reason: "本条样例中的验收依据需要重查" }, 204);
  const blocked = await decide(request, { revision: 1, action: "publish" });
  assert.equal(blocked.status(), 409);
  let current = await listed(user);
  assert.equal(current.published, 0);
  assert.equal(current.release.state, "pending");
  await post(user, route + "/instructions", { expected: 1, instructions: "只输出 SRV- 开头的完整编号。" }, 201);
  const stale = await decide(request, { revision: 1, action: "publish" });
  assert.equal(stale.status(), 409);
  current = await listed(user);
  assert.equal(current.revision, 2);
  assert.equal(current.release, undefined);
  await membership(member.id, false);
  return {
    requestBeforeVerification: 409, duplicateRequestsShareID: true,
    adminOutsideTeamDecision: unscoped.status(), requesterOutsideTeamSubmission: noMembership.status(),
    decisionWithWithdrawnEvidence: blocked.status(), decisionForSupersededDraft: stale.status(),
  };
}

import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function checkModelMaintenance({
  admin,
  member,
  panel,
  root,
  model,
  project,
  out,
  writer,
}) {
  const path = "/api/admin/model-resources/" + model.id + "/revisions/1";
  const read = async () => {
    const r = await admin.request.get(path);
    assert.equal(r.status(), 200, await r.text());
    return r.json();
  };
  assert.equal((await member.request.get(path)).status(), 403);
  const privateAgent = await admin.request.post("/api/agents", {
    data: {
      Name: "独立凭据助手",
      Instructions: "按输入核对",
      Resource: model.id,
      ResourceRevision: 1,
      Key: "synthetic-independent",
    },
  });
  assert.equal(privateAgent.status(), 201);
  const privateID = (await privateAgent.json()).id;
  async function trial(id, input, expected) {
    const resp = await admin.request.post("/api/agents/" + id + "/validate", {
      headers: { "Idempotency-Key": crypto.randomUUID() },
      data: { revision: 1, input, expected, authorizeOneCall: true },
    });
    assert.equal(resp.status(), 200, await resp.text());
    return resp.json();
  }
  const before = await read();
  assert.ok(before.agents.some((a) => a.independent));
  assert.ok(before.defaultPlanner);
  assert.ok(!before.projects.some((p) => p.id === project));
  assert.ok(!JSON.stringify(before).includes("synthetic-"));
  await panel.getByText(/接入团队模型 ·/).click();
  await panel.getByText("维护模型与凭据", { exact: true }).click();
  const ui = panel.getByRole("region", { name: "模型修订维护" });
  const reason = "例行轮换 · 已核对授权范围";
  await ui.getByLabel("维护原因").fill(reason);
  async function prepare(key) {
    await ui.getByLabel("新的团队凭据").fill(key);
    await ui.getByRole("checkbox").check();
    await ui.getByRole("button", { name: "准备同范围凭据轮换" }).click();
    await ui.getByText("候选凭据待验证", { exact: true }).waitFor();
  }
  const test = ui.getByRole("button", { name: /授权一次候选凭据验证/ });
  await prepare("synthetic-rejected");
  await test.click();
  await ui
    .getByText(/供应商返回 HTTP 401/)
    .first()
    .waitFor();
  assert.equal((await read()).blocked, false);
  assert.ok(
    await ui.getByRole("button", { name: "启用已验证凭据" }).isDisabled(),
  );
  await ui.getByRole("button", { name: "放弃候选凭据" }).click();
  await ui.getByLabel("新的团队凭据").waitFor();
  assert.equal(
    (await trial(writer, "ACTIVE_KEY_REJECT_TEST", "never")).status,
    "failed",
  );
  assert.equal((await read()).blocked, true);
  assert.equal(
    (await trial(privateID, "CHECK_KEY_BINDING", "independent")).status,
    "passed",
  );
  await prepare("synthetic-rotated");
  await test.click();
  await ui.getByText("候选凭据验证通过", { exact: true }).waitFor();
  const current = await read();
  for (const action of ["disable", "enable"])
    assert.equal(
      (
        await admin.request.post("/api/admin/catalog/models/" + model.id, {
          data: { action, reason },
        })
      ).status(),
      204,
    );
  assert.equal(
    (
      await admin.request.post(path + "/credentials", {
        data: {
          action: "apply",
          candidate: current.candidate.id,
          reason,
          generation: 1,
        },
      })
    ).status(),
    409,
  );
  // Reopen the revision to load its current lifecycle and explicitly authorize a new test.
  await panel.getByText("维护模型与凭据", { exact: true }).click();
  await panel.getByText("维护模型与凭据", { exact: true }).click();
  await ui.getByLabel("维护原因").fill(reason);
  await test.click();
  await ui.getByText("候选凭据验证通过", { exact: true }).waitFor();
  await ui.getByRole("button", { name: "启用已验证凭据" }).click();
  await ui.getByText(/凭据第 2 代/).waitFor();
  assert.equal((await read()).blocked, false);
  assert.equal(
    (await trial(writer, "CHECK_KEY_BINDING", "rotated")).status,
    "passed",
  );
  assert.equal(
    (await trial(privateID, "CHECK_KEY_BINDING", "independent")).status,
    "passed",
  );
  const sealed = await (
    await admin.request.get("/api/projects/" + project)
  ).json();
  assert.equal(sealed.planningModel.revision, 1);
  assert.deepEqual(
    sealed.artifacts.map((a) => a.version),
    [1, 1, 2],
  );
  await ui.getByText("查看依赖此修订的助手、服务与项目").click();
  await ui.screenshot({ path: out + "/model-maintenance.png" });
  await writeFile(
    out + "/model-maintenance.json",
    JSON.stringify(
      {
        provider: "controlled HTTPS",
        candidateRejected: true,
        sharedCredentialRepaired: true,
        independentCredentialPreserved: true,
        priorLifecycleRejected: true,
        revision: 1,
        generation: 2,
        ordinaryMemberExcluded: true,
        sealedVersions: [1, 1, 2],
      },
      null,
      2,
    ),
  );
}

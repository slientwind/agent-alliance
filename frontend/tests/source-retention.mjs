import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";

export async function checkSourceRetention({
  owner,
  reader,
  call,
  page,
  url,
  base,
  detail,
  decision,
  route,
}) {
  const evidence = process.env.VISUAL_EVIDENCE;
  const history = async (path) => call(owner, path + "/history");
  async function act(path, action, options = {}) {
    const h = await history(path);
    return call(
      options.actor || owner,
      path + "/lifecycle",
      "POST",
      {
        action,
        reason: "核对项目资料与分发范围",
        version: h.current,
        expectedProject: h.projectRevision,
        ...options.input,
      },
      options.status || 200,
    );
  }
  const prior = (await detail()).distributionCases;
  assert.ok(prior.length && prior.every((c) => c.state === "reference"));
  await act(url, "recycle", { actor: reader, status: 403 });
  await act(url, "recycle", { input: { version: 43 }, status: 409 });
  await act(url, "recycle");
  assert.ok(
    (await detail()).distributionCases.every((c) => c.state === "pending"),
  );
  await call(owner, route, "POST", decision(prior[0], "reference"), 409);
  await call(reader, base + "/delivery", "GET", undefined, 409);
  const revokedCopy = (await act(url, "restore")).recoveredAs;
  await call(
    reader,
    base + "/sources/" + revokedCopy + "/content",
    "GET",
    undefined,
    404,
  );
  const parent =
    "projects/" +
    (await call(owner, "projects", "POST", { goal: "资料回收与共同原件" }, 201))
      .id;
  const separate =
    "projects/" +
    (await call(owner, "projects", "POST", { goal: "独立保留资料" }, 201)).id;
  const shared = "另一个项目也有权使用的资料-RETENTION-SHARED";
  async function upload(project, name, text) {
    return (
      project +
      "/sources/" +
      (
        await call(
          owner,
          project + "/sources?name=" + name,
          "POST",
          Buffer.from(text),
          201,
        )
      ).id
    );
  }
  const independent = await upload(separate, "schedule.txt", shared);
  const first = await upload(parent, "version-one.txt", shared);
  const body = "可回收原文-RECYCLE-4187";
  await call(
    owner,
    first + "?version=1&name=revision-two.txt",
    "PUT",
    Buffer.from(body),
    201,
  );
  await page.goto(process.env.SMOKE_ORIGIN + "/#" + parent.split("/")[1]);
  await page.reload();
  await page.getByText("项目资料 · 1", { exact: true }).click();
  await page.getByText("资料版本与引用影响", { exact: true }).click();
  await page.getByRole("button", { name: "移入回收区", exact: true }).click();
  const form = page.getByRole("form", { name: "资料生命周期确认" });
  await form.getByLabel("处理原因").fill("停止使用并核对关联版本");
  await form.screenshot({ path: join(evidence, "source-recycle.png") });
  await form.getByRole("button", { name: "确认移入回收区" }).click();
  await page.getByText("回收区与处理记录 · 1", { exact: true }).click();
  await page.getByText("资料版本与引用影响", { exact: true }).click();
  await page
    .getByRole("button", { name: "恢复为新资料", exact: true })
    .waitFor();
  assert.equal(
    (await call(owner, parent + "/search?q=RECYCLE-4187")).length,
    0,
  );
  await call(
    owner,
    first + "/content?version=1&preview=1",
    "GET",
    undefined,
    404,
  );
  await page.getByRole("button", { name: "恢复为新资料", exact: true }).click();
  await form.getByLabel("处理原因").fill("以新资料继续使用");
  await form.getByRole("button", { name: "确认恢复为新资料" }).click();
  await page.getByText("可读取和引用", { exact: true }).waitFor();
  const restored = (await act(first, "restore")).recoveredAs;
  assert.ok(restored && !first.endsWith(restored));
  const restoredURL = parent + "/sources/" + restored;
  assert.equal((await history(restoredURL)).versions.length, 2);
  assert.equal(
    await (
      await owner.request.get("/api/" + restoredURL + "/content?version=1")
    ).text(),
    shared,
  );
  assert.equal(
    await (
      await owner.request.get("/api/" + restoredURL + "/content?version=2")
    ).text(),
    body,
  );
  await call(owner, first + "/content?version=2", "GET", undefined, 404);
  assert.equal(
    (await call(owner, parent + "/search?q=RECYCLE-4187"))[0].id,
    restored,
  );
  await act(restoredURL, "recycle");
  await act(restoredURL, "purge");
  await act(restoredURL, "restore", { status: 409 });
  assert.ok(
    (await history(restoredURL)).versions.every(
      (v) => v.name === null && !v.readable,
    ),
  );
  assert.equal(
    await (await owner.request.get("/api/" + independent + "/content")).text(),
    shared,
  );
  const expiredBody = "单独到期清除原文-TIME-734";
  const expiring = await upload(parent, "time-limit.txt", expiredBody);
  await act(expiring, "recycle");
  await writeFile(
    join(evidence, "source-retention.json"),
    JSON.stringify(
      {
        original: first,
        restored: restoredURL,
        independent,
        expiring,
        uniqueBody: body,
        sharedBody: shared,
        expiredBody,
        oldAccess: "denied",
        restoredVersions: 2,
        previouslyRevoked: "still denied",
        sealedDelivery: "fresh distribution review required",
        expiredSource: expiring.split("/").pop(),
      },
      null,
      2,
    ),
  );
}

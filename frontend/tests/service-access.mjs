import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";
export async function checkServiceAccess({ admin, member, id, name, out }) {
  const panels = [];
  async function open(context) {
    const p = await context.newPage();
    panels.push(p);
    await p.goto("/");
    await p.getByRole("button", { name: "我的工作区" }).click();
    await p.getByRole("button", { name: "服务与渠道" }).click();
    await p.getByRole("button", { name: new RegExp(name + " · v2") }).click();
    await p.getByText("Open API 与身份委托", { exact: true }).click();
    return p;
  }
  try {
    const owner = await open(admin);
    await owner.getByLabel("应用名称").fill("培训自动化");
    await owner.getByRole("button", { name: "签发限定服务凭证" }).click();
    const app = await owner.getByLabel("仅本次显示").inputValue();
    const auth = app.split("\n")[1],
      appID = app.split("\n")[0].split(" ").pop();
    const client = await open(member);
    assert.equal(
      await client.getByRole("button", { name: "签发限定服务凭证" }).count(),
      0,
    );
    await client.getByLabel("受托应用").selectOption(appID);
    await client.getByRole("button", { name: "授予 1 小时服务调用权" }).click();
    const grant = await client.getByLabel("仅本次显示").inputValue();
    const headers = {
      Authorization: auth,
      "X-Alliance-Delegation": grant.split("\n")[1].split(" ")[1],
    };
    assert.equal(
      (
        await member.request.get("/api/v1/services/" + id, { headers })
      ).status(),
      200,
    );
    await client.getByRole("button", { name: "撤销我的委托" }).click();
    await client.getByText(/培训自动化 · 已撤销/).waitFor();
    assert.equal(await client.getByLabel("仅本次显示").count(), 0);
    assert.equal(
      (
        await member.request.get("/api/v1/services/" + id, { headers })
      ).status(),
      403,
    );
    await owner.getByRole("button", { name: "撤销此应用" }).click();
    await owner.getByText(/培训自动化 · 已撤销/).waitFor();
    assert.equal(await owner.getByLabel("仅本次显示").count(), 0);
    assert.equal(
      (
        await admin.request.get("/api/v1/services/" + id, {
          headers: { Authorization: auth },
        })
      ).status(),
      401,
    );
    await owner
      .getByRole("dialog")
      .screenshot({ path: out + "/service-access.png" });
    await writeFile(
      out + "/service-access.json",
      JSON.stringify(
        {
          memberUse: true,
          memberDelegationRevoked: true,
          applicationRevoked: true,
          secretFieldsCleared: true,
        },
        null,
        2,
      ),
    );
  } finally {
    for (const p of panels) await p.close();
  }
}

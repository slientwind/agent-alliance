import assert from "node:assert/strict";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright";

const phase = process.argv[2];
const {
  PUBLIC_BASE_URL: baseURL,
  LOCAL_PASSWORD: password,
  IDP_PASSWORD: idpPassword,
  OIDC_TEST_WORK: work,
  VISUAL_EVIDENCE: evidence,
} = process.env;
const browser = await chromium.launch();
async function page(storageState) {
  const context = await browser.newContext({
    baseURL,
    storageState,
    ignoreHTTPSErrors: true,
  });
  return context.newPage();
}
const button = (p, name) => p.getByRole("button", { name, exact: true });
async function post(p, path, data, status) {
  const r = await p.request.post("/api/" + path, { data });
  assert.equal(r.status(), status, path);
  return r;
}
async function me(p, status = 200) {
  const r = await p.request.get("/api/me");
  assert.equal(r.status(), status);
  if (status === 200) return r.json();
}
async function authorize(p, user, outcome) {
  await p.locator("#username").fill(user);
  await p.locator("#password").fill(idpPassword);
  await p.locator("#kc-login").click();
  await p.getByRole("status").filter({ hasText: outcome }).waitFor();
}
async function login(user, outcome) {
  const p = await page();
  await p.goto("/");
  await button(p, "企业单点登录").click();
  await authorize(p, user, outcome);
  return p;
}
async function invite(owner) {
  return (await (await post(owner, "admin/invitations", {}, 201)).json()).token;
}
async function joinTeam(token, user, email, outcome) {
  const p = await page();
  await p.goto("/");
  await button(p, "我有企业邀请").click();
  for (const [label, value] of [
    ["邀请凭证", token],
    ["姓名", "测试成员"],
    ["邮箱", email],
    ["密码", password],
  ])
    await p.getByLabel(label, { exact: true }).fill(value);
  await button(p, "验证企业身份并加入").click();
  await authorize(p, user, outcome);
  return p;
}
const ok = "企业身份已验证";
const ownerEmail = "owner@sample.invalid";
try {
  if (phase === "optional") {
    const owner = await page();
    await post(
      owner,
      "initialize",
      {
        Enterprise: "登录测试",
        Name: "管理员",
        Email: ownerEmail,
        Password: password,
        Token: process.env.BOOTSTRAP_TOKEN,
      },
      201,
    );
    const id = (await me(owner)).id;
    await writeFile(join(work, "owner"), id);
    await owner
      .context()
      .storageState({ path: join(work, "old-session.json") });
    const guest = await login("owner", "没有可用的成员绑定");
    await me(guest, 401);
    await owner.goto("/");
    await button(owner, "我的工作区").click();
    await owner.getByText("账号设置", { exact: true }).click();
    await owner.getByText("企业登录身份", { exact: true }).click();
    await owner.getByLabel("当前平台账号密码").fill(password);
    await button(owner, "前往验证并绑定").click();
    await authorize(owner, "owner", ok);
    assert.equal((await me(owner)).id, id);
    const project = (await (await post(owner, "projects", { goal: "单点登录返回原待办" }, 201)).json()).id;
    const card = await page();
    await card.goto("/?task=deliver-1#" + project);
    await button(card, "企业单点登录").click();
    await authorize(card, "owner", ok);
    await card.getByRole("alert").filter({ hasText: "这条待办已更新" }).waitFor();
    assert.equal(new URL(card.url()).hash, "#" + project);
    assert.equal(new URL(card.url()).searchParams.has("task"), false);
    await card.close();
    const token = await invite(owner);
    const email = "local-only@sample.invalid";
    await joinTeam(token, "owner", email, "已有关联");
    const other = await joinTeam(token, "other", email, ok);
    assert.equal((await me(other)).role, "member");
    const copy = await page(await other.context().storageState());
    await button(other, "关闭登录提示").click();
    assert.equal(await other.locator(".login-notice").count(), 0);
    await button(other, "我的工作区").click();
    await other.getByText("账号设置", { exact: true }).click();
    await other.getByText("企业登录身份", { exact: true }).click();
    await other.getByLabel("当前平台账号密码").fill(password);
    await other.getByLabel("解除原因").fill("重新配置身份");
    await button(other, "解除企业身份绑定").click();
    await button(other, "企业单点登录").waitFor();
    await me(copy, 401);
    const fresh = await page();
    const start = await (await post(fresh, "oidc/login", {}, 200)).json();
    const auth = new URL(start.url);
    assert.equal(auth.searchParams.get("code_challenge_method"), "S256");
    const state = auth.searchParams.get("state");
    assert.ok(state && auth.searchParams.get("nonce"));
    const callback =
      "/api/oidc/callback?state=" +
      encodeURIComponent(state) +
      "&error=access_denied";
    for (const [client, code] of [
      [copy, "expired"],
      [fresh, "denied"],
      [fresh, "expired"],
    ]) {
      const resp = await client.request.get(callback, { maxRedirects: 0 });
      assert.equal(resp.headers().location, "/?sso=" + code);
    }
  } else if (phase === "required") {
    const old = await page(join(work, "old-session.json"));
    await me(old, 401);
    await post(old, "login", { Email: ownerEmail, Password: password }, 403);
    await post(old, "register", {}, 403);
    const fresh = await page();
    await fresh.goto("/");
    await button(fresh, "指定管理员应急登录").waitFor();
    await fresh.screenshot({ path: join(evidence, "required-login.png") });
    const owner = await login("owner", ok);
    assert.equal(
      (await me(owner)).id,
      await readFile(join(work, "owner"), "utf8"),
    );
    await post(
      owner,
      "me/oidc/unlink",
      { Password: password, Reason: "测试" },
      409,
    );
    const token = await invite(owner);
    const email = "invited@sample.invalid";
    const staff = await joinTeam(token, "staff", email, ok);
    const member = await me(staff);
    assert.equal(member.role, "member");
    await post(
      old,
      "oidc/enroll",
      { Token: token, Name: "重复", Email: email, Password: password },
      403,
    );
    assert.equal((await staff.request.get("/api/admin/members")).status(), 403);
    const disabled = await owner.request.patch(
      "/api/admin/members/" + member.id,
      {
        data: {
          Role: "member",
          Enabled: false,
          Reason: "停用测试",
          Password: password,
        },
      },
    );
    assert.equal(disabled.status(), 204);
    await me(staff, 401);
    await login("staff", "没有可用的成员绑定");
  } else if (phase === "outage") {
    const p = await page();
    await p.goto("/");
    await post(
      p,
      "login",
      {
        Email: "local-only@sample.invalid",
        Password: password,
        Reason: "演练",
      },
      403,
    );
    await button(p, "指定管理员应急登录").click();
    await p.getByLabel("邮箱").fill(ownerEmail);
    await p.getByLabel("密码", { exact: true }).fill(password);
    await p.getByLabel("应急登录原因").fill("身份服务中断演练");
    await button(p, "应急登录").click();
    await button(p, "我的工作区").waitFor();
    assert.equal((await me(p)).role, "admin");
  } else throw new Error("Unknown phase");
  console.log("OIDC " + phase + " passed");
} finally {
  await browser.close();
}

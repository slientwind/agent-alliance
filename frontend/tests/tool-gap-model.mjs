import { checkPlanningOutput } from "./planning-output.mjs";
import assert from "node:assert/strict";
import { chromium } from "playwright";
import { checkToolGap } from "./tool-gap.mjs";

const {
  SMOKE_ORIGIN: baseURL,
  BROWSER_PASSWORD: password,
  VISUAL_EVIDENCE: out,
  QUICK_ENDPOINT,
} = process.env;
const browser = await chromium.launch();
try {
  const admin = await browser.newPage({ baseURL });
  const member = await browser.newPage({
    baseURL,
    viewport: { width: 390, height: 844 },
  });
  const post = async (page, route, data) => {
    const reply = await page.request.post("/api/" + route, { data });
    assert.ok(reply.ok(), await reply.text());
    return reply.headers()["content-type"]?.includes("json")
      ? reply.json()
      : null;
  };
  await post(admin, "login", { email: "review@sample.invalid", password });
  const team = await post(admin, "admin/teams", { name: "合成议程验证" });
  const invite = await post(admin, "admin/invitations", {});
  const author = await post(member, "register", {
    email: "gap-author@sample.invalid",
    name: "会议经办",
    password,
    token: invite.token,
  });
  const teams = await (await member.request.get("/api/teams")).json();
  for (const [id, joined] of [
    [team.id, true],
    [teams.find((t) => t.default).id, false],
  ]) {
    const res = await admin.request.put(
      "/api/admin/teams/" + id + "/members/" + author.id,
      { data: { joined, reason: "隔离检验空能力库" } },
    );
    assert.equal(res.status(), 204);
  }
  await member.goto("/");
  assert.deepEqual(await (await member.request.get("/api/agents")).json(), []);
  const context = await checkToolGap(member, admin, QUICK_ENDPOINT, out, team.id);
  await checkPlanningOutput(member, admin, out, context, true);
} finally {
  await browser.close();
}

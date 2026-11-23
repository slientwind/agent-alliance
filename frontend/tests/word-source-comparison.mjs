import assert from "node:assert/strict";
import { writeFile } from "node:fs/promises";

export async function verifyWordSourceComparison(
  admin,
  team,
  agent,
  published,
  donor,
  out,
) {
  const http = admin.request;
  async function read(path) {
    const reply = await http.get("/api/" + path);
    assert.equal(reply.status(), 200, await reply.text());
    return reply.json();
  }
  async function post(path, data, key) {
    const reply = await http.post("/api/" + path, {
      data,
      headers: key ? { "Idempotency-Key": key } : {},
    });
    assert.ok(reply.ok(), await reply.text());
    if (reply.headers()["content-type"]?.includes("json")) return reply.json();
  }
  const original = "换班前，值班人员应记录设备状态。";
  const p = await post("projects", {
    goal: "将交班要求整理为 Word 说明",
    team,
  });
  const base = "projects/" + p.id;
  const source = await http.post(
    "/api/" + base + "/sources?name=handover.txt",
    {
      data: Buffer.from(original),
      headers: { "content-type": "application/octet-stream" },
    },
  );
  assert.equal(source.status(), 201);
  const pin = await source.json();
  await post(base + "/plan", {
    expected: (await read(base)).revision,
    brief: "形成交班说明",
    agents: [agent],
    agentRevisions: [published],
    sources: [{ id: pin.id, version: pin.version }],
  });
  await post(
    base + "/runs",
    { expected: (await read(base)).revision, authorizeCalls: 1 },
    "word-compare-execution",
  );
  let a;
  for (let i = 0; i < 100; i++) {
    a = (await read(base)).artifacts[0];
    if (a?.file) break;
    await new Promise((r) => setTimeout(r, 220));
  }
  assert.equal(a.verifiedWord, true);
  const file = base + "/artifacts/" + a.id;
  const route = file + "/source-comparison/" + pin.id;
  const first = await read(route + "?version=1");
  assert.equal(first.rows[0].text, original);
  assert.deepEqual(first.rows[0].matches, [1]);
  const donorFile = await http.get(donor);
  assert.equal(donorFile.status(), 200);
  const uploaded = await http.post("/api/" + file + "/file-revisions", {
    multipart: {
      version: "1",
      reason: "外部排版修订",
      summary: a.body,
      file: {
        name: "交班说明.docx",
        mimeType:
          "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        buffer: await donorFile.body(),
      },
    },
  });
  assert.equal(uploaded.status(), 201, await uploaded.text());
  const updated = (await read(base)).artifacts[0];
  assert.equal(updated.version, 2);
  assert.equal(updated.verifiedWord, false);
  assert.equal(updated.body, a.body);
  const denied = await http.get("/api/" + route + "?version=2");
  assert.equal(denied.status(), 404);
  await writeFile(
    out + "/word-source-comparison.json",
    JSON.stringify(
      {
        first,
        externalWordWithSameSummary: {
          version: updated.version,
          http: denied.status(),
          verifiedWord: updated.verifiedWord,
        },
      },
      null,
      2,
    ),
  );
}

import assert from "node:assert/strict";
import { readFile, stat } from "node:fs/promises";
import { chromium } from "playwright";

const dist = new URL("../dist/", import.meta.url);
const manifest = JSON.parse(await readFile(new URL(".vite/manifest.json", dist), "utf8"));
const entry = manifest["index.html"];
const features = ["admin-monitor", "studio-workspace", "pdf-runtime"].map(name => manifest[`src/${name}.${name === "pdf-runtime" ? "ts" : "tsx"}`]);
assert.ok((await stat(new URL(entry.file, dist))).size < 500_000, "the first script should stay under 500 kB");
const startup = new Set();
function visit(key) {
  if (startup.has(manifest[key].file)) return;
  startup.add(manifest[key].file);
  for (const dependency of manifest[key].imports || []) visit(dependency);
}
visit("index.html");
for (const feature of features) {
  assert.ok(feature.isDynamicEntry, feature.file);
  assert.equal(startup.has(feature.file), false, feature.file);
}

function reportPDF() {
  const contents = "BT /F1 12 Tf 16 120 Td (Bundle fixture) Tj ET";
  const objects = [
    "<< /Type /Catalog /Pages 2 0 R >>",
    "<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
    "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 240 160] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
    `<< /Length ${contents.length} >>\nstream\n${contents}\nendstream`,
    "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
  ];
  let pdf = "%PDF-1.4\n";
  const offsets = objects.map((body, i) => {
    const offset = pdf.length;
    pdf += `${i + 1} 0 obj\n${body}\nendobj\n`;
    return offset;
  });
  const xref = pdf.length;
  pdf += "xref\n0 6\n0000000000 65535 f \n";
  pdf += offsets.map(offset => `${String(offset).padStart(10, "0")} 00000 n \n`).join("");
  return Buffer.from(pdf + `trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`);
}

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ baseURL: process.env.SMOKE_ORIGIN || "http://127.0.0.1:15181" });
  page.setDefaultTimeout(10000);
  const requests = new Set();
  page.on("request", request => requests.add(new URL(request.url()).pathname.slice(1)));
  const id = "2b40fe15-8725-45cf-9576-5661de125cb5";
  const artifact = { id: "report", name: "库存报表", ordinal: 1, version: 1, body: "近期库存", file: "stock.pdf",
    approved: false, reviewNotesReadable: true, feedback: [], sourceChanges: [] };
  const project = { id, title: "库存核对", stage: "brief", revision: 1, owner: "member", reviewer: "member", team: "team", planVersion: 0,
    messages: [], artifacts: [artifact], participants: [], handoffs: [], sharedAgents: [], deliveries: [], distributionCases: [],
    budget: { warning: false, pending: 0 }, costSummary: {}, run: null, suggestion: null, plan: null };
  const base = `/api/projects/${id}`;
  await page.route("**/api/**", route => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/session-events") || path.endsWith("/events")) return route.fulfill({ contentType: "text/event-stream", body: ": ready\n\n" });
    if (path === base + "/artifacts/report/file") return route.fulfill({ contentType: "application/pdf", body: reportPDF() });
    const data = {
      "/api/instance": { initialized: true, name: "测试企业" },
      "/api/me": { id: "member", name: "测试用户", role: "admin", enabled: true },
      "/api/teams": [{ id: "team", name: "团队", joined: true, default: true }],
      "/api/projects": [project], [base]: project,
      [base + "/artifacts/report/versions"]: [artifact], [base + "/artifacts/report/comments"]: { comments: [] },
      "/api/admin/monitor/overview": { overview: {
        kpi: { calls: 0, successfulCalls: 0, tokens: 0, cachedTokens: 0, costMicroCny: "0", undisclosed: 0 },
        quality: { lightweight: { met: 0, total: 0 }, collaborative: { met: 0, total: 0 } },
        funnel: { submitted: 0, completed: 0, outputPassed: 0, accepted: 0 }, taskTrend: [], usageTrend: [],
      } },
      "/api/admin/monitor/agents": { rows: [] }, "/api/admin/monitor/queue": { counts: {}, rows: [] },
      "/api/admin/monitor/alerts": { rules: [], deliveries: [] },
    };
    return route.fulfill({ json: data[path] ?? [] });
  });

  await page.goto(`/chat/${id}`);
  await page.getByRole("button", { name: "打开成果 库存报表" }).waitFor();
  for (const feature of features) assert.equal(requests.has(feature.file), false);
  assert.equal([...requests].some(path => path.includes("pdf.worker")), false);
  await page.getByRole("button", { name: "打开成果 库存报表" }).click();
  await page.locator('canvas[data-rendered="true"]').waitFor();
  assert.ok(requests.has(features[2].file));
  assert.ok([...requests].some(path => path.includes("pdf.worker")));
  assert.equal(requests.has(features[0].file), false);
  assert.equal(requests.has(features[1].file), false);

  await page.goto("/admin/monitor");
  await page.getByRole("region", { name: "任务质量漏斗" }).waitFor();
  assert.ok(requests.has(features[0].file));
  assert.equal(requests.has(features[1].file), false);

  await page.goto("/agents/studio");
  await page.getByRole("button", { name: "创建助手", exact: true }).waitFor();
  assert.ok(requests.has(features[1].file));
} finally {
  await browser.close();
}

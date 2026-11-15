import { defineConfig } from "@playwright/test";
export default defineConfig({
  testDir: "./tests",
  timeout: 45000,
  expect: { timeout: 10000 },
  workers: 1,
  fullyParallel: false,
  reporter: [
    ["list"],
    ["html", { outputFolder: "../artifacts/browser-report", open: "never" }],
    ["json", { outputFile: "../artifacts/browser-results.json" }],
  ],
  outputDir: "../artifacts/test-results",
  use: {
    baseURL: process.env.ALLIANCE_BROWSER_URL || "http://127.0.0.1:5180",
    viewport: { width: 1440, height: 1040 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    headless: true,
  },
});

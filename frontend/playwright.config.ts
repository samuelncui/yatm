import { defineConfig, devices } from "@playwright/test";

const baseURL = process.env.YATM_BROWSER_URL;
if (!baseURL || !["127.0.0.1", "localhost", "[::1]"].includes(new URL(baseURL).hostname))
  throw new Error("Set YATM_BROWSER_URL to an isolated Demo server on loopback (or an SSH tunnel).");
if (!process.env.YATM_BROWSER_COMMIT) throw new Error("Set YATM_BROWSER_COMMIT to the source identity reported by the server.");

export default defineConfig({
  testDir: "./e2e",
  outputDir: "../output/browser-results",
  workers: 1,
  retries: 0,
  forbidOnly: Boolean(process.env.CI),
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    viewport: { width: 1440, height: 1000 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
});

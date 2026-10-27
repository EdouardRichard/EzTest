import { defineConfig, devices } from "@playwright/test"

/**
 * The installed browser channel. The E2E script defaults to Chrome and falls
 * back to Edge on hosts without Chrome; Playwright never downloads Chromium.
 */
const channel = process.env.EZTEST_E2E_CHANNEL === "msedge" ? "msedge" : "chrome"

/**
 * Playwright E2E configuration. The services are started by
 * `scripts/e2e.ps1`, not by a webServer block, because the API (13333) and the
 * Next.js web app (6868) have separate lifecycles and the API needs the
 * dedicated `eztest_e2e` database.
 *
 * Artifacts go to the gitignored `.tmp` directory so a run never leaves
 * uncommitted build output behind.
 */
export default defineConfig({
  testDir: "./e2e",
  outputDir: "../.tmp/e2e-artifacts/playwright",
  fullyParallel: false,
  // The scenarios share one API/database and the admin session; one worker
  // keeps sequence numbers and the user list deterministic.
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  use: {
    baseURL: process.env.EZTEST_E2E_BASE_URL?.trim() || "http://localhost:6868",
    channel,
    headless: true,
    actionTimeout: 20_000,
    navigationTimeout: 60_000,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
  },
  projects: [
    {
      name: channel,
      use: { ...devices["Desktop Chrome"], channel },
    },
  ],
})

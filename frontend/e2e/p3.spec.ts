import fs from "node:fs"
import path from "node:path"

import { expect, test } from "@playwright/test"

/**
 * P3 execution workspace E2E (real LLM; spec §13.2 `scripts/p3-e2e.ps1`).
 *
 * The acceptance script prepares the version through the real API/LLM and
 * exports its id in `EZTEST_E2E_P3_VERSION_ID`. Until then this spec stays
 * skipped so `pnpm e2e` on a P2 environment does not need P3 backend routes.
 * Task 14 extends the evidence/rerun/human-takeover assertions once the
 * two-SUT fixture and the timed-window case are stable.
 */
const VERSION_ID = process.env.EZTEST_E2E_P3_VERSION_ID?.trim() ?? ""
const CASE_TIMEOUT_MS = Number(process.env.EZTEST_E2E_CASE_TIMEOUT_MS || "780000")
const TEST_TIMEOUT_MS = Number(process.env.EZTEST_E2E_P3_TIMEOUT_MS || "900000")

function defineExecutionSuite(): void {
  test("confirmed cases auto-enqueue, execute, expose evidence and pause/resume", async ({
    page,
  }) => {
    test.setTimeout(TEST_TIMEOUT_MS)
    const pageErrors: string[] = []
    page.on("pageerror", (error) => pageErrors.push(String(error)))

    await page.goto("/login")
    await page.fill("#username", process.env.EZTEST_E2E_USERNAME?.trim() || "admin")
    await page.fill("#password", process.env.EZTEST_E2E_PASSWORD?.trim() || "admin123")
    await page.getByRole("button", { name: "登录" }).click()
    await page.waitForURL("**/versions")

    await page.goto(`/versions/${VERSION_ID}?tab=execution`)
    await expect(page.getByTestId("execution-tab")).toBeVisible({ timeout: 30_000 })
    await expect(page.getByTestId("execution-toolbar")).toBeVisible()
    await expect(page.getByTestId("wave-list")).toBeVisible()
    await expect(page.getByTestId("live-trace")).toBeVisible()
    await expect(page.getByTestId("results-panel")).toBeVisible()

    // Execution is intent-driven: no "start execution" action may exist.
    await expect(page.getByRole("button", { name: /开始执行/ })).toHaveCount(0)

    // The confirmed case generates an intent and a wave without user action.
    const caseRunRows = page.locator('[data-testid^="case-run-row-"]')
    await expect(caseRunRows.first()).toBeVisible({ timeout: CASE_TIMEOUT_MS })

    await caseRunRows.first().getByRole("button").first().click()
    await expect(page.getByTestId("case-run-detail")).toBeVisible()

    // Wait for one of the four terminal verdicts (real LLM drives the run).
    const terminal = caseRunRows.filter({ hasText: /通过|失败|阻塞|未定/ })
    await expect(terminal.first()).toBeVisible({ timeout: CASE_TIMEOUT_MS })

    // Evidence viewer: screenshots render through a revoked object URL.
    const evidenceTrigger = page
      .getByTestId("case-run-detail")
      .locator("button", { hasText: /截图|文本|HTTP|数据库/ })
    if ((await evidenceTrigger.count()) > 0) {
      await evidenceTrigger.first().click()
      await expect(page.getByRole("dialog")).toBeVisible()
      await expect(page.getByText("不可变证据")).toBeVisible()
      await page.getByRole("button", { name: "关闭" }).click()
    }

    // Pause/resume: best-effort because the wave may already be done.
    const pauseButton = page.getByTestId("execution-toolbar").getByRole("button", { name: "暂停" })
    if ((await pauseButton.count()) > 0) {
      await pauseButton.click()
      await expect(page.getByRole("dialog", { name: "暂停执行" })).toBeVisible()
      await page.getByRole("dialog").getByRole("button", { name: "暂停" }).click()
      await expect(page.getByText("执行已暂停")).toBeVisible()
      await page.getByRole("button", { name: "恢复" }).first().click()
      await expect(page.getByText("执行已暂停")).toHaveCount(0)
    }

    // Retest is asserted by Task 14 once the fixture guarantees a failed case:
    // the failed/blocked row opens the inline confirmation and creates a
    // `purpose=retest` intent, which starts a new wave.

    const screenshotDir =
      process.env.EZTEST_E2E_SCREENSHOT_DIR?.trim() ||
      path.resolve(process.cwd(), "..", ".tmp", "p3-acceptance", "screenshots")
    fs.mkdirSync(screenshotDir, { recursive: true })
    await page.screenshot({
      path: path.join(screenshotDir, "execution-workspace.png"),
      fullPage: true,
    })

    expect(pageErrors).toEqual([])
  })
}

if (VERSION_ID.length === 0) {
  test.describe
    .skip("P3 execution workspace (real LLM)", () => {
      defineExecutionSuite()
    })
} else {
  test.describe("P3 execution workspace (real LLM)", () => {
    defineExecutionSuite()
  })
}

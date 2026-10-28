import { expect, test } from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME, login } from "./p3-helpers"
import { createBatch, listCaseRuns, setP6Mode } from "./p6-helpers"
import {
  captureConsoleErrors,
  expectNoConsoleErrors,
  findP7Batch,
  listP7Analyses,
  p7ScreenshotPath,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7BatchTerminal,
  waitP7CaseRun,
  waitP7ReportForBatch,
  waitVersionCompleted,
} from "./p7-helpers"

/**
 * P7 degraded E2E (design spec §12.4/§6.7): the P7 agents (failure analysis,
 * triage, supplement) are degraded at the fake-LLM level while the batch still
 * executes and completes. The failure analysis lands `degraded`, no issue is
 * fabricated, no supplement batch is created, the report is marked degraded and
 * the browser keeps rendering the report/issues tabs without crashing.
 */

const SPEC_TIMEOUT_MS = 600_000

test.describe.configure({ mode: "serial" })

test("P7 降级：P7 模式 degraded → 批次完成 → 报告 degraded → 页面不崩", async ({
  page,
  request,
}) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) {
    return
  }

  const monitor = captureConsoleErrors(page)
  try {
    await setP6Mode(request, "fail")
    await setP7Mode(request, "degraded")

    const seed = await test.step("a failing batch still executes and completes", async () => {
      const seeded = await seedP7ExecutionVersion(request, { caseTitle: "P7 降级用例" })
      const batch = await createBatch(request, seeded.token, seeded.version.id)
      const done = await waitP7BatchTerminal(
        request,
        seeded.token,
        seeded.version.id,
        batch.id,
        300_000,
      )
      expect(done.status).toBe("done")

      const runs = await listCaseRuns(request, seeded.token, seeded.version.id, done.id)
      expect(runs).toHaveLength(1)
      const caseRun = await waitP7CaseRun(
        request,
        seeded.token,
        seeded.version.id,
        done.id,
        runs[0].id,
      )
      expect(caseRun.status).toBe("failed")
      expect(caseRun.failure_class).toBe("application")

      await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
      return { seeded, batch: done }
    })

    const token = seed.seeded.token
    const versionId = seed.seeded.version.id

    await test.step("the degraded autonomy chain settles without fabricating data", async () => {
      const completed = await waitVersionCompleted(request, token, versionId)
      expect(completed.status).toBe("completed")

      const analyses = await listP7Analyses(request, token, versionId, seed.batch.id)
      const failure = analyses.find((run) => run.kind === "failure")
      expect(failure?.status).toBe("degraded")
      expect(analyses.some((run) => run.kind === "issue_triage")).toBe(false)

      expect(await findP7Batch(request, token, versionId, "supplement")).toBeNull()

      const report = await waitP7ReportForBatch(request, token, versionId, seed.batch.id)
      expect(report.content.degraded).toBe(true)
      expect(report.content.issues).toHaveLength(0)
      expect(report.status).toBe("published")
    })

    await test.step("the browser renders the degraded report and empty issue tab", async () => {
      const report = await waitP7ReportForBatch(request, token, versionId, seed.batch.id)
      await page.goto(
        `/versions/${encodeURIComponent(versionId)}?tab=report&report=${encodeURIComponent(report.id)}`,
      )
      await expect(page.getByTestId("report-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-degraded")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-tab")).toContainText("AI 降级")
      await expect(page.getByTestId("report-error")).toHaveCount(0)
      await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 1")
      await expect(page.getByTestId("report-summary-case-count")).toHaveText("1")
      await expect(page.getByTestId("report-summary-failed")).toHaveText("1")
      await page.screenshot({ path: p7ScreenshotPath("p7-degraded-01-report.png"), fullPage: true })

      await page.getByRole("tab", { name: "问题" }).click()
      await expect(page.getByTestId("issues-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("issues-empty")).toBeVisible()
      await expect(page.getByTestId("issues-degraded")).toBeVisible()
      await expect(page.getByTestId("issues-degraded")).toContainText("AI 分析不可用")
      await expect(page.getByTestId("issues-error")).toHaveCount(0)

      // The page shell/report stay usable after the degraded chain.
      await page.goto(`/versions/${encodeURIComponent(versionId)}`)
      await expect(page.getByRole("heading", { name: seed.seeded.version.name })).toBeVisible({
        timeout: 60_000,
      })
      await expect(page.getByTestId("exec-report-entry")).toBeVisible()
      await page.screenshot({
        path: p7ScreenshotPath("p7-degraded-02-version.png"),
        fullPage: true,
      })
    })

    await expectNoConsoleErrors(monitor, page, "P7 降级")
  } finally {
    monitor.stop()
    await setP6Mode(request, "normal").catch(() => undefined)
    await setP7Mode(request, "normal").catch(() => undefined)
  }
})

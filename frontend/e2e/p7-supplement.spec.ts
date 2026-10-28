import { expect, test } from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME, login } from "./p3-helpers"
import { createBatch, setP6Mode } from "./p6-helpers"
import {
  captureConsoleErrors,
  expectNoConsoleErrors,
  listP7Batches,
  listP7CaseRuns,
  p7ScreenshotPath,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7BatchKind,
  waitP7BatchTerminal,
  waitP7ReportForBatch,
  waitVersionCompleted,
} from "./p7-helpers"

/**
 * P7 supplement E2E (design spec §12.4/§6.4): a passed baseline batch triggers
 * the fake post-batch supplement agent, the autonomous worker creates and
 * executes a child supplement batch automatically, and the reports expose the
 * `ai_supplement` case source badge and the batch-to-batch diff.
 *
 * The test drives the real API, the real Playwright executor and the fake LLM;
 * nothing is mocked.
 */

const SPEC_TIMEOUT_MS = 600_000

test.describe.configure({ mode: "serial" })

test("P7 补全：基线批 → fake 补全 → 自动 supplement 批 → 报告来源徽章/批次差异", async ({
  page,
  request,
}) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) {
    return
  }

  const monitor = captureConsoleErrors(page)
  try {
    await setP6Mode(request, "normal")
    await setP7Mode(request, "normal")

    const seed = await test.step("seed and execute the baseline batch", async () => {
      const seeded = await seedP7ExecutionVersion(request, { caseTitle: "P7 补全基线用例" })
      expect(seeded.version.status).toBe("executing")
      const batch = await createBatch(request, seeded.token, seeded.version.id)
      expect(batch.batch_no).toBe(1)
      const done = await waitP7BatchTerminal(
        request,
        seeded.token,
        seeded.version.id,
        batch.id,
        300_000,
      )
      expect(done.status).toBe("done")
      expect(done.counts.passed).toBe(1)

      await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
      return { seeded, baseline: done }
    })

    const token = seed.seeded.token
    const versionId = seed.seeded.version.id

    const supplement =
      await test.step("the autonomy chain creates and runs a supplement child batch", async () => {
        const child = await waitP7BatchKind(request, token, versionId, "supplement", {
          parentBatchId: seed.baseline.id,
          timeoutMs: 300_000,
        })
        expect(child.parentBatchId).toBe(seed.baseline.id)
        expect(child.batch_no).toBe(2)
        const done = await waitP7BatchTerminal(request, token, versionId, child.id, 300_000)
        expect(done.status).toBe("done")
        expect(done.counts.total).toBe(2)
        expect(done.counts.passed).toBe(2)

        const runs = await listP7CaseRuns(request, token, versionId, done.id)
        expect(runs).toHaveLength(2)
        expect(runs.every((run) => run.source === "ai_supplement")).toBe(true)
        expect(runs.every((run) => run.status === "passed")).toBe(true)

        // The baseline batch stays untouched by the supplement cycle.
        const batches = await listP7Batches(request, token, versionId)
        const baseline = batches.find((batch) => batch.id === seed.baseline.id)
        expect(baseline?.status).toBe("done")
        expect(baseline?.counts.passed).toBe(1)

        await waitVersionCompleted(request, token, versionId)
        return done
      })

    await test.step("reports: supplement source badges and batch diff", async () => {
      const baselineReport = await waitP7ReportForBatch(request, token, versionId, seed.baseline.id)
      expect(baselineReport.batchKind).toBe("baseline")
      expect(baselineReport.content.cases).toHaveLength(1)
      expect(baselineReport.content.cases[0]?.source).toBe("baseline")

      const supplementReport = await waitP7ReportForBatch(request, token, versionId, supplement.id)
      expect(supplementReport.batchKind).toBe("supplement")
      expect(supplementReport.batchNo).toBe(2)
      expect(supplementReport.content.cases).toHaveLength(2)
      expect(supplementReport.content.cases.every((row) => row.source === "ai_supplement")).toBe(
        true,
      )

      await page.goto(
        `/versions/${encodeURIComponent(versionId)}?tab=report&report=${encodeURIComponent(supplementReport.id)}`,
      )
      await expect(page.getByTestId("report-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 1")
      await expect(page.getByTestId("report-tab")).toContainText("Batch #2")
      await expect(page.getByTestId("report-tab")).toContainText("补全")

      const selector = page.getByTestId("report-batch-selector")
      await expect(selector).toHaveValue(supplement.id)
      await expect(selector.locator("option", { hasText: "补全" })).toHaveCount(1)

      await expect(page.getByTestId("report-case-source")).toHaveCount(2)
      await expect(
        page.locator('[data-testid="report-case-source"][data-source="ai_supplement"]'),
      ).toHaveCount(2)
      await expect(page.getByTestId("report-diff")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-diff-new-cases")).toHaveText("2", {
        timeout: 60_000,
      })
      await page.screenshot({
        path: p7ScreenshotPath("p7-supplement-01-report.png"),
        fullPage: true,
      })

      // Switch to the baseline batch: baseline source badge, no previous report.
      await selector.selectOption(seed.baseline.id)
      await expect(page.getByTestId("report-case-source")).toHaveCount(1)
      await expect(
        page.locator('[data-testid="report-case-source"][data-source="baseline"]'),
      ).toHaveCount(1)
      await expect(page.getByTestId("report-diff-unavailable")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-tab")).toContainText("Batch #1")
      await page.screenshot({
        path: p7ScreenshotPath("p7-supplement-02-baseline-report.png"),
        fullPage: true,
      })
    })

    await expectNoConsoleErrors(monitor, page, "P7 补全")
  } finally {
    monitor.stop()
    await setP6Mode(request, "normal").catch(() => undefined)
    await setP7Mode(request, "normal").catch(() => undefined)
  }
})

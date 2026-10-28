import { readFileSync } from "node:fs"

import { expect, test } from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME, login } from "./p3-helpers"
import { setP6Mode } from "./p6-helpers"
import {
  captureConsoleErrors,
  configureP7DbTarget,
  expectNoConsoleErrors,
  expectNoSecretInPage,
  listP7CaseRuns,
  listP7SupplementNodes,
  openP7VersionPage,
  p7ScreenshotPath,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7BatchKind,
  waitP7BatchTerminal,
  waitP7CaseRun,
  waitP7Events,
  waitP7IncrementalPlan,
  waitP7ReportForBatch,
  waitVersionCompleted,
  waitVersionStatus,
} from "./p7-helpers"

/**
 * P7 continuing E2E (design spec §12.4/§6.1): a completed version gains a new
 * requirement (completed → continuing/clarifying), the requirement is confirmed
 * (clarifying → designing), the fake supplement agent produces an incremental
 * plan, the batch is created from the plan and executed for real (including one
 * server-side db assertion), the autonomous chain publishes the Batch report and
 * finalizes the version back to completed.
 *
 * The test drives the real API, the real Playwright executor, the fake LLM and
 * the real local PostgreSQL DB target; nothing is mocked.
 */

const SPEC_TIMEOUT_MS = 900_000

test.describe.configure({ mode: "serial" })

test("P7 继续中：完成版本 → 新增需求 → 增量计划/批次 → db 断言 → 报告 → 完成", async ({
  page,
  request,
}) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true, requireDbTarget: true })) {
    return
  }

  const monitor = captureConsoleErrors(page)
  try {
    await setP6Mode(request, "normal")
    await setP7Mode(request, "normal")

    const seed =
      await test.step("seed executing version with db assertion + DB target", async () => {
        const seeded = await seedP7ExecutionVersion(request, {
          caseTitle: "P7 继续中下单用例",
          withDbAssertion: true,
        })
        expect(seeded.version.status).toBe("executing")
        expect(seeded.dbAssertionId).toMatch(/^\d+$/)
        expect(seeded.sessionId, "the fixture session must be imported").toBeGreaterThan(0)

        const target = await configureP7DbTarget(request, seeded.token, seeded.environmentId)
        expect(target.credentialSet).toBe(true)
        expect(target.enabled).toBe(true)

        await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
        await openP7VersionPage(page, seeded.version)
        await expect(page.getByTestId("exec-start-btn")).toBeVisible()
        return seeded
      })

    const token = seed.token
    const versionId = seed.version.id

    const baseline =
      await test.step("baseline batch executes for real (ui + db assertions)", async () => {
        // The post-batch supplement run (started by the autonomous chain right
        // after the report) is set to noop: the two static fake proposals are
        // reserved for the incremental_design run so the plan is deterministic.
        // P7 agents are never part of the execution-agent turn.
        await setP7Mode(request, "noop")
        await page.getByTestId("exec-start-btn").click()
        const created = await waitP7BatchKind(request, token, versionId, "baseline", {
          timeoutMs: 120_000,
        })
        expect(created.batch_no).toBe(1)
        const done = await waitP7BatchTerminal(request, token, versionId, created.id, 300_000)
        expect(done.status).toBe("done")
        expect(done.counts.passed).toBe(1)
        expect(done.counts.failed).toBe(0)

        const runs = await listP7CaseRuns(request, token, versionId, done.id)
        expect(runs).toHaveLength(1)
        const caseRun = await waitP7CaseRun(request, token, versionId, done.id, runs[0].id)
        expect(caseRun.status).toBe("passed")
        expect(caseRun.source).toBe("baseline")

        const dbAssertion = caseRun.assertions.find((assertion) => assertion.kind === "db")
        expect(dbAssertion, "the db assertion must be recorded").toBeTruthy()
        expect(dbAssertion?.verdict).toBe("passed")
        expect(
          caseRun.evidence.some((evidence) => evidence.kind === "db_result"),
          "the db assertion must store db_result evidence",
        ).toBe(true)
        return { batch: done, caseRunId: caseRun.id }
      })

    await test.step("autonomy chain: baseline report + finalize (noop supplement)", async () => {
      await waitP7ReportForBatch(request, token, versionId, baseline.batch.id)
      const completed = await waitVersionCompleted(request, token, versionId)
      expect(completed.batch_no).toBe(1)

      await waitP7Events(request, token, versionId, [
        { type: "report.published" },
        { type: "version.status_changed" },
      ])
      await expect(page.getByTestId("add-requirement-btn")).toBeVisible({ timeout: 120_000 })
      await expect(page.getByTestId("continuing-badge")).toHaveCount(0)
      await page.screenshot({
        path: p7ScreenshotPath("p7-continuing-01-completed.png"),
        fullPage: true,
      })
      // From here the incremental design must propose for real again.
      await setP7Mode(request, "normal")
    })

    await test.step("new requirement: completed → continuing/clarifying → designing", async () => {
      await page.getByTestId("add-requirement-btn").click()
      await expect(
        page.getByText("当前版本已完成：创建需求后将自动进入继续中，并开始增量澄清。"),
      ).toBeVisible()

      const title = `P7 继续中新增需求 ${seed.suffix}`
      await page.getByLabel("标题", { exact: true }).fill(title)
      await page.getByLabel("描述", { exact: true }).fill("继续中场景：新增发票开具需求。")
      await page.getByRole("button", { name: "创建需求", exact: true }).click()

      const badge = page.getByTestId("continuing-badge")
      await expect(badge).toBeVisible({ timeout: 60_000 })
      await expect(badge).toContainText("继续中（第")
      await expect(page.getByTestId("continue-phase-label")).toHaveText("增量澄清", {
        timeout: 60_000,
      })

      const continuing = await waitVersionStatus(request, token, versionId, "continuing")
      expect(continuing.continuePhase).toBe("clarifying")

      // Confirming the only draft requirement clears the phase to designing.
      await page.getByRole("button", { name: "确认需求", exact: true }).click()
      await expect(page.getByTestId("continue-phase-label")).toHaveText("增量设计", {
        timeout: 60_000,
      })
      const designing = await waitVersionStatus(request, token, versionId, "continuing")
      expect(designing.continuePhase).toBe("designing")
      expect(designing.paused).toBe(false)
      await page.screenshot({
        path: p7ScreenshotPath("p7-continuing-02-designing.png"),
        fullPage: true,
      })

      const plan = await waitP7IncrementalPlan(request, token, versionId)
      expect(plan.run?.kind).toBe("incremental_design")
      expect(plan.newCases.length).toBe(2)
      expect(plan.affectedCases.length).toBeGreaterThanOrEqual(1)
      expect(plan.parentBatchId).toBe(baseline.batch.id)
      expect(plan.defaultCaseIds.length).toBeGreaterThanOrEqual(3)
      expect(plan.degraded).toBe(false)
      expect(plan.defaultCaseIds.every((id) => /^\d+$/.test(id))).toBe(true)
      const supplementNodes = await listP7SupplementNodes(request, token, versionId)
      expect(supplementNodes).toHaveLength(2)
      expect(supplementNodes.every((node) => /^\d+$/.test(node.id))).toBe(true)

      await page.getByRole("button", { name: "查看增量计划" }).click()
      const panel = page.getByTestId("incremental-plan")
      await expect(panel).toBeVisible()
      await expect(page.getByTestId("incremental-plan-new-case")).toHaveCount(2)
      await expect(page.getByTestId("incremental-plan-affected-case")).toHaveCount(1)
      await expect(page.getByTestId("incremental-plan-create-batch")).toBeEnabled()
      await page.screenshot({
        path: p7ScreenshotPath("p7-continuing-03-incremental-plan.png"),
        fullPage: true,
      })

      await page.getByTestId("incremental-plan-create-batch").click()
      await expect(page.getByTestId("results-tab")).toBeVisible({ timeout: 60_000 })
    })

    const incremental =
      await test.step("incremental batch executes and the version completes", async () => {
        const batch = await waitP7BatchKind(request, token, versionId, "incremental", {
          timeoutMs: 120_000,
        })
        expect(batch.parentBatchId).toMatch(/^\d+$/)
        const done = await waitP7BatchTerminal(request, token, versionId, batch.id, 300_000)
        expect(done.status).toBe("done")
        expect(done.counts.total).toBe(3)
        expect(done.counts.passed).toBe(3)

        const runs = await listP7CaseRuns(request, token, versionId, done.id)
        expect(runs).toHaveLength(3)
        expect(runs.filter((run) => run.source === "ai_supplement")).toHaveLength(2)
        expect(runs.every((run) => run.status === "passed")).toBe(true)

        const completed = await waitVersionCompleted(request, token, versionId)
        expect(completed.continuePhase).toBeNull()
        await expect(page.getByTestId("continuing-badge")).toHaveCount(0, { timeout: 120_000 })
        await waitP7Events(request, token, versionId, [
          { type: "case.supplemented" },
          { type: "case.revalidation_marked" },
          { type: "report.published" },
        ])
        return done
      })

    await test.step("report/issue tabs: source badges, diff and Markdown export", async () => {
      const report = await waitP7ReportForBatch(request, token, versionId, incremental.id)
      expect(report.revision).toBe(1)
      expect(report.status).toBe("published")
      expect(report.batchKind).toBe("incremental")
      expect(report.content.cases.filter((row) => row.source === "ai_supplement")).toHaveLength(2)

      await page.goto(
        `/versions/${encodeURIComponent(versionId)}?tab=report&report=${encodeURIComponent(report.id)}`,
      )
      await expect(page.getByTestId("report-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 1")
      await expect(page.getByTestId("report-case-source")).toHaveCount(3)
      await expect(
        page.locator('[data-testid="report-case-source"][data-source="ai_supplement"]'),
      ).toHaveCount(2)
      await expect(page.getByTestId("report-diff")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-diff-base")).toBeVisible()
      await expect(page.getByTestId("report-diff-new-cases")).toHaveText("2", { timeout: 60_000 })

      const [download] = await Promise.all([
        page.waitForEvent("download"),
        page.getByTestId("report-export-btn").click(),
      ])
      expect(download.suggestedFilename()).toMatch(/\.md$/)
      const downloadPath = await download.path()
      expect(downloadPath, "the Markdown export must produce a readable file").not.toBeNull()
      if (downloadPath === null) throw new Error("Markdown export path is unavailable")
      const markdown = readFileSync(downloadPath, "utf8")
      expect(markdown).toContain("## 1. 执行摘要")
      expect(markdown).toContain("```mermaid")
      expect(markdown).toContain("P7 继续中下单用例")
      expect(markdown.includes(ADMIN_PASSWORD), "the report must not leak a password").toBe(false)
      await expect(page.getByTestId("report-export-notice")).toContainText("sha256")

      // The issues tab stays usable (the successful run produced none).
      await page.getByRole("tab", { name: "问题" }).click()
      await expect(page.getByTestId("issues-tab")).toBeVisible()
      await expect(page.getByTestId("issues-empty")).toBeVisible({ timeout: 60_000 })
      await expectNoSecretInPage(page, ADMIN_PASSWORD, "报告/问题 Tab")
      await page.screenshot({
        path: p7ScreenshotPath("p7-continuing-04-report.png"),
        fullPage: true,
      })
    })

    await test.step("overview metrics and batch timeline", async () => {
      await page.getByRole("tab", { name: "总览" }).click()
      await expect(page.getByTestId("overview-metrics")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("overview-batch-timeline")).toBeVisible()
      await expect(page.getByTestId("overview-batch-timeline")).toContainText("增量")
      await page.screenshot({
        path: p7ScreenshotPath("p7-continuing-05-overview.png"),
        fullPage: true,
      })
    })

    await expectNoConsoleErrors(monitor, page, "P7 继续中")
  } finally {
    monitor.stop()
    await setP6Mode(request, "normal").catch(() => undefined)
    await setP7Mode(request, "normal").catch(() => undefined)
  }
})

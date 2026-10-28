import { readFileSync } from "node:fs"

import { expect, test } from "@playwright/test"

import { ADMIN_PASSWORD, ADMIN_USERNAME, login } from "./p3-helpers"
import { listCaseRuns, setP6Mode } from "./p6-helpers"
import {
  captureConsoleErrors,
  createHumanIssue,
  expectNoConsoleErrors,
  expectNoSecretInPage,
  getP7Issue,
  openP7VersionPage,
  p7ScreenshotPath,
  seedP7ExecutionVersion,
  setP7Mode,
  skipP7SpecWhenHarnessMissing,
  waitP7AnalysisSettled,
  waitP7BatchKind,
  waitP7BatchTerminal,
  waitP7CaseRun,
  waitP7Events,
  waitP7Issue,
  waitP7ReportForBatch,
  waitVersionCompleted,
} from "./p7-helpers"

/**
 * P7 issues/report E2E (design spec §12.4/§6.3): one baseline batch fails with
 * an application defect, the fake failure analysis and issue triage materialize
 * one AI issue, a human issue is added, the issues tab confirms/dismisses both
 * (dismiss reason required), the report detects the changed input as stale and
 * regenerates revision 2, the supplement batch report provides the batch diff,
 * and the Markdown export plus the global issue/report deep links are verified.
 *
 * The test drives the real API, the real Playwright executor, the fake LLM and
 * the real browser; nothing is mocked.
 */

const SPEC_TIMEOUT_MS = 900_000

test.describe.configure({ mode: "serial" })

test("P7 问题与报告：失败分析 → 确认/驳回 → stale → 重新生成 → 差异/导出 → 全局深链", async ({
  page,
  request,
}) => {
  test.setTimeout(SPEC_TIMEOUT_MS)
  if (await skipP7SpecWhenHarnessMissing({ requireExecutor: true })) {
    return
  }

  const monitor = captureConsoleErrors(page)
  try {
    await setP7Mode(request, "normal")
    await setP6Mode(request, "fail")

    const seed = await test.step("seed a failing baseline batch", async () => {
      const seeded = await seedP7ExecutionVersion(request, { caseTitle: "P7 失败分析下单用例" })
      await login(page, ADMIN_USERNAME, ADMIN_PASSWORD)
      await openP7VersionPage(page, seeded.version)
      await page.getByTestId("exec-start-btn").click()
      const batch = await waitP7BatchKind(request, seeded.token, seeded.version.id, "baseline", {
        timeoutMs: 120_000,
      })
      const done = await waitP7BatchTerminal(
        request,
        seeded.token,
        seeded.version.id,
        batch.id,
        300_000,
      )
      expect(done.status).toBe("done")
      expect(done.counts.failed).toBe(1)
      // The auto-created supplement child batch must execute with the normal
      // planner, so the failure mode is switched back right after the baseline.
      await setP6Mode(request, "normal")

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
      expect(caseRun.evidence.length).toBeGreaterThan(0)
      return { seeded, batchId: done.id, caseRun }
    })

    const token = seed.seeded.token
    const versionId = seed.seeded.version.id

    const aiIssue =
      await test.step("fake failure analysis + triage materialize one AI issue", async () => {
        const analyses = await waitP7AnalysisSettled(request, token, versionId, seed.batchId, [
          "failure",
          "issue_triage",
        ])
        const failure = analyses.find((run) => run.kind === "failure")
        const triage = analyses.find((run) => run.kind === "issue_triage")
        expect(failure?.status).toBe("done")
        expect(triage?.status).toBe("done")

        const issue = await waitP7Issue(
          request,
          token,
          versionId,
          (candidate) => candidate.source === "ai" && candidate.category === "defect",
        )
        expect(issue.failureClass).toBe("application")
        expect(issue.severity).toBe("major")
        expect(issue.refs.some((ref) => ref.kind === "evidence")).toBe(true)
        expect(issue.revision).toBe(1)
        return issue
      })

    const baselineReport =
      await test.step("baseline report exists before the issue changes", async () => {
        const report = await waitP7ReportForBatch(request, token, versionId, seed.batchId)
        expect(report.batchNo).toBe(1)
        expect(report.status).toBe("published")
        return report
      })

    const supplement =
      await test.step("autonomy chain settles (supplement child + completed)", async () => {
        const child = await waitP7BatchKind(request, token, versionId, "supplement", {
          parentBatchId: seed.batchId,
          timeoutMs: 300_000,
        })
        const childDone = await waitP7BatchTerminal(request, token, versionId, child.id, 300_000)
        expect(childDone.status).toBe("done")
        expect(childDone.counts.failed).toBe(0)
        await waitVersionCompleted(request, token, versionId)
        return childDone
      })

    // Created only after the autonomous chain settled: a late fingerprint change
    // must not be auto-regenerated by the worker, so the stale/regenerate path
    // is fully user-driven.
    const humanIssue = await test.step("human issue via the real API", async () => {
      const issue = await createHumanIssue(request, token, versionId, {
        title: "P7 人工问题：结算页文案错误",
        severity: "minor",
        description: "结算页按钮文案与需求不一致。",
        reproSteps: "1. 打开结算页\n2. 检查提交按钮文案",
        caseRunId: seed.caseRun.id,
        caseNodeId: seed.seeded.caseNodeId,
      })
      expect(issue.source).toBe("human")
      expect(issue.status).toBe("open")
      return issue
    })

    await test.step("issues tab: AI issue detail, evidence viewer and confirm", async () => {
      await page.goto(`/versions/${encodeURIComponent(versionId)}?tab=issues`)
      await expect(page.getByTestId("issues-tab")).toBeVisible({ timeout: 60_000 })

      await page.getByTestId(`issue-row-${aiIssue.id}`).click()
      const detail = page.getByTestId("issue-detail")
      await expect(detail).toBeVisible({ timeout: 60_000 })
      await expect(detail).toHaveAttribute("data-issue-id", aiIssue.id)
      await expect(detail).toContainText(aiIssue.title)
      await expect(detail).toContainText("复现步骤")
      await expect(detail.locator("pre code")).toContainText("运行失败用例")

      await page.getByTestId("issue-evidence-link").first().click()
      const viewer = page.getByTestId("evidence-viewer")
      await expect(viewer).toBeVisible({ timeout: 30_000 })
      await expect(viewer).toHaveAttribute("data-evidence-kind", "screenshot")
      await page.keyboard.press("Escape")
      await expect(viewer).toBeHidden({ timeout: 10_000 })

      await page.getByTestId("issue-confirm-btn").click()
      await expect(detail).toContainText("已确认", { timeout: 30_000 })
      await expect(page.getByTestId("issue-confirm-btn")).toBeDisabled()
      await expect(page.getByTestId("issue-dismiss-btn")).toBeDisabled()
      const confirmed = await getP7Issue(request, token, versionId, aiIssue.id)
      expect(confirmed.status).toBe("confirmed")
      expect(confirmed.revision).toBe(2)
    })

    await test.step("issues tab: dismiss requires a reason and records it", async () => {
      await page.getByTestId(`issue-row-${humanIssue.id}`).click()
      const detail = page.getByTestId("issue-detail")
      await expect(detail).toHaveAttribute("data-issue-id", humanIssue.id, { timeout: 30_000 })

      await page.getByTestId("issue-dismiss-btn").click()
      const dialog = page.getByTestId("issue-dismiss-dialog")
      await expect(dialog).toBeVisible()
      await expect(dialog.getByRole("button", { name: "确认驳回" })).toBeDisabled()

      const reason = `P7 E2E 驳回原因 ${String(Date.now())}`
      await page.getByTestId("issue-dismiss-reason").fill(reason)
      await expect(dialog.getByRole("button", { name: "确认驳回" })).toBeEnabled()
      await dialog.getByRole("button", { name: "确认驳回" }).click()
      await expect(dialog).toBeHidden({ timeout: 30_000 })
      await expect(detail).toContainText("已驳回")
      await expect(detail).toContainText(reason)
      const dismissed = await getP7Issue(request, token, versionId, humanIssue.id)
      expect(dismissed.status).toBe("dismissed")
      expect(dismissed.dismissedReason).toBe(reason)
      await page.screenshot({
        path: p7ScreenshotPath("p7-issues-01-confirmed-dismissed.png"),
        fullPage: true,
      })
    })

    const regenerated = await test.step("report stale banner → regenerate revision 2", async () => {
      await page.goto(
        `/versions/${encodeURIComponent(versionId)}?tab=report&report=${encodeURIComponent(baselineReport.id)}`,
      )
      await expect(page.getByTestId("report-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 1")
      const staleBanner = page.getByTestId("report-stale-banner")
      await expect(staleBanner).toBeVisible({ timeout: 60_000 })
      await expect(staleBanner).toContainText("报告已过期")

      await page.getByTestId("report-regenerate-btn").click()
      await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 2", {
        timeout: 60_000,
      })
      await expect(page.getByTestId("report-stale-banner")).toHaveCount(0)

      // The UI already regenerated; a direct read of the batch must be fresh.
      const reports = await waitP7ReportForBatch(request, token, versionId, seed.batchId)
      expect(reports.revision).toBe(2)
      expect(reports.status).toBe("published")

      await waitP7Events(request, token, versionId, [
        { type: "report.stale" },
        { type: "issue.updated" },
      ])
      await page.screenshot({
        path: p7ScreenshotPath("p7-issues-02-report-regenerated.png"),
        fullPage: true,
      })
      return reports
    })

    await test.step("batch diff and Markdown export on the supplement report", async () => {
      const supplementReport = await waitP7ReportForBatch(request, token, versionId, supplement.id)
      await page.goto(
        `/versions/${encodeURIComponent(versionId)}?tab=report&report=${encodeURIComponent(supplementReport.id)}`,
      )
      await expect(page.getByTestId("report-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-revision-badge")).toBeVisible({ timeout: 60_000 })

      // Issue terminal states changed after this batch report was generated.
      const staleBanner = page.getByTestId("report-stale-banner")
      if (await staleBanner.isVisible().catch(() => false)) {
        await page.getByTestId("report-regenerate-btn").click()
        await expect(staleBanner).toHaveCount(0, { timeout: 60_000 })
      }

      await expect(page.getByTestId("report-diff")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-diff-new-cases")).toHaveText("2", {
        timeout: 60_000,
      })
      const diffBase = page.getByTestId("report-diff-base")
      await diffBase.selectOption(regenerated.id)
      await expect(diffBase).toHaveValue(regenerated.id)
      await expect(page.getByTestId("report-diff-new-cases")).toHaveText("2")

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
      expect(markdown.includes(ADMIN_PASSWORD), "the report must not leak a password").toBe(false)
      await expect(page.getByTestId("report-export-notice")).toContainText("sha256")
      await expectNoSecretInPage(page, ADMIN_PASSWORD, "报告差异/导出")
      await page.screenshot({
        path: p7ScreenshotPath("p7-issues-03-report-diff.png"),
        fullPage: true,
      })
    })

    await test.step("global issue and report centres deep-link to the version tabs", async () => {
      await page.goto("/issues")
      await expect(page.getByTestId("global-issues-page")).toBeVisible({ timeout: 60_000 })
      const issueRow = page.getByTestId(`global-issue-row-${aiIssue.id}`)
      await expect(issueRow).toBeVisible({ timeout: 60_000 })
      const issueHref = await issueRow.getByRole("link").getAttribute("href")
      expect(issueHref).toContain(`/versions/${versionId}`)
      expect(issueHref).toContain(`issue=${aiIssue.id}`)
      await issueRow.getByRole("link").click()
      await expect(page.getByTestId("issues-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("issue-detail")).toHaveAttribute("data-issue-id", aiIssue.id, {
        timeout: 60_000,
      })

      await page.goto("/reports")
      await expect(page.getByTestId("global-reports-page")).toBeVisible({ timeout: 60_000 })
      const reportRow = page.getByTestId(`global-report-row-${regenerated.id}`)
      await expect(reportRow).toBeVisible({ timeout: 60_000 })
      const reportHref = await reportRow.getByRole("link").getAttribute("href")
      expect(reportHref).toContain(`/versions/${versionId}`)
      expect(reportHref).toContain(`report=${regenerated.id}`)
      await reportRow.getByRole("link").click()
      await expect(page.getByTestId("report-tab")).toBeVisible({ timeout: 60_000 })
      await expect(page.getByTestId("report-revision-badge")).toHaveText("revision 2", {
        timeout: 60_000,
      })
      await page.screenshot({
        path: p7ScreenshotPath("p7-issues-04-global-centres.png"),
        fullPage: true,
      })
    })

    await expectNoConsoleErrors(monitor, page, "P7 问题与报告")
  } finally {
    monitor.stop()
    await setP6Mode(request, "normal").catch(() => undefined)
    await setP7Mode(request, "normal").catch(() => undefined)
  }
})

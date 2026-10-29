import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { ReportDiff as ReportDiffView, ReportSummary } from "../types"

const mocks = vi.hoisted(() => ({
  diffReports: vi.fn(),
}))

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>()
  return { ...actual, diffReports: mocks.diffReports }
})

import { ReportDiff } from "./ReportDiff"

const VERSION_ID = "1844674407370955161"
const REPORT_2 = "1844674407370955162"
const REPORT_1 = "1844674407370955163"
const BATCH_1 = "1844674407370955164"
const BATCH_2 = "1844674407370955165"
const REPORT_3 = "1844674407370955168"

function reportSummary(id: string, batchId: string, revision: number): ReportSummary {
  return {
    id,
    versionId: VERSION_ID,
    batchId,
    revision,
    status: "published",
    title: `报告 ${revision}`,
    inputFingerprint: "a".repeat(64),
    markdown: { sha256: "b".repeat(64), sizeBytes: 1, downloadPath: "" },
    staleReason: "",
    publishedAt: null,
    createdAt: "",
    updatedAt: "",
  }
}

const REPORTS = [reportSummary(REPORT_2, BATCH_2, 2), reportSummary(REPORT_1, BATCH_1, 1)]
const BATCH_NOS = { [BATCH_1]: 1, [BATCH_2]: 2 }

function diff(overrides: Partial<ReportDiffView> = {}): ReportDiffView {
  return {
    baseReportId: REPORT_1,
    targetReportId: REPORT_2,
    baseBatchNo: 1,
    targetBatchNo: 2,
    summary: {
      addedIssues: 1,
      fixedIssues: 2,
      regressed: 1,
      recovered: 1,
      newCases: 2,
      removedCases: 1,
      changed: 1,
    },
    addedIssues: [{ issueId: "1844674407370955170", title: "新问题", severity: "major" }],
    fixedIssues: [{ issueId: "1844674407370955171", title: "已修复问题", severity: "minor" }],
    regressedCases: [
      {
        caseNodeId: "1844674407370955172",
        title: "回归用例",
        from: "passed",
        to: "failed",
      },
    ],
    recoveredCases: [
      {
        caseNodeId: "1844674407370955173",
        title: "恢复用例",
        from: "failed",
        to: "passed",
      },
    ],
    changedCases: [],
    newCases: [{ caseNodeId: "1844674407370955174", title: "新增用例", source: "ai_supplement" }],
    removedCases: [],
    ...overrides,
  }
}

describe("ReportDiff", () => {
  beforeEach(() => {
    mocks.diffReports.mockReset()
    mocks.diffReports.mockResolvedValue(diff())
  })

  afterEach(() => {
    cleanup()
  })

  it("diffs against the default previous-batch base and shows the counts", async () => {
    render(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_2}
        reports={REPORTS}
        batchNoById={BATCH_NOS}
      />,
    )

    await waitFor(() =>
      expect(mocks.diffReports).toHaveBeenCalledWith(VERSION_ID, REPORT_2, undefined),
    )
    expect(await screen.findByTestId("report-diff-added-issues")).toBeTruthy()
    expect(screen.getByTestId("report-diff-added-issues").textContent).toContain("1")
    expect(screen.getByTestId("report-diff-fixed-issues").textContent).toContain("2")
    expect(screen.getByTestId("report-diff-regressed").textContent).toContain("1")
    expect(screen.getByTestId("report-diff-recovered").textContent).toContain("1")
    expect(screen.getByTestId("report-diff-new-cases").textContent).toContain("2")
    expect(screen.getByTestId("report-diff-removed-cases").textContent).toContain("1")
    expect(screen.getByText("新问题")).toBeTruthy()
    expect(screen.getByText("回归用例")).toBeTruthy()
  })

  it("loads the diff against an explicitly selected base report", async () => {
    const explicit = diff({
      summary: {
        addedIssues: 0,
        fixedIssues: 0,
        regressed: 3,
        recovered: 0,
        newCases: 0,
        removedCases: 0,
        changed: 0,
      },
      addedIssues: [],
      fixedIssues: [],
      regressedCases: [],
    })
    mocks.diffReports.mockResolvedValueOnce(diff()).mockResolvedValueOnce(explicit)

    render(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_2}
        reports={REPORTS}
        batchNoById={BATCH_NOS}
      />,
    )
    await screen.findByTestId("report-diff")

    fireEvent.change(screen.getByTestId("report-diff-base"), {
      target: { value: REPORT_1 },
    })

    await waitFor(() =>
      expect(mocks.diffReports).toHaveBeenLastCalledWith(VERSION_ID, REPORT_2, REPORT_1),
    )
    await waitFor(() =>
      expect(screen.getByTestId("report-diff-regressed").textContent).toContain("3"),
    )
  })

  it("absorbs the first-batch 404 into a neutral no-previous-report state", async () => {
    mocks.diffReports.mockRejectedValueOnce(
      new ApiError({
        status: 404,
        code: "report_not_found",
        message: "report not found",
        details: { reason: "report_not_found" },
      }),
    )

    render(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_2}
        reports={REPORTS}
        batchNoById={BATCH_NOS}
      />,
    )

    const neutral = await screen.findByTestId("report-diff-unavailable")
    expect(neutral.textContent).toContain("无上一批次报告，无法对比")
    expect(screen.queryByTestId("report-diff-error")).toBeNull()
  })

  it("keeps reporting failures for an explicitly selected base", async () => {
    mocks.diffReports.mockResolvedValueOnce(diff()).mockRejectedValueOnce(
      new ApiError({
        status: 404,
        code: "report_not_found",
        message: "explicit base missing",
        details: { reason: "report_not_found" },
      }),
    )

    render(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_2}
        reports={REPORTS}
        batchNoById={BATCH_NOS}
      />,
    )
    await screen.findByTestId("report-diff-added-issues")

    fireEvent.change(screen.getByTestId("report-diff-base"), {
      target: { value: REPORT_1 },
    })

    const alert = await screen.findByTestId("report-diff-error")
    expect(alert.textContent).toContain("资源不存在")
    expect(screen.queryByTestId("report-diff-unavailable")).toBeNull()
  })

  it("resets an explicit base when the target report changes", async () => {
    const { rerender } = render(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_2}
        reports={REPORTS}
        batchNoById={BATCH_NOS}
      />,
    )
    await screen.findByTestId("report-diff-added-issues")

    fireEvent.change(screen.getByTestId("report-diff-base"), {
      target: { value: REPORT_1 },
    })
    await waitFor(() =>
      expect(mocks.diffReports).toHaveBeenLastCalledWith(VERSION_ID, REPORT_2, REPORT_1),
    )

    rerender(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_3}
        reports={[...REPORTS, reportSummary(REPORT_3, BATCH_2, 3)]}
        batchNoById={BATCH_NOS}
      />,
    )

    await waitFor(() =>
      expect(mocks.diffReports).toHaveBeenLastCalledWith(VERSION_ID, REPORT_3, undefined),
    )
    expect((screen.getByTestId("report-diff-base") as HTMLSelectElement).value).toBe("")
    expect(mocks.diffReports).not.toHaveBeenCalledWith(VERSION_ID, REPORT_3, REPORT_1)
  })

  it("renders the no-difference and error states", async () => {
    mocks.diffReports.mockResolvedValueOnce(
      diff({
        summary: {
          addedIssues: 0,
          fixedIssues: 0,
          regressed: 0,
          recovered: 0,
          newCases: 0,
          removedCases: 0,
          changed: 0,
        },
        addedIssues: [],
        fixedIssues: [],
        regressedCases: [],
        recoveredCases: [],
        changedCases: [],
        newCases: [],
        removedCases: [],
      }),
    )
    const { unmount } = render(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_2}
        reports={REPORTS}
        batchNoById={BATCH_NOS}
      />,
    )
    expect(await screen.findByText("两版报告无差异")).toBeTruthy()
    unmount()

    mocks.diffReports.mockRejectedValueOnce(
      new ApiError({ status: 409, code: "conflict", message: "base report missing" }),
    )
    render(
      <ReportDiff
        versionId={VERSION_ID}
        reportId={REPORT_2}
        reports={REPORTS}
        batchNoById={BATCH_NOS}
      />,
    )
    const alert = await screen.findByTestId("report-diff-error")
    expect(alert.textContent).toContain("base report missing")
  })
})

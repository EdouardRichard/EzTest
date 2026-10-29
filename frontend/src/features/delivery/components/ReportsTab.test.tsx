import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { ReportBatchSummary, ReportContent, ReportDetail, ReportSummary } from "../types"

const mocks = vi.hoisted(() => ({
  listReportBatches: vi.fn(),
  listReports: vi.fn(),
  getReport: vi.fn(),
  ensureReport: vi.fn(),
  regenerateReport: vi.fn(),
  exportReport: vi.fn(),
  diffReports: vi.fn(),
}))

vi.mock("../api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api")>()
  return {
    ...actual,
    listReportBatches: mocks.listReportBatches,
    listReports: mocks.listReports,
    getReport: mocks.getReport,
    ensureReport: mocks.ensureReport,
    regenerateReport: mocks.regenerateReport,
    exportReport: mocks.exportReport,
    diffReports: mocks.diffReports,
  }
})

import { ReportsTab } from "./ReportsTab"

const VERSION_ID = "1844674407370955161"
const BATCH_1 = "1844674407370955162"
const BATCH_2 = "1844674407370955163"
const REPORT_1 = "1844674407370955164"
const REPORT_2 = "1844674407370955165"
const REPORT_3 = "1844674407370955166"
const REPORT_2_R1 = "1844674407370955167"

const STREAM = { events: [], status: "live" as const }

const BATCHES: ReportBatchSummary[] = [
  { id: BATCH_2, batchNo: 2, status: "done", kind: "incremental" },
  { id: BATCH_1, batchNo: 1, status: "done", kind: "baseline" },
]

function reportSummary(
  id: string,
  batchId: string,
  revision: number,
  overrides: Partial<ReportSummary> = {},
): ReportSummary {
  return {
    id,
    versionId: VERSION_ID,
    batchId,
    revision,
    status: "published",
    title: `报告 revision ${revision}`,
    inputFingerprint: "a".repeat(64),
    markdown: { sha256: "b".repeat(64), sizeBytes: 2048, downloadPath: `/api/x/${id}` },
    staleReason: "",
    publishedAt: "2026-10-28T01:00:00Z",
    createdAt: "2026-10-28T00:00:00Z",
    updatedAt: "2026-10-28T01:00:00Z",
    ...overrides,
  }
}

function reportContent(overrides: Partial<ReportContent> = {}): ReportContent {
  return {
    schema: "eztest.report.v1",
    version: {
      id: VERSION_ID,
      name: "示例版本",
      label: "",
      status: "completed",
      codeBaselineCommit: "abc123baseline",
    },
    batch: {
      id: BATCH_2,
      batchNo: 2,
      kind: "incremental",
      status: "done",
      baselineHash: "c".repeat(64),
      graphHash: "d".repeat(64),
      codeBaseCommit: "",
      headCommit: "",
      startedAt: "2026-10-28T00:00:00Z",
      finishedAt: "2026-10-28T00:00:30Z",
      baseFallbackReason: "",
      historyRewritten: false,
    },
    summary: {
      caseCount: 3,
      passed: 2,
      failed: 1,
      blocked: 0,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
      flakyCount: 1,
    },
    cases: [
      {
        caseRunId: "1844674407370955170",
        caseNodeId: "1844674407370955171",
        title: "登录用例",
        source: "ai_supplement",
        status: "failed",
        failureClass: "application",
        attempt: 1,
        stepCount: 4,
        assertionCount: 2,
        evidenceCount: 3,
      },
    ],
    requirements: [
      {
        requirementId: "1844674407370955172",
        title: "必须支持登录",
        status: "clarified",
        caseCount: 2,
        passed: 1,
        failed: 1,
      },
    ],
    issues: [
      {
        issueId: "1844674407370955173",
        title: "登录按钮无响应",
        severity: "major",
        status: "open",
        confidence: "high",
        category: "defect",
        caseNodeId: "1844674407370955171",
        codeRefs: [{ path: "src/login.ts", line: 42 }],
      },
    ],
    risks: ["登录路径存在回归风险"],
    degraded: false,
    appendix: {
      environment: "dev",
      system: "示例系统",
      commit: "abc123",
      generatedAt: "2026-10-28T01:00:00Z",
    },
    ...overrides,
  }
}

function reportDetail(
  id: string,
  batchId: string,
  revision: number,
  overrides: Partial<ReportDetail> = {},
): ReportDetail {
  return {
    ...reportSummary(id, batchId, revision),
    batchNo: 2,
    batchKind: "incremental",
    content: reportContent(),
    ...overrides,
  }
}

const REPORT_2_SUMMARY = reportSummary(REPORT_2, BATCH_2, 2, {
  status: "stale",
  staleReason: "批次结果已更新",
})
const REPORT_2_R1_SUMMARY = reportSummary(REPORT_2_R1, BATCH_2, 1)
const REPORT_1_SUMMARY = reportSummary(REPORT_1, BATCH_1, 1)

const DETAIL_2 = reportDetail(REPORT_2, BATCH_2, 2, {
  status: "stale",
  staleReason: "批次结果已更新",
})
const DETAIL_2_R1 = reportDetail(REPORT_2_R1, BATCH_2, 1)
// A P7-era revision: its snapshot predates the D49 fields, so they stay absent.
const LEGACY_CONTENT = reportContent({ degraded: true })
delete LEGACY_CONTENT.batch.baseFallbackReason
delete LEGACY_CONTENT.batch.historyRewritten
const DETAIL_1 = reportDetail(REPORT_1, BATCH_1, 1, {
  content: LEGACY_CONTENT,
})

function emptyPage() {
  return { items: [], nextCursor: "", hasMore: false }
}

function page<T>(items: T[]) {
  return { items, nextCursor: "", hasMore: false }
}

const EMPTY_DIFF = {
  baseReportId: REPORT_1,
  targetReportId: REPORT_2,
  baseBatchNo: 1,
  targetBatchNo: 2,
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
}

describe("ReportsTab", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listReportBatches.mockResolvedValue(page(BATCHES))
    mocks.listReports.mockImplementation(
      (_versionId: string, filters: { batchId?: string } = {}) => {
        if (filters.batchId === BATCH_1) return Promise.resolve(page([REPORT_1_SUMMARY]))
        if (filters.batchId === BATCH_2)
          return Promise.resolve(page([REPORT_2_SUMMARY, REPORT_2_R1_SUMMARY]))
        return Promise.resolve(page([REPORT_2_SUMMARY, REPORT_2_R1_SUMMARY, REPORT_1_SUMMARY]))
      },
    )
    mocks.getReport.mockImplementation((_versionId: string, reportId: string) => {
      if (reportId === REPORT_1) return Promise.resolve(DETAIL_1)
      if (reportId === REPORT_2_R1) return Promise.resolve(DETAIL_2_R1)
      return Promise.resolve(DETAIL_2)
    })
    mocks.diffReports.mockResolvedValue(EMPTY_DIFF)
  })

  afterEach(() => {
    cleanup()
  })

  it("loads the newest batch report with revision, stale banner and content", async () => {
    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)

    await screen.findByTestId("report-revision-badge")
    expect(screen.getByTestId("report-revision-badge").textContent).toContain("revision 2")
    expect(screen.getByTestId("report-stale-banner").textContent).toContain("批次结果已更新")

    const detail = screen.getByTestId("report-tab")
    expect(within(detail).getByText("Batch #2")).toBeTruthy()
    expect(within(detail).getByText("增量")).toBeTruthy()
    expect(within(detail).getByText("已过期")).toBeTruthy()
    expect(within(detail).getByText("登录用例")).toBeTruthy()
    expect(within(detail).getByTestId("report-case-source").textContent).toContain("补充")
    expect(within(detail).getByText("登录按钮无响应")).toBeTruthy()
    expect(within(detail).getByText("必须支持登录")).toBeTruthy()
    expect(within(detail).getByText("登录路径存在回归风险")).toBeTruthy()
    expect(within(detail).getByText(/示例系统/)).toBeTruthy()

    expect(mocks.listReports).toHaveBeenCalledWith(
      VERSION_ID,
      expect.objectContaining({ batchId: BATCH_2 }),
      expect.anything(),
    )
  })

  it("switches batches and loads that batch's report", async () => {
    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    await screen.findByTestId("report-revision-badge")

    fireEvent.change(screen.getByTestId("report-batch-selector"), {
      target: { value: BATCH_1 },
    })

    await waitFor(() =>
      expect(mocks.getReport).toHaveBeenLastCalledWith(VERSION_ID, REPORT_1, expect.anything()),
    )
    await waitFor(() =>
      expect(screen.getByTestId("report-revision-badge").textContent).toContain("revision 1"),
    )
    expect(screen.queryByTestId("report-stale-banner")).toBeNull()
    expect(screen.getByTestId("report-degraded").textContent).toContain("AI 分析不可用")
  })

  it("lists the active batch revisions and switches revision", async () => {
    const onReportChange = vi.fn()
    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} onReportChange={onReportChange} />)

    await screen.findByTestId("report-revision-badge")
    const selector = screen.getByTestId("report-revision-selector") as HTMLSelectElement
    expect(selector.value).toBe(REPORT_2)
    expect(mocks.listReports).toHaveBeenCalledWith(
      VERSION_ID,
      expect.objectContaining({ batchId: BATCH_2, limit: 100 }),
      expect.anything(),
    )
    expect(
      within(selector)
        .getAllByRole("option")
        .map((option) => (option as HTMLOptionElement).value),
    ).toEqual([REPORT_2, REPORT_2_R1])

    fireEvent.change(selector, { target: { value: REPORT_2_R1 } })
    await waitFor(() => expect(onReportChange).toHaveBeenCalledWith(REPORT_2_R1))
    await waitFor(() =>
      expect(mocks.getReport).toHaveBeenLastCalledWith(VERSION_ID, REPORT_2_R1, expect.anything()),
    )
    await waitFor(() =>
      expect(screen.getByTestId("report-revision-badge").textContent).toContain("revision 1"),
    )
    expect((screen.getByTestId("report-batch-selector") as HTMLSelectElement).value).toBe(BATCH_2)
  })

  it("shows D49 base fallback fields and the legacy unrecorded state", async () => {
    const fallbackContent = reportContent()
    fallbackContent.batch = {
      ...fallbackContent.batch,
      baseFallbackReason: "history_rewritten",
      historyRewritten: true,
    }
    const withFallback = reportDetail(REPORT_2, BATCH_2, 2, { content: fallbackContent })
    mocks.getReport.mockImplementation((_versionId: string, reportId: string) => {
      if (reportId === REPORT_1) return Promise.resolve(DETAIL_1)
      if (reportId === REPORT_2_R1) return Promise.resolve(DETAIL_2_R1)
      return Promise.resolve(withFallback)
    })

    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)

    const fallback = await screen.findByTestId("report-batch-fallback")
    expect(fallback.textContent).toContain("history_rewritten")
    expect(fallback.textContent).toContain("历史已回退")
    expect(screen.getByTestId("report-batch-baseline").textContent).toContain("abc123baseline")

    fireEvent.change(screen.getByTestId("report-batch-selector"), {
      target: { value: BATCH_1 },
    })
    await waitFor(() =>
      expect(screen.getByTestId("report-batch-fallback").textContent).toContain("未记录"),
    )
    expect(screen.getByTestId("report-batch-baseline").textContent).toContain("abc123baseline")
  })

  it("regenerates a stale report into a new revision", async () => {
    const regenerated = reportDetail(REPORT_3, BATCH_2, 3)
    mocks.regenerateReport.mockResolvedValueOnce(regenerated)
    mocks.getReport.mockImplementation((_versionId: string, reportId: string) => {
      if (reportId === REPORT_3) return Promise.resolve(regenerated)
      if (reportId === REPORT_1) return Promise.resolve(DETAIL_1)
      return Promise.resolve(DETAIL_2)
    })

    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    await screen.findByTestId("report-stale-banner")
    fireEvent.click(screen.getByTestId("report-regenerate-btn"))

    await waitFor(() => expect(mocks.regenerateReport).toHaveBeenCalledWith(VERSION_ID, REPORT_2))
    await waitFor(() =>
      expect(screen.getByTestId("report-revision-badge").textContent).toContain("revision 3"),
    )
    expect(screen.queryByTestId("report-stale-banner")).toBeNull()
  })

  it("exports the Markdown report and shows the downloaded SHA256 prefix", async () => {
    const createObjectUrl = vi.fn(() => "blob:mock")
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectUrl })
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() })
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})
    mocks.exportReport.mockResolvedValueOnce({
      blob: new Blob(["# report"]),
      filename: "report-r2.md",
      sha256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
    })

    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    await screen.findByTestId("report-export-btn")
    fireEvent.click(screen.getByTestId("report-export-btn"))

    await waitFor(() => expect(mocks.exportReport).toHaveBeenCalledWith(VERSION_ID, REPORT_2))
    expect(createObjectUrl).toHaveBeenCalledTimes(1)
    const notice = await screen.findByTestId("report-export-notice")
    expect(notice.textContent).toContain("report-r2.md")
    expect(notice.textContent).toContain("sha256 abcdef012345")
  })

  it("shows a retryable export failure", async () => {
    mocks.exportReport.mockRejectedValueOnce(
      new ApiError({
        status: 500,
        code: "internal",
        message: "missing file",
        retryable: true,
        details: { reason: "report_export_failed" },
      }),
    )

    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    fireEvent.click(await screen.findByTestId("report-export-btn"))

    const alert = await screen.findByTestId("report-export-error")
    expect(alert.textContent).toContain("重试")
  })

  it("offers report generation when the batch has no report yet", async () => {
    mocks.listReports.mockImplementation(
      (_versionId: string, filters: { batchId?: string } = {}) => {
        if (filters.batchId === BATCH_1) return Promise.resolve(page([]))
        return Promise.resolve(page([REPORT_2_SUMMARY]))
      },
    )
    const generated = reportDetail(REPORT_3, BATCH_1, 1)
    mocks.ensureReport.mockResolvedValueOnce(generated)
    mocks.getReport.mockImplementation((_versionId: string, reportId: string) =>
      Promise.resolve(reportId === REPORT_3 ? generated : DETAIL_2),
    )

    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    fireEvent.change(await screen.findByTestId("report-batch-selector"), {
      target: { value: BATCH_1 },
    })
    expect(await screen.findByText("该批次尚无报告")).toBeTruthy()
    fireEvent.click(screen.getByTestId("report-generate-btn"))

    await waitFor(() => expect(mocks.ensureReport).toHaveBeenCalledWith(VERSION_ID, BATCH_1))
    await waitFor(() =>
      expect(screen.getByTestId("report-revision-badge").textContent).toContain("revision 1"),
    )
  })

  it("shows the no-batch empty state and the API error state", async () => {
    mocks.listReportBatches.mockResolvedValueOnce(emptyPage())
    mocks.listReports.mockResolvedValue(emptyPage())
    const { unmount } = render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    expect(await screen.findByTestId("report-empty")).toBeTruthy()
    expect(screen.getByText("还没有执行批次")).toBeTruthy()
    unmount()

    mocks.listReportBatches.mockRejectedValueOnce(
      new ApiError({ status: 403, code: "forbidden", message: "forbidden" }),
    )
    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    const alert = await screen.findByTestId("report-error")
    expect(alert.textContent).toContain("无权限")
  })

  it("opens a deep-linked report revision and switches to its batch", async () => {
    const onReportChange = vi.fn()
    render(
      <ReportsTab
        versionId={VERSION_ID}
        stream={STREAM}
        reportId={REPORT_1}
        onReportChange={onReportChange}
      />,
    )

    await waitFor(() =>
      expect(screen.getByTestId("report-revision-badge").textContent).toContain("revision 1"),
    )
    expect(mocks.getReport).toHaveBeenCalledWith(VERSION_ID, REPORT_1, expect.anything())
    expect((screen.getByTestId("report-batch-selector") as HTMLSelectElement).value).toBe(BATCH_1)
    expect((screen.getByTestId("report-revision-selector") as HTMLSelectElement).value).toBe(
      REPORT_1,
    )
    expect(screen.getByTestId("report-degraded")).toBeTruthy()
  })

  it("deep-links report cases and issues and routes through the caller callbacks", async () => {
    const onOpenIssue = vi.fn()
    const onOpenCase = vi.fn()

    render(
      <ReportsTab
        versionId={VERSION_ID}
        stream={STREAM}
        onOpenIssue={onOpenIssue}
        onOpenCase={onOpenCase}
      />,
    )

    const caseLink = await screen.findByTestId("report-case-link-1844674407370955170")
    expect(caseLink.getAttribute("href")).toBe(
      `/versions/${VERSION_ID}?tab=results&caseRun=1844674407370955170`,
    )
    const issueLink = screen.getByTestId("report-issue-link-1844674407370955173")
    expect(issueLink.getAttribute("href")).toBe(
      `/versions/${VERSION_ID}?tab=issues&issue=1844674407370955173`,
    )

    fireEvent.click(caseLink)
    fireEvent.click(issueLink)
    expect(onOpenCase).toHaveBeenCalledWith("1844674407370955170", "1844674407370955171")
    expect(onOpenIssue).toHaveBeenCalledWith("1844674407370955173")
  })

  it("renders the report diff section for the selected report", async () => {
    render(<ReportsTab versionId={VERSION_ID} stream={STREAM} />)
    await screen.findByTestId("report-diff")
    expect(mocks.diffReports).toHaveBeenCalledWith(VERSION_ID, REPORT_2, undefined)
  })
})

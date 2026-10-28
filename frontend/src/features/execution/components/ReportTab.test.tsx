import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"

import type { Batch, ReportDownload, ReportView } from "../types"

const VERSION_ID = "1844674407370955161"
const BATCH_1 = "1844674407370955200"
const BATCH_2 = "1844674407370955300"
const GENERATED_AT = "2026-10-28T10:00:00Z"
const BASELINE_HASH = "a".repeat(64)
const COMMIT = "9abab3e"

const mocks = vi.hoisted(() => ({
  useBatches: vi.fn(),
  useReport: vi.fn(),
  exportReport: vi.fn(),
}))

vi.mock("../hooks/useExecution", () => ({
  useBatches: mocks.useBatches,
  useReport: mocks.useReport,
}))

vi.mock("../api", () => ({
  exportReport: mocks.exportReport,
}))

import { ReportTab } from "./ReportTab"

function batch(overrides: Partial<Batch> = {}): Batch {
  return {
    id: BATCH_2,
    version_id: VERSION_ID,
    batch_no: 2,
    status: "done",
    paused: false,
    pause_mode: "",
    pause_reason: "",
    baseline_hash: BASELINE_HASH,
    code_baseline_commit: COMMIT,
    head_commit: null,
    counts: {
      total: 4,
      passed: 2,
      failed: 1,
      blocked: 1,
      inconclusive: 0,
      skipped: 0,
      interrupted: 0,
    },
    created_by: "1",
    started_at: "2026-10-28T09:50:00Z",
    finished_at: "2026-10-28T10:00:00Z",
    failure_reason: "",
    ...overrides,
  }
}

function report(overrides: Partial<ReportView> = {}): ReportView {
  return {
    batch: {
      id: BATCH_2,
      batch_no: 2,
      status: "done",
      baseline_hash: BASELINE_HASH,
      code_baseline_commit: COMMIT,
    },
    summary: {
      case_count: 4,
      by_status: { passed: 2, failed: 1, blocked: 1, inconclusive: 0 },
      by_failure_class: { application: 1, environment: 1 },
      flaky_case_ids: ["case-run-2"],
      duration_ms: 123456,
      evidence_count: 7,
    },
    cases: [
      {
        case_run_id: "case-run-1",
        case_node_id: "node-1",
        title: "下单成功",
        status: "passed",
        failure_class: null,
        steps: 3,
        assertions: 2,
        evidence: 4,
        duration_ms: 1234,
      },
      {
        case_run_id: "case-run-2",
        case_node_id: "node-2",
        title: "支付失败",
        status: "failed",
        failure_class: "application",
        steps: 5,
        assertions: 3,
        evidence: 2,
        duration_ms: 4567,
      },
    ],
    environment: {
      system: "商城",
      environment: "预发",
      base_url: "http://fixture.test",
    },
    generated_at: GENERATED_AT,
    ...overrides,
  }
}

function reportResult(batchId: string) {
  if (batchId === BATCH_1) {
    const first = report()
    return {
      report: {
        ...first,
        batch: { ...first.batch, id: BATCH_1, batch_no: 1 },
        cases: [
          {
            case_run_id: "case-run-11",
            case_node_id: "node-11",
            title: "批量导出",
            status: "blocked",
            failure_class: "environment",
            steps: 2,
            assertions: 1,
            evidence: 1,
            duration_ms: 2500,
          },
        ],
      } satisfies ReportView,
      loading: false,
      error: "",
      streamStatus: "live" as const,
      refresh: vi.fn(),
    }
  }
  if (batchId === BATCH_2) {
    return {
      report: report(),
      loading: false,
      error: "",
      streamStatus: "live" as const,
      refresh: vi.fn(),
    }
  }
  return {
    report: null,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn(),
  }
}

function batchesResult(batches: Batch[], overrides: Record<string, unknown> = {}) {
  return {
    batches,
    nextCursor: "",
    hasMore: false,
    loading: false,
    error: "",
    streamStatus: "live" as const,
    refresh: vi.fn(),
    ...overrides,
  }
}

describe("ReportTab", () => {
  const downloads: string[] = []
  let resolveExport: ((download: ReportDownload) => void) | null = null

  beforeEach(() => {
    downloads.length = 0
    resolveExport = null
    vi.clearAllMocks()

    mocks.useBatches.mockReturnValue(batchesResult([batch(), batch({ id: BATCH_1, batch_no: 1 })]))
    mocks.useReport.mockImplementation((_versionId: string, batchId: string) =>
      reportResult(batchId),
    )
    mocks.exportReport.mockResolvedValue({
      blob: new Blob(["# 报告"], { type: "text/markdown" }),
      filename: "batch-2-report.md",
    })

    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      writable: true,
      value: vi.fn(() => "blob:report"),
    })
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      writable: true,
      value: vi.fn(),
    })
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      downloads.push(this.download)
    })
  })

  afterEach(cleanup)

  it("renders summary cards, metadata and case rows for the newest batch", () => {
    render(<ReportTab versionId={VERSION_ID} />)

    expect(screen.getByTestId("report-tab")).toBeTruthy()
    expect(mocks.useReport).toHaveBeenCalledWith(VERSION_ID, BATCH_2)

    const selector = screen.getByTestId("report-batch-selector") as HTMLSelectElement
    expect(selector.value).toBe(BATCH_2)
    expect(selector.options).toHaveLength(2)

    expect(screen.getByTestId("report-summary-case-count").textContent).toContain("4")
    expect(screen.getByTestId("report-summary-passed").textContent).toContain("2")
    expect(screen.getByTestId("report-summary-failed").textContent).toContain("1")
    expect(screen.getByTestId("report-summary-blocked").textContent).toContain("1")
    expect(screen.getByTestId("report-summary-inconclusive").textContent).toContain("0")
    expect(screen.getByTestId("report-summary-flaky").textContent).toContain("1")
    expect(screen.getByTestId("report-summary-evidence").textContent).toContain("7")
    expect(screen.getByTestId("report-summary-duration").textContent).toContain("2分3秒")
    expect(screen.getByTestId("report-summary-classes").textContent).toContain("应用")
    expect(screen.getByTestId("report-summary-classes").textContent).toContain("环境")

    expect(screen.getByTestId("report-baseline").textContent).toContain(BASELINE_HASH)
    expect(screen.getByTestId("report-commit").textContent).toContain(COMMIT)
    expect(screen.getByTestId("report-environment").textContent).toContain("商城")
    expect(screen.getByTestId("report-environment").textContent).toContain("预发")
    expect(screen.getByTestId("report-environment").textContent).toContain("http://fixture.test")
    expect(screen.getByTestId("report-generated-at").textContent).toContain(GENERATED_AT)

    const passedRow = screen.getByTestId("report-case-row-case-run-1")
    expect(passedRow.textContent).toContain("下单成功")
    expect(passedRow.textContent).toContain("通过")
    expect(passedRow.textContent).toContain("3")
    expect(passedRow.textContent).toContain("2")
    expect(passedRow.textContent).toContain("4")
    expect(passedRow.textContent).toContain("1.2s")

    const failedRow = screen.getByTestId("report-case-row-case-run-2")
    expect(failedRow.textContent).toContain("支付失败")
    expect(failedRow.textContent).toContain("失败")
    expect(failedRow.textContent).toContain("应用")
    expect(failedRow.textContent).toContain("4.6s")
  })

  it("switches the report when another batch is selected", () => {
    render(<ReportTab versionId={VERSION_ID} />)

    fireEvent.change(screen.getByTestId("report-batch-selector"), {
      target: { value: BATCH_1 },
    })

    expect(mocks.useReport).toHaveBeenLastCalledWith(VERSION_ID, BATCH_1)
    expect(screen.getByTestId("report-case-row-case-run-11").textContent).toContain("批量导出")
    expect(screen.queryByTestId("report-case-row-case-run-1")).toBeNull()
  })

  it("exports Markdown through the API and downloads the header filename", async () => {
    render(<ReportTab versionId={VERSION_ID} />)

    fireEvent.click(screen.getByTestId("report-export-btn"))

    await waitFor(() => expect(mocks.exportReport).toHaveBeenCalledTimes(1))
    expect(mocks.exportReport).toHaveBeenCalledWith(VERSION_ID, BATCH_2, {
      fallbackFilename: "execution-report-batch-2.md",
    })
    await waitFor(() => expect(downloads).toEqual(["batch-2-report.md"]))
    expect(URL.revokeObjectURL).toHaveBeenCalledWith("blob:report")
    expect(screen.getByText(/已导出 batch-2-report\.md/)).toBeTruthy()
  })

  it("disables the export button while the download is in flight", async () => {
    mocks.exportReport.mockReturnValue(
      new Promise<ReportDownload>((resolve) => {
        resolveExport = resolve
      }),
    )
    render(<ReportTab versionId={VERSION_ID} />)

    fireEvent.click(screen.getByTestId("report-export-btn"))

    await waitFor(() => {
      expect((screen.getByTestId("report-export-btn") as HTMLButtonElement).disabled).toBe(true)
    })
    expect(screen.getByTestId("report-export-btn").textContent).toContain("导出中")

    resolveExport?.({
      blob: new Blob(["# 报告"], { type: "text/markdown" }),
      filename: "report.md",
    })

    await waitFor(() => {
      expect((screen.getByTestId("report-export-btn") as HTMLButtonElement).disabled).toBe(false)
    })
  })

  it("maps an export failure to the stable reason copy", async () => {
    mocks.exportReport.mockRejectedValue(
      new ApiError({
        status: 500,
        code: "internal",
        message: "export failed",
        details: { reason: "export_failed" },
      }),
    )
    render(<ReportTab versionId={VERSION_ID} />)

    fireEvent.click(screen.getByTestId("report-export-btn"))

    const alert = await screen.findByTestId("report-export-error")
    expect(alert.textContent).toContain("报告导出失败，请稍后重试。")
    expect(downloads).toEqual([])
  })

  it("guides the user to start a batch when none exists", () => {
    mocks.useBatches.mockReturnValue(batchesResult([]))
    render(<ReportTab versionId={VERSION_ID} />)

    expect(screen.getByTestId("report-empty").textContent).toContain("还没有执行批次")
    expect(screen.queryByTestId("report-export-btn")).toBeNull()
  })

  it("shows loading and error states for the batch list", () => {
    mocks.useBatches.mockReturnValue(batchesResult([], { loading: true }))
    const { rerender } = render(<ReportTab versionId={VERSION_ID} />)
    expect(screen.getByTestId("report-loading").textContent).toContain("正在加载批次")

    mocks.useBatches.mockReturnValue(batchesResult([], { error: "无法加载批次" }))
    rerender(<ReportTab versionId={VERSION_ID} />)
    expect(screen.getByRole("alert").textContent).toContain("无法加载批次")
  })

  it("shows the report error and the empty case list", () => {
    mocks.useReport.mockReturnValue({
      report: null,
      loading: false,
      error: "报告加载失败",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    const { rerender } = render(<ReportTab versionId={VERSION_ID} />)
    expect(screen.getByRole("alert").textContent).toContain("报告加载失败")

    mocks.useReport.mockReturnValue({
      report: report({ cases: [] }),
      loading: false,
      error: "",
      streamStatus: "live",
      refresh: vi.fn(),
    })
    rerender(<ReportTab versionId={VERSION_ID} />)
    expect(screen.getByTestId("report-cases-empty").textContent).toContain("没有用例结果")
    expect(screen.getByTestId("report-summary-case-count").textContent).toContain("4")
  })
})

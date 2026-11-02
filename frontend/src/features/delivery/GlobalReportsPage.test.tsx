import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type { GlobalReportView } from "./global-api"
import type { ReportDetailView, ReportRevisionView } from "./types"

const globalApi = vi.hoisted(() => ({
  listGlobalReports: vi.fn(),
}))
vi.mock("./global-api", () => globalApi)

const api = vi.hoisted(() => ({
  getReport: vi.fn(),
  getRevision: vi.fn(),
  downloadHTML: vi.fn(),
}))
vi.mock("./api", () => api)

const navigation = vi.hoisted(() => ({
  push: vi.fn(),
  replace: vi.fn(),
  refresh: vi.fn(),
  back: vi.fn(),
  forward: vi.fn(),
  prefetch: vi.fn(),
}))
const search = vi.hoisted(() => ({ params: new URLSearchParams() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  useSearchParams: () => search.params,
}))

import { GlobalReportsPage } from "./GlobalReportsPage"

/* --------------------------------- fixtures -------------------------------- */

const created = "2026-10-31T10:00:00Z"
const updated = "2026-11-01T06:00:00Z"
const fingerprint = "a".repeat(64)

const revision2: ReportRevisionView = {
  id: "8202",
  report_id: "8101",
  revision: 2,
  status: "published",
  as_of: "2026-11-01T05:30:00Z",
  input_fingerprint: fingerprint,
  template_version: "1",
  render_status: "done",
  html_available: true,
  pdf_available: false,
  created_at: updated,
}

const revision1: ReportRevisionView = {
  ...revision2,
  id: "8201",
  revision: 1,
  status: "draft",
  html_available: false,
  render_status: "failed",
  created_at: created,
}

const versionReport: GlobalReportView = {
  id: "8101",
  version_id: "2001",
  version_title: "结算改版",
  url: "/versions/2001?tab=reports&report=8101",
  wave_id: null,
  kind: "version",
  status: "published",
  input_fingerprint: fingerprint,
  current_revision: revision2,
  created_at: created,
  updated_at: updated,
}

const waveReport: GlobalReportView = {
  id: "8102",
  version_id: "2002",
  version_title: "登录改版",
  url: "/versions/2002?tab=reports&report=8102",
  wave_id: "77",
  kind: "wave",
  status: "stale",
  input_fingerprint: fingerprint,
  current_revision: null,
  created_at: created,
  updated_at: updated,
}

const reportDetail: ReportDetailView = {
  ...versionReport,
  revisions: [revision2, revision1],
  refs: [{ kind: "requirement", ref_id: "7" }],
}

function page(items: GlobalReportView[], nextCursor = "", hasMore = false) {
  return { items, nextCursor, hasMore }
}

function apiError(status: number, message: string): ApiError {
  return new ApiError({ status, code: "unavailable", message })
}

/* ---------------------------------- setup ---------------------------------- */

beforeEach(() => {
  vi.clearAllMocks()
  search.params = new URLSearchParams()
  globalApi.listGlobalReports.mockResolvedValue(page([]))
  api.getReport.mockResolvedValue(reportDetail)
  api.getRevision.mockResolvedValue(revision1)
  api.downloadHTML.mockResolvedValue({
    blob: new Blob(["<html>report</html>"], { type: "text/html" }),
    filename: "report.html",
    contentType: "text/html",
  })
})

afterEach(() => {
  cleanup()
})

/* ---------------------------------- tests ---------------------------------- */

describe("GlobalReportsPage", () => {
  it("renders cross-version reports with current revisions and deep links", async () => {
    globalApi.listGlobalReports.mockResolvedValue(page([versionReport, waveReport]))
    render(<GlobalReportsPage />)

    expect(screen.getByTestId("global-reports-page")).toBeInTheDocument()
    expect(await screen.findByText("结算改版")).toBeInTheDocument()
    expect(screen.getByText("登录改版")).toBeInTheDocument()
    const table = screen.getByRole("table")
    expect(within(table).getByText(/版本总结/)).toBeInTheDocument()
    expect(within(table).getByText(/波次报告/)).toBeInTheDocument()
    expect(within(table).getByText("已发布")).toBeInTheDocument()
    expect(within(table).getByText("已过期")).toBeInTheDocument()
    expect(screen.getByTestId("global-report-revision-8101")).toHaveTextContent("v2")
    expect(screen.getByTestId("global-report-revision-8102")).toHaveTextContent("暂无修订")

    expect(screen.getByTestId("global-report-link-8101")).toHaveAttribute(
      "href",
      "/versions/2001?tab=reports&report=8101",
    )
    expect(screen.getByTestId("global-report-link-8102")).toHaveAttribute(
      "href",
      "/versions/2002?tab=reports&report=8102",
    )
  })

  it("forwards the kind filter and mirrors it in the URL", async () => {
    globalApi.listGlobalReports.mockResolvedValue(page([versionReport]))
    render(<GlobalReportsPage />)
    await screen.findByText("结算改版")

    fireEvent.change(screen.getByLabelText("类型"), { target: { value: "wave" } })

    await waitFor(() => {
      expect(globalApi.listGlobalReports).toHaveBeenLastCalledWith(
        expect.objectContaining({ kind: "wave" }),
      )
    })
    expect(navigation.replace).toHaveBeenLastCalledWith("/reports?kind=wave")
  })

  it("loads the next page with the exclusive before cursor", async () => {
    globalApi.listGlobalReports
      .mockResolvedValueOnce(page([versionReport], "8100", true))
      .mockResolvedValueOnce(page([waveReport], "", false))
    render(<GlobalReportsPage />)

    await screen.findByText("结算改版")
    fireEvent.click(screen.getByTestId("global-reports-load-more"))

    await waitFor(() => {
      expect(globalApi.listGlobalReports).toHaveBeenLastCalledWith(
        expect.objectContaining({ before: "8100" }),
      )
    })
    expect(await screen.findByText("登录改版")).toBeInTheDocument()
    expect(screen.queryByTestId("global-reports-load-more")).not.toBeInTheDocument()
  })

  it("shows the empty state", async () => {
    render(<GlobalReportsPage />)
    expect(await screen.findByText("暂无报告")).toBeInTheDocument()
  })

  it("shows the error state and retries", async () => {
    globalApi.listGlobalReports
      .mockRejectedValueOnce(apiError(503, "global reports unavailable"))
      .mockResolvedValueOnce(page([versionReport]))
    render(<GlobalReportsPage />)

    expect(await screen.findByText("报告列表加载失败")).toBeInTheDocument()
    expect(screen.getByText("global reports unavailable")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("结算改版")).toBeInTheDocument()
  })

  it("opens a read-only report detail without export or write actions", async () => {
    globalApi.listGlobalReports.mockResolvedValue(page([versionReport]))
    render(<GlobalReportsPage />)

    fireEvent.click(await screen.findByTestId("global-report-8101"))
    expect(await screen.findByTestId("report-detail")).toBeInTheDocument()
    expect(api.getReport).toHaveBeenCalledWith("2001", "8101")
    const panel = screen.getByTestId("report-detail")
    expect(within(panel).getByRole("button", { name: "打开报告 HTML" })).toBeInTheDocument()
    for (const label of ["下载 HTML", "重新生成", "PDF（P7）"]) {
      expect(within(panel).queryByRole("button", { name: label })).not.toBeInTheDocument()
    }
  })

  it("selects a historical revision through the version-scoped detail endpoint", async () => {
    globalApi.listGlobalReports.mockResolvedValue(page([versionReport]))
    render(<GlobalReportsPage />)

    fireEvent.click(await screen.findByTestId("global-report-8101"))
    await screen.findByTestId("report-detail")

    fireEvent.change(screen.getByLabelText("修订版本"), { target: { value: "1" } })

    await waitFor(() => {
      expect(api.getRevision).toHaveBeenCalledWith("2001", "8101", 1)
    })
  })

  it("surfaces an HTML open failure without exporting the report", async () => {
    globalApi.listGlobalReports.mockResolvedValue(page([versionReport]))
    api.downloadHTML.mockRejectedValue(apiError(404, "report html is not available"))
    render(<GlobalReportsPage />)

    fireEvent.click(await screen.findByTestId("global-report-8101"))
    await screen.findByTestId("report-detail")
    fireEvent.click(screen.getByRole("button", { name: "打开报告 HTML" }))

    expect(await screen.findByText("report html is not available")).toBeInTheDocument()
  })
})

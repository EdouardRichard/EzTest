import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import type {
  RegenerateReportResult,
  ReportDetailView,
  ReportRevisionView,
  ReportView,
} from "./types"

const api = vi.hoisted(() => ({
  listReports: vi.fn(),
  getReport: vi.fn(),
  getRevision: vi.fn(),
  regenerate: vi.fn(),
  downloadHTML: vi.fn(),
  downloadPDF: vi.fn(),
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

const sse = vi.hoisted(() => ({
  handlers: null as Record<string, (event: unknown) => void> | null,
  calls: 0,
}))
vi.mock("@/features/clarification/useVersionEvents", async () => {
  const React = await import("react")
  return {
    useVersionEvents: (
      _versionId: string,
      handlers: Record<string, (event: unknown) => void>,
    ): string => {
      sse.handlers = handlers
      React.useEffect(() => {
        sse.calls += 1
      }, [])
      return "live"
    },
  }
})

import { ReportsTab } from "./ReportsTab"

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
  template_version: "p4-report-v1",
  render_status: "done",
  html_available: true,
  pdf_available: true,
  pdf_status: "done",
  pdf_error: null,
  pdf_rendered_at: "2026-11-01T05:31:00Z",
  created_at: updated,
}

const revision1: ReportRevisionView = {
  id: "8201",
  report_id: "8101",
  revision: 1,
  status: "published",
  as_of: "2026-10-31T04:00:00Z",
  input_fingerprint: "b".repeat(64),
  template_version: "p4-report-v1",
  render_status: "done",
  html_available: true,
  pdf_available: false,
  pdf_status: "failed",
  pdf_error: "chrome timed out after 60s",
  pdf_rendered_at: null,
  created_at: created,
}

const versionReport: ReportView = {
  id: "8101",
  version_id: "2001",
  wave_id: null,
  kind: "version",
  status: "stale",
  input_fingerprint: fingerprint,
  current_revision: revision2,
  created_at: created,
  updated_at: updated,
}

const waveRevision: ReportRevisionView = {
  ...revision1,
  id: "8203",
  report_id: "8102",
  as_of: "2026-10-31T08:00:00Z",
  pdf_status: "pending",
  pdf_error: null,
}

const waveReport: ReportView = {
  id: "8102",
  version_id: "2001",
  wave_id: "9201",
  kind: "wave",
  status: "published",
  input_fingerprint: "b".repeat(64),
  current_revision: waveRevision,
  created_at: created,
  updated_at: created,
}

const versionDetail: ReportDetailView = {
  ...versionReport,
  revisions: [revision2, revision1],
  refs: [
    { kind: "issue", ref_id: "9101" },
    { kind: "wave", ref_id: "9201" },
  ],
}

const waveDetail: ReportDetailView = {
  ...waveReport,
  revisions: [waveRevision],
  refs: [{ kind: "case_run", ref_id: "9301" }],
}

const regenerated: RegenerateReportResult = {
  report_id: "8101",
  revision: 3,
  work_item_id: "9901",
}

function mockApi(): void {
  api.listReports.mockResolvedValue([versionReport, waveReport])
  api.getReport.mockImplementation((_versionId: string, reportId: string) =>
    reportId === "8101"
      ? Promise.resolve(versionDetail)
      : reportId === "8102"
        ? Promise.resolve(waveDetail)
        : Promise.reject(new ApiError({ status: 404, code: "not_found", message: "报告不存在" })),
  )
  api.getRevision.mockImplementation((_versionId: string, _reportId: string, revision: number) =>
    revision === 1
      ? Promise.resolve(revision1)
      : revision === 2
        ? Promise.resolve(revision2)
        : Promise.reject(
            new ApiError({ status: 404, code: "not_found", message: "revision 不存在" }),
          ),
  )
  api.regenerate.mockResolvedValue(regenerated)
  api.downloadHTML.mockResolvedValue({
    blob: new Blob(["<html></html>"], { type: "text/html" }),
    filename: "report-8101-r2.html",
    contentType: "text/html",
  })
  api.downloadPDF.mockResolvedValue({
    blob: new Blob(["%PDF-1.7"], { type: "application/pdf" }),
    filename: "report-8101-r2.pdf",
    contentType: "application/pdf",
  })
}

function detailPanel(): HTMLElement {
  return screen.getByTestId("report-detail")
}

describe("ReportsTab", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    navigation.replace.mockReset()
    search.params = new URLSearchParams()
    sse.handlers = null
    sse.calls = 0
    mockApi()
  })

  afterEach(() => {
    cleanup()
    Reflect.deleteProperty(URL, "createObjectURL")
    Reflect.deleteProperty(URL, "revokeObjectURL")
  })

  it("shows a loading state while reports load", () => {
    api.listReports.mockReturnValue(new Promise<never>(() => {}))

    render(<ReportsTab versionId="2001" />)

    expect(screen.getByText("正在加载报告…")).toBeInTheDocument()
  })

  it("selects the version summary by default and shows revision, as-of, stale and PDF state", async () => {
    render(<ReportsTab versionId="2001" />)

    expect(await screen.findByTestId("report-8101")).toBeInTheDocument()
    expect(api.listReports).toHaveBeenCalledWith("2001", { limit: 100 })
    expect(api.getReport).toHaveBeenCalledWith("2001", "8101")

    const list = screen.getByTestId("report-list")
    expect(within(list).getByText("版本总结")).toBeInTheDocument()
    expect(within(list).getByText("已过期")).toBeInTheDocument()
    expect(within(list).getByText(/2026-11-01/)).toBeInTheDocument()

    const panel = detailPanel()
    await within(panel).findByText("报告详情")
    expect(within(panel).getByText("v2")).toBeInTheDocument()
    expect(within(panel).getByTestId("report-as-of")).toHaveTextContent("2026-11-01")
    expect(within(panel).getByText("p4-report-v1")).toBeInTheDocument()
    expect(within(panel).getByText(fingerprint.slice(0, 12))).toBeInTheDocument()
    expect(within(panel).getByText("输入已变化，建议重新生成。")).toBeInTheDocument()
    expect(within(panel).getByTestId("report-pdf-status")).toHaveTextContent("PDF 已生成")
    expect(within(panel).getByTestId("report-pdf-rendered-at")).toHaveTextContent("2026-11-01")
    expect(within(panel).getByRole("button", { name: "下载 PDF" })).toBeEnabled()
  })

  it("switches to wave reports and writes ?report= into the URL", async () => {
    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(screen.getByTestId("reports-kind-wave"))
    fireEvent.click(await screen.findByTestId("report-8102"))

    await waitFor(() => expect(api.getReport).toHaveBeenCalledWith("2001", "8102"))
    expect(navigation.replace).toHaveBeenCalledWith("/versions/2001?tab=reports&report=8102")
    expect(await within(detailPanel()).findByRole("heading", { level: 2 })).toHaveTextContent(
      "波次 #9201",
    )
  })

  it("switches revisions, refetches the revision metadata and updates as-of and the URL", async () => {
    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.change(await screen.findByLabelText("修订版本"), { target: { value: "1" } })

    await waitFor(() => expect(api.getRevision).toHaveBeenCalledWith("2001", "8101", 1))
    await waitFor(() =>
      expect(within(detailPanel()).getByText(/2026-10-31/)).toHaveTextContent("2026-10-31"),
    )
    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=reports&report=8101&revision=1",
    )
  })

  it("honours ?report= and ?revision= deep links without rewriting the URL", async () => {
    search.params = new URLSearchParams("tab=reports&report=8101&revision=1")

    render(<ReportsTab versionId="2001" />)

    expect(await screen.findByTestId("report-8101")).toBeInTheDocument()
    await waitFor(() => expect(api.getReport).toHaveBeenCalledWith("2001", "8101"))
    await waitFor(() => expect(api.getRevision).toHaveBeenCalledWith("2001", "8101", 1))
    await waitFor(() =>
      expect(within(detailPanel()).getByText(/2026-10-31/)).toHaveTextContent("2026-10-31"),
    )
    expect(navigation.replace).not.toHaveBeenCalled()
  })

  it("refetches reports and detail on P4 events while keeping the selected revision", async () => {
    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")
    fireEvent.change(await screen.findByLabelText("修订版本"), { target: { value: "1" } })
    await waitFor(() => expect(api.getRevision).toHaveBeenCalledWith("2001", "8101", 1))

    expect(sse.calls).toBe(1)
    expect(sse.handlers).not.toBeNull()
    const listCalls = api.listReports.mock.calls.length
    const detailCalls = api.getReport.mock.calls.length
    const revisionCalls = api.getRevision.mock.calls.length

    act(() => {
      sse.handlers?.onReportChanged?.({ type: "report.stale" })
      sse.handlers?.onIssueChanged?.({ type: "issue.updated" })
      sse.handlers?.onRequirementChanged?.({ type: "requirement.closed" })
      sse.handlers?.onVersionChanged?.({ type: "version.reopened" })
    })

    await waitFor(() => expect(api.listReports).toHaveBeenCalledTimes(listCalls + 1), {
      timeout: 2000,
    })
    await waitFor(() => expect(api.getReport).toHaveBeenCalledTimes(detailCalls + 1), {
      timeout: 2000,
    })
    await waitFor(() => expect(api.getRevision).toHaveBeenCalledTimes(revisionCalls + 1), {
      timeout: 2000,
    })

    // The explicit revision selection is not reset by a live refresh.
    expect(screen.getByLabelText("修订版本")).toHaveValue("1")
    expect(within(detailPanel()).getByText(/2026-10-31/)).toHaveTextContent("2026-10-31")
    expect(navigation.replace).toHaveBeenCalledWith(
      "/versions/2001?tab=reports&report=8101&revision=1",
    )
    expect(sse.calls).toBe(1)
  })

  it("refetches the report list on stream.reset without a new subscription", async () => {
    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")
    const listCalls = api.listReports.mock.calls.length

    act(() => {
      sse.handlers?.onStreamReset?.({ reason: "cursor_too_old", snapshot_cursor: 99 })
    })

    await waitFor(() => expect(api.listReports).toHaveBeenCalledTimes(listCalls + 1))
    expect(sse.calls).toBe(1)
  })

  it("regenerates with a reason, disables duplicate submission and refreshes", async () => {
    let resolveRegenerate: (value: RegenerateReportResult) => void = () => {}
    api.regenerate.mockReturnValue(
      new Promise<RegenerateReportResult>((resolve) => {
        resolveRegenerate = resolve
      }),
    )

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(await within(detailPanel()).findByRole("button", { name: "重新生成" }))
    const dialog = await screen.findByRole("dialog", { name: "重新生成报告" })
    fireEvent.change(within(dialog).getByLabelText("重新生成原因（可选）"), {
      target: { value: "补充失败用例" },
    })
    fireEvent.click(within(dialog).getByRole("button", { name: "重新生成" }))

    await waitFor(() =>
      expect(api.regenerate).toHaveBeenCalledWith("2001", "8101", { reason: "补充失败用例" }),
    )
    expect(within(dialog).getByRole("button", { name: "提交中…" })).toBeDisabled()

    resolveRegenerate(regenerated)

    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "重新生成报告" })).not.toBeInTheDocument()
    })
    await waitFor(() => expect(api.listReports).toHaveBeenCalledTimes(2))
    expect(screen.getByText(/已提交重新生成/)).toBeInTheDocument()
  })

  it("opens the report HTML in a new window through apiDownload", async () => {
    const createObjectURL = vi.fn(() => "blob:report-html")
    const revokeObjectURL = vi.fn()
    Object.assign(URL, { createObjectURL, revokeObjectURL })
    const open = vi.spyOn(window, "open").mockReturnValue({} as Window)

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(await within(detailPanel()).findByRole("button", { name: "打开报告 HTML" }))

    await waitFor(() => expect(api.downloadHTML).toHaveBeenCalledWith("2001", "8101", 2))
    expect(open).toHaveBeenCalledWith("blob:report-html", "_blank", "noopener,noreferrer")
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:report-html")
  })

  it("downloads the report HTML with the server filename", async () => {
    Object.assign(URL, {
      createObjectURL: vi.fn(() => "blob:report-html"),
      revokeObjectURL: vi.fn(),
    })
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(await within(detailPanel()).findByRole("button", { name: "下载 HTML" }))

    await waitFor(() => expect(api.downloadHTML).toHaveBeenCalledWith("2001", "8101", 2))
    const anchor = click.mock.instances[0] as HTMLAnchorElement
    expect(anchor.download).toBe("report-8101-r2.html")
  })

  it("shows an inline error when opening the HTML fails", async () => {
    api.downloadHTML.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "state_conflict",
        message: "报告内容校验失败",
        requestId: "req_report",
      }),
    )

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(await within(detailPanel()).findByRole("button", { name: "打开报告 HTML" }))

    expect(await screen.findByText("报告内容校验失败")).toBeInTheDocument()
  })

  it("downloads the PDF through apiDownload with the server filename", async () => {
    Object.assign(URL, {
      createObjectURL: vi.fn(() => "blob:report-pdf"),
      revokeObjectURL: vi.fn(),
    })
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(await within(detailPanel()).findByRole("button", { name: "下载 PDF" }))

    await waitFor(() => expect(api.downloadPDF).toHaveBeenCalledWith("2001", "8101", 2))
    const anchor = click.mock.instances[0] as HTMLAnchorElement
    expect(anchor.download).toBe("report-8101-r2.pdf")
  })

  it("falls back to a revision PDF filename when Content-Disposition is absent", async () => {
    api.downloadPDF.mockResolvedValue({
      blob: new Blob(["%PDF-1.7"], { type: "application/pdf" }),
      filename: "",
      contentType: "application/pdf",
    })
    Object.assign(URL, {
      createObjectURL: vi.fn(() => "blob:report-pdf"),
      revokeObjectURL: vi.fn(),
    })
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(await within(detailPanel()).findByRole("button", { name: "下载 PDF" }))

    await waitFor(() => expect(api.downloadPDF).toHaveBeenCalledWith("2001", "8101", 2))
    const anchor = click.mock.instances[0] as HTMLAnchorElement
    expect(anchor.download).toBe("report-8101-r2.pdf")
  })

  it("shows the PDF failure reason and disables the PDF download", async () => {
    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.change(await screen.findByLabelText("修订版本"), { target: { value: "1" } })

    await waitFor(() => expect(api.getRevision).toHaveBeenCalledWith("2001", "8101", 1))
    const panel = detailPanel()
    await within(panel).findByText("PDF 生成失败")
    expect(within(panel).getByTestId("report-pdf-error")).toHaveTextContent(
      "chrome timed out after 60s",
    )
    expect(within(panel).getByRole("button", { name: "下载 PDF" })).toBeDisabled()
  })

  it("shows the render_pdf disabled hint when PDF is off", async () => {
    const disabledRevision: ReportRevisionView = {
      ...revision1,
      status: "draft",
      revision: 4,
      pdf_status: "disabled",
      pdf_error: null,
    }
    api.listReports.mockResolvedValue([{ ...versionReport, current_revision: disabledRevision }])
    api.getReport.mockResolvedValue({
      ...versionDetail,
      revisions: [disabledRevision, revision2, revision1],
      current_revision: disabledRevision,
    })
    api.getRevision.mockResolvedValue(disabledRevision)

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    const panel = detailPanel()
    await within(panel).findByText("PDF 未启用")
    expect(within(panel).getByTestId("report-pdf-hint")).toHaveTextContent("report.render_pdf")
    expect(within(panel).getByRole("button", { name: "下载 PDF" })).toBeDisabled()
  })

  it("shows an inline error when the PDF download fails", async () => {
    api.downloadPDF.mockRejectedValue(
      new ApiError({
        status: 409,
        code: "pdf_failed",
        message: "PDF 生成失败，暂不可下载",
        requestId: "req_pdf",
      }),
    )
    Object.assign(URL, {
      createObjectURL: vi.fn(() => "blob:report-pdf"),
      revokeObjectURL: vi.fn(),
    })
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})

    render(<ReportsTab versionId="2001" />)
    await screen.findByTestId("report-8101")

    fireEvent.click(await within(detailPanel()).findByRole("button", { name: "下载 PDF" }))

    expect(await screen.findByText("PDF 生成失败，暂不可下载")).toBeInTheDocument()
  })

  it("shows the empty state when the version has no reports", async () => {
    api.listReports.mockResolvedValue([])

    render(<ReportsTab versionId="2001" />)

    expect(await screen.findByText("暂无报告")).toBeInTheDocument()
    expect(screen.getByText("执行波次或版本收尾后会自动生成报告。")).toBeInTheDocument()
  })

  it("shows a retryable error state when the report list fails", async () => {
    api.listReports
      .mockRejectedValueOnce(
        new ApiError({
          status: 500,
          code: "internal",
          message: "报告列表暂不可用",
          requestId: "req_reports",
        }),
      )
      .mockResolvedValueOnce([versionReport])

    render(<ReportsTab versionId="2001" />)

    expect(await screen.findByText("报告列表加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("report-8101")).toBeInTheDocument()
    expect(api.listReports).toHaveBeenCalledTimes(2)
  })
})

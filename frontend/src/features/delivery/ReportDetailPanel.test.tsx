import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import type { ComponentProps } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

vi.mock("next/navigation", () => ({
  useRouter: () => ({ replace: vi.fn() }),
  useSearchParams: () => new URLSearchParams(),
}))

import { ReportDetailPanel } from "./ReportDetailPanel"
import type { ReportDetailView, ReportExportArtifact, ReportRevisionView } from "./types"

const revision: ReportRevisionView = {
  id: "8201",
  report_id: "8101",
  revision: 2,
  status: "published",
  as_of: "2026-11-01T05:30:00Z",
  input_fingerprint: "a".repeat(64),
  template_version: "2",
  render_status: "done",
  html_available: true,
  pdf_available: false,
  pdf_status: "disabled",
  pdf_error: null,
  pdf_rendered_at: null,
  created_at: "2026-11-01T05:30:00Z",
}

const report: ReportDetailView = {
  id: "8101",
  version_id: "2001",
  wave_id: null,
  kind: "version",
  status: "published",
  input_fingerprint: "a".repeat(64),
  current_revision: revision,
  revisions: [revision],
  refs: [],
  created_at: "2026-11-01T05:30:00Z",
  updated_at: "2026-11-01T05:30:00Z",
}

type PanelProps = ComponentProps<typeof ReportDetailPanel>

function renderPanel(
  nextRevision: Partial<ReportRevisionView>,
  overrides: Partial<PanelProps> = {},
): void {
  const merged = { ...revision, ...nextRevision }
  render(
    <ReportDetailPanel
      versionId="2001"
      report={{ ...report, revisions: [merged], current_revision: merged }}
      revision={merged}
      revisionNumber={merged.revision}
      loading={false}
      error={null}
      onRetry={vi.fn()}
      onRevisionChange={vi.fn()}
      onOpenHTML={vi.fn()}
      onDownloadHTML={vi.fn()}
      onDownloadPDF={vi.fn()}
      onRegenerate={vi.fn()}
      opening={false}
      downloadingPDF={false}
      regenerating={false}
      actionError={null}
      notice={null}
      {...overrides}
    />,
  )
}

describe("ReportDetailPanel PDF state", () => {
  afterEach(cleanup)

  it("shows a pending badge and disables the PDF download", () => {
    renderPanel({ pdf_status: "pending", pdf_available: false })

    expect(screen.getByTestId("report-pdf-status")).toHaveTextContent("PDF 待生成")
    expect(screen.getByTestId("report-pdf-hint")).toHaveTextContent("后台生成")
    expect(screen.getByRole("button", { name: "下载 PDF" })).toBeDisabled()
  })

  it("shows a running badge and disables the PDF download", () => {
    renderPanel({ pdf_status: "running", pdf_available: false })

    expect(screen.getByTestId("report-pdf-status")).toHaveTextContent("PDF 生成中")
    expect(screen.getByRole("button", { name: "下载 PDF" })).toBeDisabled()
  })

  it("shows the done badge, rendered time and enables the download", () => {
    renderPanel({
      pdf_status: "done",
      pdf_available: true,
      pdf_rendered_at: "2026-11-01T06:00:00Z",
    })

    expect(screen.getByTestId("report-pdf-status")).toHaveTextContent("PDF 已生成")
    expect(screen.getByTestId("report-pdf-rendered-at")).toHaveTextContent("2026-11-01")
    expect(screen.getByRole("button", { name: "下载 PDF" })).toBeEnabled()
  })

  it("shows the failure reason and keeps the download disabled", () => {
    renderPanel({ pdf_status: "failed", pdf_error: "renderer unavailable" })

    expect(screen.getByTestId("report-pdf-status")).toHaveTextContent("PDF 生成失败")
    expect(screen.getByTestId("report-pdf-error")).toHaveTextContent("renderer unavailable")
    expect(screen.getByTestId("report-pdf-hint")).toHaveTextContent("自动重试")
    expect(screen.getByRole("button", { name: "下载 PDF" })).toBeDisabled()
  })

  it("explains that PDF is disabled by the render_pdf setting", () => {
    renderPanel({ pdf_status: "disabled", pdf_available: false })

    expect(screen.getByTestId("report-pdf-status")).toHaveTextContent("PDF 未启用")
    expect(screen.getByTestId("report-pdf-hint")).toHaveTextContent("report.render_pdf")
    expect(screen.getByRole("button", { name: "下载 PDF" })).toBeDisabled()
  })

  it("keeps HTML actions usable while PDF is disabled", () => {
    renderPanel({ pdf_status: "disabled", html_available: true })

    const panel = screen.getByTestId("report-detail")
    expect(within(panel).getByRole("button", { name: "打开报告 HTML" })).toBeEnabled()
    expect(within(panel).getByRole("button", { name: "下载 HTML" })).toBeEnabled()
  })
})

const artifact: ReportExportArtifact = {
  id: "9301",
  version_id: "2001",
  owner_kind: "report",
  owner_id: "8101",
  revision: 2,
  format: "markdown",
  size: 1234,
  mime: "text/markdown; charset=utf-8",
  filename: "report-8101-r2.md",
  sha256: "c".repeat(64),
  status: "ready",
  created_at: "2026-11-01T05:30:00Z",
  download_url: "/api/exports/9301/download",
}

describe("ReportDetailPanel export artifacts", () => {
  afterEach(cleanup)

  it("exports HTML/PDF/Markdown and lists artifacts with sha256 and size", () => {
    const onExport = vi.fn()
    const onDownloadArtifact = vi.fn()
    renderPanel(
      { pdf_status: "done", pdf_available: true },
      {
        artifacts: [artifact],
        artifactsLoading: false,
        artifactsError: null,
        exportingFormat: null,
        onExport,
        onDownloadArtifact,
      },
    )

    expect(screen.getByText("artifacts")).toBeInTheDocument()
    expect(screen.getByTestId("report-artifact-9301")).toHaveTextContent("sha256")
    expect(screen.getByTestId("report-artifact-9301")).toHaveTextContent(
      artifact.sha256.slice(0, 12),
    )
    expect(screen.getByTestId("report-artifact-9301")).toHaveTextContent("1.2 KB")

    fireEvent.click(screen.getByRole("button", { name: "HTML" }))
    fireEvent.click(screen.getByRole("button", { name: "PDF" }))
    fireEvent.click(screen.getByRole("button", { name: "Markdown" }))

    expect(onExport.mock.calls).toEqual([["html"], ["pdf"], ["markdown"]])
    fireEvent.click(screen.getByRole("button", { name: "下载 artifact 9301" }))
    expect(onDownloadArtifact).toHaveBeenCalledWith(artifact)
  })

  it("disables export while a historical revision is selected", () => {
    renderPanel({}, { revisionNumber: 1 })

    expect(screen.getByRole("button", { name: "HTML" })).toBeDisabled()
    expect(screen.getByRole("button", { name: "Markdown" })).toBeDisabled()
  })

  it("disables the PDF export until the current PDF is ready", () => {
    renderPanel({ pdf_status: "pending", pdf_available: false })

    expect(screen.getByRole("button", { name: "HTML" })).toBeEnabled()
    expect(screen.getByRole("button", { name: "Markdown" })).toBeEnabled()
    expect(screen.getByRole("button", { name: "PDF" })).toBeDisabled()
  })

  it("hides export and artifact UI in read-only mode", () => {
    renderPanel({}, { readOnly: true, artifacts: [artifact] })

    expect(screen.queryByText("artifacts")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "HTML" })).not.toBeInTheDocument()
  })
})

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  createExport: vi.fn(),
  downloadExport: vi.fn(),
}))

vi.mock("./api", () => api)

const download = vi.hoisted(() => ({ saveDownload: vi.fn() }))
vi.mock("./download", () => download)

import { ExportCasesDialog } from "./ExportCasesDialog"
import { exportViewFixture } from "./test-fixtures"

const DOWNLOAD = {
  blob: new Blob(["# 用例设计"]),
  filename: exportViewFixture.filename,
  contentType: exportViewFixture.mime,
}

function renderDialog(overrides: Partial<Parameters<typeof ExportCasesDialog>[0]> = {}) {
  const props = {
    versionId: "2001",
    open: true,
    onClose: vi.fn(),
    onExported: vi.fn(),
    ...overrides,
  }
  render(<ExportCasesDialog {...props} />)
  return props
}

describe("ExportCasesDialog", () => {
  beforeEach(() => {
    api.createExport.mockReset()
    api.downloadExport.mockReset()
    download.saveDownload.mockReset()
    api.createExport.mockResolvedValue(exportViewFixture)
    api.downloadExport.mockResolvedValue(DOWNLOAD)
  })

  afterEach(cleanup)

  it("renders nothing while closed", () => {
    renderDialog({ open: false })
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("offers the four export formats for the whole version", () => {
    renderDialog()

    expect(screen.getByRole("dialog", { name: "导出用例" })).toBeInTheDocument()
    expect(screen.getByText(/整版本/)).toBeInTheDocument()
    for (const format of ["markdown", "mermaid", "opml", "csv"]) {
      expect(screen.getByTestId(`export-format-${format}`)).toBeInTheDocument()
    }
    expect(screen.queryByText("执行结果")).not.toBeInTheDocument()
  })

  it.each([
    ["markdown", "Markdown"],
    ["mermaid", "Mermaid"],
    ["opml", "OPML"],
    ["csv", "CSV"],
  ] as const)("creates and downloads a %s export", async (format, label) => {
    const props = renderDialog()

    fireEvent.click(screen.getByRole("button", { name: label }))

    await waitFor(() =>
      expect(api.createExport).toHaveBeenCalledWith("2001", {
        owner_kind: "case",
        owner_id: "2001",
        format,
      }),
    )
    await waitFor(() => expect(api.downloadExport).toHaveBeenCalledWith("9001"))
    expect(download.saveDownload).toHaveBeenCalledWith(DOWNLOAD, exportViewFixture.filename)
    expect(await screen.findByTestId("export-status")).toHaveTextContent(
      "已导出 case-export-9001.md",
    )
    expect(props.onExported).toHaveBeenCalledWith(exportViewFixture)
  })

  it("shows the failure inline and retries with the same format", async () => {
    api.createExport
      .mockRejectedValueOnce(
        new ApiError({ status: 500, code: "internal", message: "导出生成失败" }),
      )
      .mockResolvedValueOnce(exportViewFixture)
    renderDialog()

    fireEvent.click(screen.getByRole("button", { name: "OPML" }))

    expect(await screen.findByText("导出生成失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    await waitFor(() => expect(api.createExport).toHaveBeenCalledTimes(2))
    expect(api.createExport).toHaveBeenLastCalledWith("2001", {
      owner_kind: "case",
      owner_id: "2001",
      format: "opml",
    })
    expect(await screen.findByTestId("export-status")).toBeInTheDocument()
  })

  it("closes through the dialog close button", () => {
    const props = renderDialog()

    fireEvent.click(screen.getByRole("button", { name: "关闭" }))

    expect(props.onClose).toHaveBeenCalledTimes(1)
  })
})

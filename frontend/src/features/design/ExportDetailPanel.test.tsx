import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({
  getExport: vi.fn(),
  downloadExport: vi.fn(),
}))
vi.mock("./api", () => api)

const download = vi.hoisted(() => ({ saveDownload: vi.fn() }))
vi.mock("./download", () => download)

import { ExportDetailPanel } from "./ExportDetailPanel"
import type { ExportView } from "./types"

const exportView: ExportView = {
  id: "9001",
  version_id: "2001",
  owner_kind: "case",
  owner_id: "2001",
  revision: 3,
  format: "markdown",
  size: 2048,
  mime: "text/markdown",
  filename: "cases-r3.md",
  sha256: "a".repeat(64),
  status: "ready",
  created_at: "2026-11-01T08:00:00Z",
  download_url: "/api/exports/9001/download",
}

describe("ExportDetailPanel", () => {
  beforeEach(() => {
    api.getExport.mockReset()
    api.downloadExport.mockReset()
    download.saveDownload.mockReset()
  })

  afterEach(cleanup)

  it("renders the export metadata and its design deep link", async () => {
    api.getExport.mockResolvedValue(exportView)

    render(<ExportDetailPanel exportId="9001" />)

    expect(await screen.findByText("cases-r3.md")).toBeInTheDocument()
    expect(screen.getByText("markdown")).toBeInTheDocument()
    expect(screen.getByText("r3")).toBeInTheDocument()
    expect(screen.getByText("2.0 KB")).toBeInTheDocument()
    expect(screen.getByText("text/markdown")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: /在设计中打开/ })).toHaveAttribute(
      "href",
      expect.stringContaining("/versions/2001?tab=design"),
    )
    expect(api.getExport).toHaveBeenCalledWith("9001")
  })

  it("downloads the export through the authenticated api-client", async () => {
    api.getExport.mockResolvedValue(exportView)
    api.downloadExport.mockResolvedValue({
      blob: new Blob(["cases"]),
      filename: "cases-r3.md",
      contentType: "text/markdown",
    })

    render(<ExportDetailPanel exportId="9001" />)

    fireEvent.click(await screen.findByRole("button", { name: "下载导出" }))

    await waitFor(() => expect(api.downloadExport).toHaveBeenCalledWith("9001"))
    expect(download.saveDownload).toHaveBeenCalledWith(
      expect.objectContaining({ filename: "cases-r3.md" }),
      "cases-r3.md",
    )
  })

  it("shows the load error and retries the export read", async () => {
    api.getExport.mockRejectedValueOnce(new Error("导出不存在")).mockResolvedValueOnce(exportView)

    render(<ExportDetailPanel exportId="9001" />)

    expect(await screen.findByText("导出不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("cases-r3.md")).toBeInTheDocument()
    expect(api.getExport).toHaveBeenCalledTimes(2)
  })
})

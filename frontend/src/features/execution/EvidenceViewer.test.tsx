import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ downloadEvidence: vi.fn() }))
vi.mock("./api", () => api)

import { EvidenceViewer } from "./EvidenceViewer"
import { evidenceScreenshotFixture, evidenceTextFixture } from "./test-fixtures"

function download(blob: Blob, filename: string) {
  return {
    blob,
    filename,
    contentType: blob.type,
  }
}

describe("EvidenceViewer", () => {
  const createObjectURL = vi.fn<(blob: Blob) => string>()
  const revokeObjectURL = vi.fn<(url: string) => void>()

  beforeEach(() => {
    api.downloadEvidence.mockReset()
    window.sessionStorage.clear()
    createObjectURL.mockReset()
    revokeObjectURL.mockReset()
    createObjectURL.mockReturnValue("blob:evidence-9401")
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      writable: true,
      value: createObjectURL,
    })
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      writable: true,
      value: revokeObjectURL,
    })
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it("shows screenshot bytes through an object URL and revokes it on close", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "step-3.png"),
    )

    const { rerender } = render(
      <EvidenceViewer
        versionId="2001"
        evidence={evidenceScreenshotFixture}
        open
        onClose={() => {}}
      />,
    )

    const image = await screen.findByTestId("evidence-image")
    expect(image).toHaveAttribute("src", "blob:evidence-9401")
    expect(api.downloadEvidence).toHaveBeenCalledWith("2001", "9401")
    expect(screen.getByText("不可变证据")).toBeInTheDocument()
    expect(screen.getByText(`sha256: ${evidenceScreenshotFixture.sha256}`)).toBeInTheDocument()
    expect(screen.getByText("大小 20.0 KB")).toBeInTheDocument()

    rerender(
      <EvidenceViewer
        versionId="2001"
        evidence={evidenceScreenshotFixture}
        open={false}
        onClose={() => {}}
      />,
    )

    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:evidence-9401"))
  })

  it("renders text evidence inline", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["PASS login as admin"], { type: "text/plain" }), "console.log"),
    )

    render(
      <EvidenceViewer versionId="2001" evidence={evidenceTextFixture} open onClose={() => {}} />,
    )

    expect(await screen.findByTestId("evidence-text")).toHaveTextContent("PASS login as admin")
    expect(screen.getByText("文本 · text/plain")).toBeInTheDocument()
  })

  it("shows a retryable error state when the evidence download fails", async () => {
    api.downloadEvidence
      .mockRejectedValueOnce(
        new ApiError({
          status: 404,
          code: "not_found",
          message: "证据不存在",
          requestId: "req_ev",
        }),
      )
      .mockResolvedValueOnce(download(new Blob(["ok"], { type: "text/plain" }), "note.txt"))

    render(
      <EvidenceViewer versionId="2001" evidence={evidenceTextFixture} open onClose={() => {}} />,
    )

    expect(await screen.findByText("证据不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByTestId("evidence-text")).toHaveTextContent("ok")
    expect(api.downloadEvidence).toHaveBeenCalledTimes(2)
  })

  it("re-downloads through apiDownload when the download button is used", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "step-3.png"),
    )
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={evidenceScreenshotFixture}
        open
        onClose={() => {}}
      />,
    )

    await screen.findByTestId("evidence-image")
    fireEvent.click(screen.getByRole("button", { name: "下载" }))

    await waitFor(() => expect(api.downloadEvidence).toHaveBeenCalledTimes(2))
    expect(clickSpy).toHaveBeenCalledTimes(1)
  })
})

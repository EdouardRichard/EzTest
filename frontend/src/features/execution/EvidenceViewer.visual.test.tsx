import { cleanup, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({ downloadEvidence: vi.fn() }))
vi.mock("./api", () => api)

import { EvidenceViewer } from "./EvidenceViewer"
import { evidenceScreenshotFixture } from "./test-fixtures"

describe("EvidenceViewer visual kinds", () => {
  const createObjectURL = vi.fn<(blob: Blob) => string>()
  const revokeObjectURL = vi.fn<(url: string) => void>()

  beforeEach(() => {
    api.downloadEvidence.mockReset()
    createObjectURL.mockReset()
    revokeObjectURL.mockReset()
    createObjectURL.mockReturnValue("blob:visual-evidence")
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

  it("renders visual_diff evidence as an image with the visual label", async () => {
    api.downloadEvidence.mockResolvedValue({
      blob: new Blob(["png"], { type: "image/png" }),
      filename: "diff.png",
      contentType: "image/png",
    })

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={{
          ...evidenceScreenshotFixture,
          id: "9402",
          kind: "visual_diff",
          filename: "diff.png",
        }}
        open
        onClose={() => {}}
      />,
    )

    expect(await screen.findByTestId("evidence-image")).toHaveAttribute(
      "src",
      "blob:visual-evidence",
    )
    expect(screen.getAllByText("视觉差异图").length).toBeGreaterThan(0)
    expect(api.downloadEvidence).toHaveBeenCalledWith("2001", "9402")
  })
})

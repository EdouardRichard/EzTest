import { cleanup, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const api = vi.hoisted(() => ({ downloadEvidence: vi.fn() }))
vi.mock("./api", () => api)

import { EvidenceViewer } from "./EvidenceViewer"
import { evidenceScreenshotFixture } from "./test-fixtures"
import type { Evidence } from "./types"

const earlierActual: Evidence = {
  ...evidenceScreenshotFixture,
  id: "9402",
  kind: "visual_actual",
  filename: "actual-before.png",
  created_at: "2026-10-31T12:00:00Z",
}

const laterActual: Evidence = {
  ...earlierActual,
  id: "9403",
  filename: "actual-after.png",
  created_at: "2026-10-31T12:00:30Z",
}

const differentStepActual: Evidence = {
  ...earlierActual,
  id: "9404",
  step_seq: 1,
  filename: "other-step.png",
}

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
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    cleanup()
    vi.clearAllTimers()
    vi.useRealTimers()
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

  it("pairs before/after screenshots of the same case run and step", async () => {
    api.downloadEvidence.mockResolvedValue({
      blob: new Blob(["png"], { type: "image/png" }),
      filename: "actual.png",
      contentType: "image/png",
    })
    createObjectURL
      .mockReturnValueOnce("blob:visual-before")
      .mockReturnValueOnce("blob:visual-after")

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={earlierActual}
        candidates={[earlierActual, laterActual, differentStepActual]}
        open
        onClose={() => {}}
      />,
    )

    const compare = await screen.findByTestId("evidence-compare")
    expect(compare).toHaveTextContent("前后对比")
    expect(within(compare).getByText("之前")).toBeInTheDocument()
    expect(within(compare).getByText("之后")).toBeInTheDocument()

    const before = within(screen.getByTestId("evidence-compare-before")).getByRole("img")
    const after = within(screen.getByTestId("evidence-compare-after")).getByRole("img")
    expect(before).toHaveAttribute("src", "blob:visual-before")
    expect(after).toHaveAttribute("src", "blob:visual-after")
    expect(before.className).toContain("object-contain")
    expect(after.className).toContain("object-contain")

    // Only the before/after pair is downloaded a second time; the different
    // step screenshot is never paired.
    expect(api.downloadEvidence).toHaveBeenCalledWith("2001", "9403")
    expect(api.downloadEvidence).not.toHaveBeenCalledWith("2001", "9404")
  })

  it("does not compare evidence from a different step", async () => {
    api.downloadEvidence.mockResolvedValue({
      blob: new Blob(["png"], { type: "image/png" }),
      filename: "actual.png",
      contentType: "image/png",
    })

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={earlierActual}
        candidates={[differentStepActual]}
        open
        onClose={() => {}}
      />,
    )

    await screen.findByTestId("evidence-image")
    expect(screen.queryByTestId("evidence-compare")).not.toBeInTheDocument()
  })
})

import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { EvidenceMeta, EvidenceView } from "../types"

const mocks = vi.hoisted(() => ({
  getEvidenceMeta: vi.fn(),
  getEvidenceUrl: vi.fn(),
  apiStreamFetch: vi.fn(),
}))

vi.mock("../api", () => ({
  getEvidenceMeta: mocks.getEvidenceMeta,
  getEvidenceUrl: mocks.getEvidenceUrl,
}))

vi.mock("@/lib/api-client", () => ({
  apiStreamFetch: mocks.apiStreamFetch,
  ApiError: class ApiError extends Error {
    readonly status: number
    readonly requestId = ""
    constructor(init: { status: number; message: string }) {
      super(init.message)
      this.name = "ApiError"
      this.status = init.status
    }
  },
}))

import { EvidenceViewer } from "./EvidenceViewer"

/** Snowflake identifiers stay decimal strings end-to-end (spec §6.1). */
const VERSION_ID = "1844674407370955161"
const EVIDENCE_ID = "1844674407370955402"
const COMPARE_ID = "1844674407370955403"

let objectUrlCount = 0
const createObjectURL = vi.fn<(blob: Blob) => string>(() => {
  objectUrlCount += 1
  return `blob:evidence-${objectUrlCount}`
})
const revokeObjectURL = vi.fn()

function evidenceMeta(overrides: Partial<EvidenceMeta> = {}): EvidenceMeta {
  return {
    id: EVIDENCE_ID,
    kind: "screenshot",
    mime: "image/png",
    size_bytes: 2048,
    sha256: "a".repeat(64),
    metadata: {},
    created_at: "2026-10-28T10:00:00Z",
    ...overrides,
  }
}

function evidence(overrides: Partial<EvidenceView> = {}): EvidenceView {
  return {
    id: EVIDENCE_ID,
    kind: "screenshot",
    mime: "image/png",
    size_bytes: 2048,
    sha256: "a".repeat(64),
    ...overrides,
  }
}

/** Minimal stand-in for the blob half the viewer consumes. */
function binaryResponse(bytes: string, type: string): Response {
  const blob = new Blob([bytes], { type })
  return {
    ok: true,
    status: 200,
    headers: new Headers({ "content-type": type }),
    blob: async () => blob,
    text: async () => bytes,
  } as unknown as Response
}

type ViewerProps = {
  versionId: string
  evidenceId: string
  compareWith?: string
  onClose: () => void
}

function Viewer(props: ViewerProps) {
  const { onClose, ...rest } = props
  return <EvidenceViewer {...rest} evidence={evidence()} onClose={onClose} />
}

function renderViewer(onClose: () => void = vi.fn()) {
  return render(
    <Viewer
      versionId={VERSION_ID}
      evidenceId={EVIDENCE_ID}
      compareWith={COMPARE_ID}
      onClose={onClose}
    />,
  )
}

/** Controls the viewer mount so Esc can be observed together with focus return. */
function EscapeHarness() {
  const [open, setOpen] = useState(false)
  return (
    <div>
      <button type="button" data-testid="opener" onClick={() => setOpen(true)}>
        打开证据
      </button>
      {open ? (
        <Viewer
          versionId={VERSION_ID}
          evidenceId={EVIDENCE_ID}
          compareWith={COMPARE_ID}
          onClose={() => setOpen(false)}
        />
      ) : null}
    </div>
  )
}

describe("EvidenceViewer comparison", () => {
  beforeEach(() => {
    mocks.getEvidenceMeta.mockReset()
    mocks.getEvidenceUrl.mockReset()
    mocks.apiStreamFetch.mockReset()

    mocks.getEvidenceMeta.mockImplementation(async (_versionId: string, evidenceId: string) =>
      evidenceId === COMPARE_ID
        ? evidenceMeta({ id: COMPARE_ID })
        : evidenceMeta({ id: EVIDENCE_ID }),
    )
    mocks.getEvidenceUrl.mockImplementation(
      (_versionId: string, evidenceId: string) =>
        `http://api.test/api/versions/${VERSION_ID}/evidence/${evidenceId}`,
    )
    mocks.apiStreamFetch.mockResolvedValue(binaryResponse("png", "image/png"))

    objectUrlCount = 0
    createObjectURL.mockClear()
    revokeObjectURL.mockReset()
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL })
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL })
  })

  afterEach(cleanup)

  it("renders both screenshots side by side with highlighted difference borders", async () => {
    renderViewer()

    const compare = await screen.findByTestId("evidence-compare")
    expect(compare.getAttribute("data-mode")).toBe("side-by-side")
    expect(screen.getByRole("button", { name: "并排" }).getAttribute("aria-pressed")).toBe("true")

    const primary = await screen.findByTestId("evidence-compare-image-primary")
    const other = await screen.findByTestId("evidence-compare-image-compare")
    expect(primary.getAttribute("src")).toBeTruthy()
    expect(other.getAttribute("src")).toBeTruthy()
    expect(primary.getAttribute("src")).not.toBe(other.getAttribute("src"))

    // Differences are signalled by a high-contrast border on both sides; no
    // pixel-level diff is computed in the browser (spec §8.3).
    expect(
      screen.getByTestId("evidence-compare-pane-primary").getAttribute("data-diff-highlight"),
    ).toBe("true")
    expect(
      screen.getByTestId("evidence-compare-pane-compare").getAttribute("data-diff-highlight"),
    ).toBe("true")

    // Both artifacts go through the bearer-aware bytes endpoint.
    expect(mocks.getEvidenceMeta).toHaveBeenCalledWith(VERSION_ID, EVIDENCE_ID, expect.anything())
    expect(mocks.getEvidenceMeta).toHaveBeenCalledWith(VERSION_ID, COMPARE_ID, expect.anything())
    expect(mocks.apiStreamFetch).toHaveBeenCalledTimes(2)
  })

  it("switches to the slider mode and moves the divider", async () => {
    renderViewer()

    await screen.findByTestId("evidence-compare-image-compare")
    fireEvent.click(screen.getByRole("button", { name: "滑块" }))

    expect(screen.getByTestId("evidence-compare").getAttribute("data-mode")).toBe("slider")
    const slider = screen.getByRole("slider", { name: "对比滑块" }) as HTMLInputElement
    expect(slider.value).toBe("50")

    fireEvent.change(slider, { target: { value: "25" } })
    expect(slider.value).toBe("25")
    expect(screen.getByTestId("evidence-compare-image-slider").style.clipPath).toContain("75")

    fireEvent.click(screen.getByRole("button", { name: "并排" }))
    expect(screen.getByTestId("evidence-compare").getAttribute("data-mode")).toBe("side-by-side")
  })

  it("closes on Escape and returns focus to the opener", async () => {
    render(<EscapeHarness />)
    const opener = screen.getByTestId("opener")
    opener.focus()
    fireEvent.click(opener)

    const dialog = await screen.findByTestId("evidence-viewer")
    await screen.findByTestId("evidence-compare-image-compare")

    fireEvent.keyDown(dialog, { key: "Escape" })
    expect(screen.queryByTestId("evidence-viewer")).toBeNull()
    expect(document.activeElement).toBe(opener)
  })

  it("keeps the primary evidence visible when the comparison artifact fails to load", async () => {
    mocks.getEvidenceMeta.mockImplementation(async (_versionId: string, evidenceId: string) => {
      if (evidenceId === COMPARE_ID) throw new Error("compare-boom")
      return evidenceMeta({ id: EVIDENCE_ID })
    })

    renderViewer()

    // The primary lightbox still works; the failed comparison degrades to a
    // retryable alert instead of replacing the whole viewer.
    expect(await screen.findByTestId("evidence-screenshot")).toBeTruthy()
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载对比证据失败")
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy()
  })
})

import { cleanup, render, screen } from "@testing-library/react"
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

import { EvidenceViewer, evidenceKindLabel, isImageEvidenceKind } from "./EvidenceViewer"

const VERSION_ID = "1844674407370955161"
const ACTUAL_ID = "1844674407370955402"
const DIFF_ID = "1844674407370955403"

let objectUrlCount = 0
const createObjectURL = vi.fn<(blob: Blob) => string>(() => {
  objectUrlCount += 1
  return `blob:visual-${objectUrlCount}`
})
const revokeObjectURL = vi.fn()

function evidenceMeta(overrides: Partial<EvidenceMeta> = {}): EvidenceMeta {
  return {
    id: ACTUAL_ID,
    kind: "visual_actual",
    mime: "image/png",
    size_bytes: 2048,
    sha256: "a".repeat(64),
    metadata: {},
    created_at: "2026-10-29T09:00:00Z",
    ...overrides,
  }
}

function evidence(overrides: Partial<EvidenceView> = {}): EvidenceView {
  return {
    id: ACTUAL_ID,
    kind: "visual_actual",
    mime: "image/png",
    size_bytes: 2048,
    sha256: "a".repeat(64),
    ...overrides,
  }
}

function binaryResponse(bytes: string): Response {
  const blob = new Blob([bytes], { type: "image/png" })
  return {
    ok: true,
    status: 200,
    headers: new Headers({ "content-type": "image/png" }),
    blob: async () => blob,
    text: async () => bytes,
  } as unknown as Response
}

function renderViewer(evidenceId: string, compareWith = "") {
  return render(
    <EvidenceViewer
      versionId={VERSION_ID}
      evidenceId={evidenceId}
      evidence={evidence()}
      compareWith={compareWith}
      onClose={vi.fn()}
    />,
  )
}

describe("EvidenceViewer visual evidence", () => {
  beforeEach(() => {
    mocks.getEvidenceMeta.mockReset()
    mocks.getEvidenceUrl.mockReset()
    mocks.apiStreamFetch.mockReset()
    mocks.getEvidenceMeta.mockImplementation(async (_versionId: string, evidenceId: string) =>
      evidenceId === DIFF_ID
        ? evidenceMeta({ id: DIFF_ID, kind: "visual_diff" })
        : evidenceMeta({ id: ACTUAL_ID, kind: "visual_actual" }),
    )
    mocks.getEvidenceUrl.mockImplementation(
      (_versionId: string, evidenceId: string) =>
        `http://api.test/api/versions/${VERSION_ID}/evidence/${evidenceId}`,
    )
    mocks.apiStreamFetch.mockResolvedValue(binaryResponse("png"))
    objectUrlCount = 0
    createObjectURL.mockClear()
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL })
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL })
  })

  afterEach(cleanup)

  it("labels the visual evidence kinds and treats them as images", () => {
    expect(evidenceKindLabel("visual_baseline")).toBe("视觉基线")
    expect(evidenceKindLabel("visual_actual")).toBe("视觉实际截图")
    expect(evidenceKindLabel("visual_diff")).toBe("视觉差异图")
    expect(isImageEvidenceKind("visual_actual")).toBe(true)
    expect(isImageEvidenceKind("visual_diff")).toBe(true)
    expect(isImageEvidenceKind("visual_baseline")).toBe(true)
    expect(isImageEvidenceKind("http_exchange")).toBe(false)
  })

  it("renders an actual screenshot as an image", async () => {
    renderViewer(ACTUAL_ID)
    const image = await screen.findByTestId("evidence-visual-image")
    expect(image.getAttribute("data-visual-kind")).toBe("visual_actual")
    expect(image.getAttribute("src")).toBeTruthy()
  })

  it("renders the diff image with its explanation", async () => {
    renderViewer(DIFF_ID)
    const image = await screen.findByTestId("evidence-visual-image")
    expect(image.getAttribute("data-visual-kind")).toBe("visual_diff")
    expect(screen.getByTestId("evidence-visual-diff").textContent).toContain("差异图")
  })

  it("compares the actual screenshot with the diff image side by side", async () => {
    renderViewer(ACTUAL_ID, DIFF_ID)
    const compare = await screen.findByTestId("evidence-compare")
    expect(compare.getAttribute("data-mode")).toBe("side-by-side")
    expect(await screen.findByTestId("evidence-compare-image-primary")).toBeTruthy()
    expect(await screen.findByTestId("evidence-compare-image-compare")).toBeTruthy()
    expect(mocks.getEvidenceMeta).toHaveBeenCalledWith(VERSION_ID, ACTUAL_ID, expect.anything())
    expect(mocks.getEvidenceMeta).toHaveBeenCalledWith(VERSION_ID, DIFF_ID, expect.anything())
  })
})

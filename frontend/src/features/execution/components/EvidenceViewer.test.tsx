import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
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
const EVIDENCE_ID = "1844674407370955162"
const EVIDENCE_URL = `http://api.test/api/versions/${VERSION_ID}/evidence/${EVIDENCE_ID}`

const createObjectURL = vi.fn<(blob: Blob) => string>(() => "blob:evidence")
const revokeObjectURL = vi.fn()
const writeText = vi.fn()

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

/** Minimal stand-in for the blob/text halves the viewer consumes. */
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

function jsonResponse(payload: unknown): Response {
  const text = JSON.stringify(payload)
  return {
    ok: true,
    status: 200,
    headers: new Headers({ "content-type": "application/json" }),
    blob: async () => new Blob([text], { type: "application/json" }),
    text: async () => text,
  } as unknown as Response
}

function renderViewer(onClose: () => void = vi.fn(), initial = evidence()) {
  return render(
    <EvidenceViewer
      versionId={VERSION_ID}
      evidenceId={EVIDENCE_ID}
      evidence={initial}
      onClose={onClose}
    />,
  )
}

describe("EvidenceViewer", () => {
  beforeEach(() => {
    mocks.getEvidenceMeta.mockReset()
    mocks.getEvidenceUrl.mockReset()
    mocks.apiStreamFetch.mockReset()
    mocks.getEvidenceMeta.mockResolvedValue(evidenceMeta())
    mocks.getEvidenceUrl.mockReturnValue(EVIDENCE_URL)
    mocks.apiStreamFetch.mockResolvedValue(binaryResponse("png", "image/png"))

    createObjectURL.mockReset()
    createObjectURL.mockReturnValue("blob:evidence")
    revokeObjectURL.mockReset()
    writeText.mockReset()
    writeText.mockResolvedValue(undefined)

    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL })
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL })
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    })
  })

  afterEach(cleanup)

  it("renders a screenshot lightbox through a bearer blob fetch", async () => {
    renderViewer()

    const dialog = screen.getByTestId("evidence-viewer")
    expect(dialog.getAttribute("role")).toBe("dialog")
    expect(dialog.getAttribute("aria-modal")).toBe("true")

    const image = await screen.findByTestId("evidence-screenshot")
    expect(image.getAttribute("src")).toBe("blob:evidence")
    expect(image.getAttribute("alt")).toContain("截图")

    // The bytes endpoint needs the bearer, so the viewer must go through the
    // API client instead of pointing <img> at the bare URL helper.
    expect(mocks.apiStreamFetch).toHaveBeenCalledWith(EVIDENCE_URL, expect.anything())
    expect(createObjectURL).toHaveBeenCalledTimes(1)
    const capturedBlob = createObjectURL.mock.calls[0]?.[0] as Blob | undefined
    expect(capturedBlob?.size ?? 0).toBeGreaterThan(0)
  })

  it("renders http exchanges as request/response panes and copies curl", async () => {
    mocks.getEvidenceMeta.mockResolvedValue(
      evidenceMeta({ kind: "http_exchange", mime: "application/json" }),
    )
    mocks.apiStreamFetch.mockResolvedValue(
      jsonResponse({
        request: {
          method: "POST",
          url: "https://shop.test/api/orders",
          headers: { "Content-Type": ["application/json"] },
          body: '{"sku":"A1"}',
        },
        response: {
          status: 201,
          headers: { "Content-Type": ["application/json"] },
          body: '{"id":"o-1"}',
          truncated: false,
        },
      }),
    )

    renderViewer(vi.fn(), evidence({ kind: "http_exchange", mime: "application/json" }))

    const requestPane = await screen.findByTestId("evidence-http-request")
    expect(requestPane.textContent).toContain("POST")
    expect(requestPane.textContent).toContain("https://shop.test/api/orders")
    expect(requestPane.textContent).toContain('{"sku":"A1"}')

    const responsePane = screen.getByTestId("evidence-http-response")
    expect(responsePane.textContent).toContain("201")
    expect(responsePane.textContent).toContain('{"id":"o-1"}')

    fireEvent.click(screen.getByRole("button", { name: "复制 curl" }))
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1))
    const curl = String(writeText.mock.calls[0]?.[0] ?? "")
    expect(curl).toContain("curl -X POST")
    expect(curl).toContain("https://shop.test/api/orders")
    expect(curl).toContain("Content-Type: application/json")
    expect(curl).toContain('{"sku":"A1"}')
    expect(screen.getByRole("button", { name: "已复制" })).toBeTruthy()
  })

  it("filters console evidence by search text", async () => {
    mocks.getEvidenceMeta.mockResolvedValue(
      evidenceMeta({ kind: "console_log", mime: "application/json" }),
    )
    mocks.apiStreamFetch.mockResolvedValue(
      jsonResponse({
        items: [
          { type: "log", text: "启动完成", ts: 1 },
          { type: "error", text: "Token 过期", ts: 2 },
        ],
      }),
    )

    renderViewer(vi.fn(), evidence({ kind: "console_log", mime: "application/json" }))

    expect(await screen.findByText(/启动完成/)).toBeTruthy()

    fireEvent.change(screen.getByTestId("evidence-console-search"), { target: { value: "token" } })
    expect(screen.queryByText(/启动完成/)).toBeNull()
    expect(screen.getByText(/Token 过期/)).toBeTruthy()

    fireEvent.change(screen.getByTestId("evidence-console-search"), {
      target: { value: "不存在的内容" },
    })
    expect(screen.getByText("没有匹配的日志。")).toBeTruthy()
  })

  it("shows an empty state when the console log has no entries", async () => {
    mocks.getEvidenceMeta.mockResolvedValue(
      evidenceMeta({ kind: "console_log", mime: "application/json" }),
    )
    mocks.apiStreamFetch.mockResolvedValue(jsonResponse({ items: [] }))

    renderViewer(vi.fn(), evidence({ kind: "console_log", mime: "application/json" }))

    expect(await screen.findByText("该日志证据没有记录内容。")).toBeTruthy()
  })

  it("offers download evidence as a blob link without exposing a path", async () => {
    mocks.getEvidenceMeta.mockResolvedValue(
      evidenceMeta({ kind: "download", mime: "text/csv", size_bytes: 12 }),
    )
    mocks.apiStreamFetch.mockResolvedValue(binaryResponse("a,b", "text/csv"))

    renderViewer(vi.fn(), evidence({ kind: "download", mime: "text/csv" }))

    const link = await screen.findByTestId("evidence-download")
    expect(link.getAttribute("href")).toBe("blob:evidence")
    expect(link.getAttribute("download")).toBe(`evidence-${EVIDENCE_ID}.csv`)
    expect(link.textContent).toContain("下载")
  })

  it("reports a load failure and retries the metadata read", async () => {
    mocks.getEvidenceMeta.mockRejectedValueOnce(new Error("boom"))
    mocks.getEvidenceMeta.mockResolvedValueOnce(evidenceMeta())
    mocks.apiStreamFetch.mockResolvedValue(binaryResponse("png", "image/png"))

    renderViewer()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载证据失败")

    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByTestId("evidence-screenshot")).toBeTruthy()
    expect(mocks.getEvidenceMeta).toHaveBeenCalledTimes(2)
  })

  it("closes on Escape and from the close button", async () => {
    const onClose = vi.fn()
    renderViewer(onClose)
    await screen.findByTestId("evidence-screenshot")

    fireEvent.keyDown(screen.getByTestId("evidence-viewer"), { key: "Escape" })
    expect(onClose).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByRole("button", { name: "关闭证据查看器" }))
    expect(onClose).toHaveBeenCalledTimes(2)
  })
})

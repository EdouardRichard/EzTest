import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({ downloadEvidence: vi.fn() }))
vi.mock("./api", () => api)

import {
  buildCurl,
  diffLines,
  EvidenceViewer,
  findComparisonPair,
  REVOKE_DELAY_MS,
} from "./EvidenceViewer"
import { evidenceScreenshotFixture, evidenceTextFixture } from "./test-fixtures"
import type { Evidence } from "./types"

function download(blob: Blob, filename: string) {
  return {
    blob,
    filename,
    contentType: blob.type,
  }
}

const httpEvidenceFixture: Evidence = {
  ...evidenceTextFixture,
  id: "9403",
  kind: "http",
  mime: "application/json",
  filename: "http-request.json",
}

describe("findComparisonPair same-second captures", () => {
  const sameSecond = "2026-10-31T12:00:00Z"
  const capture = (id: string): Evidence => ({
    ...evidenceScreenshotFixture,
    id,
    created_at: sameSecond,
  })

  it("tie-breaks equal timestamps by numeric evidence id, not array order", () => {
    const older = capture("9402")
    const newer = capture("9403")

    expect(findComparisonPair(newer, [older])).toEqual({ before: older, after: newer })
    expect(findComparisonPair(older, [newer])).toEqual({ before: older, after: newer })
  })

  it("compares numeric ids above Number.MAX_SAFE_INTEGER exactly", () => {
    const older = capture("9007199254740992")
    const newer = capture("9007199254740993")

    expect(findComparisonPair(older, [newer])).toEqual({ before: older, after: newer })
    expect(findComparisonPair(newer, [older])).toEqual({ before: older, after: newer })
  })
})

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
    // Unmount under fake timers so every pending delayed-revoke timer is
    // discarded instead of leaking into the next test.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    cleanup()
    vi.clearAllTimers()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it("shows screenshot bytes through an object URL and revokes it after a delay on close", async () => {
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

    // X-05: revoking the blob URL in the same tick as dialog teardown makes the
    // browser report ERR_FILE_NOT_FOUND for the image it is still painting.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    try {
      rerender(
        <EvidenceViewer
          versionId="2001"
          evidence={evidenceScreenshotFixture}
          open={false}
          onClose={() => {}}
        />,
      )
      expect(revokeObjectURL).not.toHaveBeenCalled()

      act(() => {
        vi.advanceTimersByTime(REVOKE_DELAY_MS)
      })
      expect(revokeObjectURL).toHaveBeenCalledWith("blob:evidence-9401")
    } finally {
      vi.useRealTimers()
    }
  })

  it("opens a full-size lightbox for a screenshot and closes it with Escape", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "step-3.png"),
    )
    const onClose = vi.fn()

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={evidenceScreenshotFixture}
        open
        onClose={onClose}
      />,
    )

    const image = await screen.findByTestId("evidence-image")
    expect(image.className).toContain("object-contain")

    fireEvent.click(screen.getByRole("button", { name: "放大查看" }))
    const lightbox = screen.getByTestId("evidence-lightbox")
    expect(within(lightbox).getByRole("img")).toHaveAttribute("src", "blob:evidence-9401")
    expect(within(lightbox).getByRole("img").className).toContain("object-contain")

    // Escape closes the lightbox first and keeps the evidence dialog open.
    fireEvent.keyDown(window, { key: "Escape" })
    expect(screen.queryByTestId("evidence-lightbox")).not.toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog", { name: "证据 #9401" })).toBeInTheDocument()

    fireEvent.keyDown(window, { key: "Escape" })
    expect(onClose).toHaveBeenCalledTimes(1)
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

  it("renders HTTP request/response panes, copies an exact curl and diffs JSON bodies", async () => {
    const payload = {
      request: {
        method: "POST",
        url: "https://api.example.test/orders",
        headers: {
          "Content-Type": "application/json",
          Authorization: "***",
        },
        body: '{"total":1}',
      },
      response: {
        status: 200,
        // The backend persists response headers as `map[string][]string`
        // (`case_run.go`/`tools.go`), so every real value is an array.
        headers: {
          "Content-Type": ["application/json"],
          "X-Trace-Id": ["trace-9f"],
        },
        body: '{"total":2}',
        body_truncated: false,
      },
    }
    api.downloadEvidence.mockResolvedValue(
      download(
        new Blob([JSON.stringify(payload)], { type: "application/json" }),
        "http-request.json",
      ),
    )
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    })

    render(
      <EvidenceViewer versionId="2001" evidence={httpEvidenceFixture} open onClose={() => {}} />,
    )

    const requestPane = await screen.findByTestId("evidence-http-request")
    expect(requestPane).toHaveTextContent("POST")
    expect(requestPane).toHaveTextContent("https://api.example.test/orders")
    expect(requestPane).toHaveTextContent('{"total":1}')

    const responsePane = screen.getByTestId("evidence-http-response")
    expect(responsePane).toHaveTextContent("200")
    expect(responsePane).toHaveTextContent('{"total":2}')
    // A real backend payload carries response headers as string arrays; they
    // must be rendered, not silently dropped.
    expect(responsePane).toHaveTextContent("X-Trace-Id")
    expect(responsePane).toHaveTextContent("trace-9f")
    expect(responsePane).toHaveTextContent("application/json")

    fireEvent.click(screen.getByRole("button", { name: "复制 curl" }))
    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        "curl -X 'POST' 'https://api.example.test/orders' -H 'Content-Type: application/json' -H 'Authorization: ***' --data-raw '{\"total\":1}'",
      ),
    )

    // Diff only applies to structured JSON bodies; the LCS output marks the
    // line-level change between the request and response payloads.
    const diff = screen.getByTestId("evidence-http-diff")
    expect(diff.querySelector('[data-diff-type="remove"]')).toHaveTextContent('"total": 1')
    expect(diff.querySelector('[data-diff-type="add"]')).toHaveTextContent('"total": 2')
  })

  it("falls back to a plain text pane for malformed HTTP evidence", async () => {
    const malformed = JSON.stringify({ request: { method: "POST" }, response: { status: 200 } })
    api.downloadEvidence.mockResolvedValue(
      download(new Blob([malformed], { type: "application/json" }), "http-request.json"),
    )

    render(
      <EvidenceViewer versionId="2001" evidence={httpEvidenceFixture} open onClose={() => {}} />,
    )

    const text = await screen.findByTestId("evidence-text")
    expect(text).toHaveTextContent('"request"')
    expect(screen.queryByTestId("evidence-http-request")).not.toBeInTheDocument()
    expect(screen.queryByTestId("evidence-http-response")).not.toBeInTheDocument()
  })

  it("shell-quotes the curl method and special characters", () => {
    const curl = buildCurl({
      method: "POST|PUT",
      url: "https://api.example.test/o'brien",
      headers: { "X-Note": "`tick`" },
      body: "it's",
    })

    expect(curl).toContain("-X 'POST|PUT'")
    expect(curl).toContain("'https://api.example.test/o'\\''brien'")
    expect(curl).toContain("-H 'X-Note: `tick`'")
    expect(curl).toContain("--data-raw 'it'\\''s'")
  })

  it("diffs lines with LCS ordering and reconstructs both sides", () => {
    const diff = diffLines("a\nb\nc", "a\nx\nc")

    expect(diff).toEqual([
      { type: "same", text: "a" },
      { type: "remove", text: "b" },
      { type: "add", text: "x" },
      { type: "same", text: "c" },
    ])
    const before = diff
      .filter((line) => line.type !== "add")
      .map((line) => line.text)
      .join("\n")
    const after = diff
      .filter((line) => line.type !== "remove")
      .map((line) => line.text)
      .join("\n")
    expect(before).toBe("a\nb\nc")
    expect(after).toBe("a\nx\nc")
  })

  it("falls back to a full remove/add block for oversized diffs", () => {
    const before = Array.from({ length: 2000 }, (_, index) => `left-${index}`).join("\n")
    const after = Array.from({ length: 2000 }, (_, index) => `right-${index}`).join("\n")

    const diff = diffLines(before, after)

    expect(diff).toHaveLength(4000)
    expect(diff.slice(0, 2000).every((line) => line.type === "remove")).toBe(true)
    expect(diff.slice(2000).every((line) => line.type === "add")).toBe(true)
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

  it("re-downloads through apiDownload and keeps the download blob URL alive briefly", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "step-3.png"),
    )
    // Distinct URLs so the assertion proves the download blob (not the viewer
    // preview blob) is the one kept alive.
    createObjectURL
      .mockReturnValueOnce("blob:evidence-preview")
      .mockReturnValueOnce("blob:evidence-download")
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {})

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={evidenceScreenshotFixture}
        open
        onClose={() => {}}
      />,
    )

    expect(await screen.findByTestId("evidence-image")).toHaveAttribute(
      "src",
      "blob:evidence-preview",
    )
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] })
    try {
      fireEvent.click(screen.getByRole("button", { name: "下载" }))
      await act(async () => {
        await Promise.resolve()
        await Promise.resolve()
      })

      expect(api.downloadEvidence).toHaveBeenCalledTimes(2)
      expect(clickSpy).toHaveBeenCalledTimes(1)
      expect(revokeObjectURL).not.toHaveBeenCalled()

      act(() => {
        vi.advanceTimersByTime(REVOKE_DELAY_MS)
      })
      expect(revokeObjectURL).toHaveBeenCalledWith("blob:evidence-download")
      expect(revokeObjectURL).not.toHaveBeenCalledWith("blob:evidence-preview")
    } finally {
      vi.useRealTimers()
    }
  })
})

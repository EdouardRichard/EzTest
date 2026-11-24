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
  isBinaryEvidence,
  isTextMime,
  parseCommandEvidence,
  parseNetworkEvidence,
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

// X-03 before/after pairing. The behaviour is generic to image evidence kinds,
// so these fixtures use `screenshot` (they originally used the now-removed
// visual_actual kind, which GD-22 deleted; the pairing contract itself stayed).
const earlierCapture: Evidence = {
  ...evidenceScreenshotFixture,
  id: "9412",
  filename: "capture-before.png",
  created_at: "2026-10-31T12:00:00Z",
}

const laterCapture: Evidence = {
  ...earlierCapture,
  id: "9413",
  filename: "capture-after.png",
  created_at: "2026-10-31T12:00:30Z",
}

const differentStepCapture: Evidence = {
  ...earlierCapture,
  id: "9414",
  step_seq: 1,
  filename: "other-step.png",
}

const differentRunCapture: Evidence = {
  ...laterCapture,
  id: "9415",
  case_run_id: "9999",
  filename: "other-run.png",
  created_at: "2026-10-31T12:00:10Z",
}

// `snapshot` is the other surviving image kind: it proves the guard compares
// kinds for equality rather than merely "is an image".
const differentKindCapture: Evidence = {
  ...laterCapture,
  id: "9416",
  kind: "snapshot",
  filename: "snapshot-instead.png",
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

  it("pairs before/after screenshots of the same case run and step", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "capture.png"),
    )
    createObjectURL
      .mockReturnValueOnce("blob:compare-before")
      .mockReturnValueOnce("blob:compare-after")

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={earlierCapture}
        candidates={[earlierCapture, laterCapture, differentStepCapture]}
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
    expect(before).toHaveAttribute("src", "blob:compare-before")
    expect(after).toHaveAttribute("src", "blob:compare-after")
    expect(before.className).toContain("object-contain")
    expect(after.className).toContain("object-contain")

    // Only the before/after pair is downloaded a second time; the different
    // step screenshot is never paired.
    expect(api.downloadEvidence).toHaveBeenCalledWith("2001", "9413")
    expect(api.downloadEvidence).not.toHaveBeenCalledWith("2001", "9414")
  })

  it("does not compare evidence from a different step", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "capture.png"),
    )

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={earlierCapture}
        candidates={[differentStepCapture]}
        open
        onClose={() => {}}
      />,
    )

    await screen.findByTestId("evidence-image")
    expect(screen.queryByTestId("evidence-compare")).not.toBeInTheDocument()
  })

  it("does not compare evidence from another case run or kind", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["png"], { type: "image/png" }), "capture.png"),
    )

    render(
      <EvidenceViewer
        versionId="2001"
        evidence={earlierCapture}
        candidates={[differentRunCapture, differentKindCapture]}
        open
        onClose={() => {}}
      />,
    )

    await screen.findByTestId("evidence-image")
    expect(screen.queryByTestId("evidence-compare")).not.toBeInTheDocument()
    expect(api.downloadEvidence).not.toHaveBeenCalledWith("2001", "9415")
    expect(api.downloadEvidence).not.toHaveBeenCalledWith("2001", "9416")
  })

  /* ------------------------- M4: command/trace/network/file ------------------------- */

  const commandEvidence: Evidence = {
    ...evidenceTextFixture,
    id: "9501",
    kind: "command",
    mime: "application/json",
    filename: "command-cmd_1.json",
  }
  const commandPayload = {
    kind: "command",
    command_id: "cmd_1",
    argv: ["node", "-e", "console.log('hi')"],
    shell: false,
    cwd: "/data/workspaces/run-1",
    env: { PATH: "/usr/bin", EZTEST_EXECUTOR_TOKEN: "dev-secret" },
    exit: 0,
    outcome: "exited",
    duration: 42,
    stdout: "hi\n",
    stderr: "",
    stdout_truncated: false,
    stderr_truncated: false,
    stdout_ref: null,
    stderr_ref: null,
    stdout_bytes: 3,
    stderr_bytes: 0,
  }

  it("renders command evidence with argv, exit/outcome and stdio panes (M4)", async () => {
    api.downloadEvidence.mockResolvedValue(
      download(
        new Blob([JSON.stringify(commandPayload)], { type: "application/json" }),
        "command-cmd_1.json",
      ),
    )
    render(<EvidenceViewer versionId="2001" evidence={commandEvidence} open onClose={() => {}} />)

    const pane = await screen.findByTestId("evidence-command")
    expect(pane).toHaveTextContent("node -e console.log('hi')")
    expect(pane).toHaveTextContent("exit 0")
    expect(pane).toHaveTextContent("exited")
    expect(screen.getByTestId("evidence-command-stdout")).toHaveTextContent("hi")
    // env is full plaintext (GD-01): the token is shown verbatim, not scrubbed.
    expect(screen.getByTestId("evidence-command-env")).toHaveTextContent(
      "EZTEST_EXECUTOR_TOKEN=dev-secret",
    )
    expect(screen.queryByTestId("evidence-text")).not.toBeInTheDocument()
  })

  it("shows command stdio truncation flag and spill ref (M4)", async () => {
    const truncated = {
      ...commandPayload,
      stdout: "head…",
      stdout_truncated: true,
      stdout_ref: ".eztest/commands/cmd_1.stdout.log",
      stdout_bytes: 100000,
    }
    api.downloadEvidence.mockResolvedValue(
      download(
        new Blob([JSON.stringify(truncated)], { type: "application/json" }),
        "command-cmd_1.json",
      ),
    )
    render(<EvidenceViewer versionId="2001" evidence={commandEvidence} open onClose={() => {}} />)

    const stdout = await screen.findByTestId("evidence-command-stdout")
    expect(stdout).toHaveTextContent("已截断")
    expect(stdout).toHaveTextContent(".eztest/commands/cmd_1.stdout.log")
  })

  const networkEvidence: Evidence = {
    ...evidenceTextFixture,
    id: "9502",
    kind: "network",
    mime: "application/json",
    filename: "network.json",
  }

  it("renders network evidence as a request table (M4)", async () => {
    const payload = {
      items: [
        {
          method: "GET",
          url: "https://sut.test/login",
          status: 200,
          duration_ms: 12,
          mime: "text/html",
          resource_type: "document",
        },
        {
          method: "POST",
          url: "https://sut.test/api",
          status: null,
          duration_ms: 5,
          mime: "",
          resource_type: "xhr",
          error: "net::ERR_FAILED",
        },
      ],
      recording: false,
      degraded: false,
    }
    api.downloadEvidence.mockResolvedValue(
      download(new Blob([JSON.stringify(payload)], { type: "application/json" }), "network.json"),
    )
    render(<EvidenceViewer versionId="2001" evidence={networkEvidence} open onClose={() => {}} />)

    const pane = await screen.findByTestId("evidence-network")
    expect(pane).toHaveTextContent("https://sut.test/login")
    expect(pane).toHaveTextContent("net::ERR_FAILED")
    expect(pane).toHaveTextContent("2 条")
    expect(screen.queryByTestId("evidence-network-degraded")).not.toBeInTheDocument()
  })

  it("renders the captured network body preview and discloses truncation (H-D6)", async () => {
    const payload = {
      items: [
        {
          method: "POST",
          url: "https://sut.test/login",
          status: 200,
          duration_ms: 8,
          mime: "application/json",
          resource_type: "xhr",
          body_preview: '{"token":"abc"}',
          body_truncated: false,
          body_bytes: 15,
        },
        {
          method: "GET",
          url: "https://sut.test/big",
          status: 200,
          duration_ms: 20,
          mime: "text/html",
          resource_type: "document",
          body_preview: "<html>partial",
          body_truncated: true,
          body_bytes: 9000,
        },
      ],
      recording: true,
      degraded: false,
    }
    api.downloadEvidence.mockResolvedValue(
      download(new Blob([JSON.stringify(payload)], { type: "application/json" }), "network.json"),
    )
    render(<EvidenceViewer versionId="2001" evidence={networkEvidence} open onClose={() => {}} />)

    const pane = await screen.findByTestId("evidence-network")
    // The captured body text is actually rendered, not parsed and silently dropped.
    expect(pane).toHaveTextContent('{"token":"abc"}')
    expect(pane).toHaveTextContent("<html>partial")
    // A truncated body must be labelled, so the operator never believes they saw
    // the full response.
    expect(pane).toHaveTextContent("已截断")
    expect(pane).toHaveTextContent("8.8 KB")
  })

  it("surfaces an explicit network degradation record (M4, never silent)", async () => {
    const payload = { items: [], recording: false, degraded: true, reason: "cdp_inject_failed" }
    api.downloadEvidence.mockResolvedValue(
      download(new Blob([JSON.stringify(payload)], { type: "application/json" }), "network.json"),
    )
    render(<EvidenceViewer versionId="2001" evidence={networkEvidence} open onClose={() => {}} />)

    const banner = await screen.findByTestId("evidence-network-degraded")
    expect(banner).toHaveTextContent("cdp_inject_failed")
  })

  it("renders a text file evidence preview with metadata (M4)", async () => {
    const fileEvidence: Evidence = {
      ...evidenceTextFixture,
      id: "9503",
      kind: "file",
      mime: "text/plain",
      filename: "seed.sql",
    }
    api.downloadEvidence.mockResolvedValue(
      download(new Blob(["INSERT INTO t VALUES (1);"], { type: "text/plain" }), "seed.sql"),
    )
    render(<EvidenceViewer versionId="2001" evidence={fileEvidence} open onClose={() => {}} />)

    const pane = await screen.findByTestId("evidence-file")
    expect(pane).toHaveTextContent("seed.sql")
    expect(screen.getByTestId("evidence-file-text")).toHaveTextContent("INSERT INTO t VALUES (1);")
  })

  it("renders a binary file evidence as a download notice, not mojibake (M4)", async () => {
    const fileEvidence: Evidence = {
      ...evidenceTextFixture,
      id: "9504",
      kind: "file",
      mime: "application/octet-stream",
      filename: "blob.bin",
    }
    api.downloadEvidence.mockResolvedValue(
      download(
        new Blob([new Uint8Array([0, 1, 2, 255])], { type: "application/octet-stream" }),
        "blob.bin",
      ),
    )
    render(<EvidenceViewer versionId="2001" evidence={fileEvidence} open onClose={() => {}} />)

    const pane = await screen.findByTestId("evidence-file")
    expect(pane).toHaveTextContent("二进制文件")
    expect(screen.queryByTestId("evidence-file-text")).not.toBeInTheDocument()
    expect(screen.queryByTestId("evidence-text")).not.toBeInTheDocument()
  })

  it("renders trace evidence as a download-oriented panel with show-trace hint (M4)", async () => {
    const traceEvidence: Evidence = {
      ...evidenceTextFixture,
      id: "9505",
      kind: "trace",
      mime: "application/zip",
      filename: "trace.zip",
    }
    api.downloadEvidence.mockResolvedValue(
      download(
        new Blob([new Uint8Array([0x50, 0x4b, 3, 4])], { type: "application/zip" }),
        "trace.zip",
      ),
    )
    render(<EvidenceViewer versionId="2001" evidence={traceEvidence} open onClose={() => {}} />)

    const pane = await screen.findByTestId("evidence-trace")
    expect(pane).toHaveTextContent("playwright show-trace")
    expect(pane).toHaveTextContent("trace.zip")
    expect(screen.queryByTestId("evidence-text")).not.toBeInTheDocument()
  })
})

describe("M4 evidence parsers", () => {
  it("parseCommandEvidence accepts a command record and rejects non-command JSON", () => {
    const record = parseCommandEvidence(
      JSON.stringify({ kind: "command", argv: ["echo", "hi"], exit: 0, stdout: "hi" }),
    )
    expect(record).not.toBeNull()
    expect(record?.argv).toEqual(["echo", "hi"])
    expect(record?.exit).toBe(0)
    expect(parseCommandEvidence(JSON.stringify({ kind: "file" }))).toBeNull()
    expect(parseCommandEvidence(JSON.stringify({ request: {}, response: {} }))).toBeNull()
    expect(parseCommandEvidence("not json")).toBeNull()
    expect(parseCommandEvidence(JSON.stringify({ kind: "command", argv: [1] }))).toBeNull()
  })

  it("parseCommandEvidence reports a killed command exit as null", () => {
    const record = parseCommandEvidence(
      JSON.stringify({ kind: "command", argv: ["sleep"], exit: null, outcome: "cancelled" }),
    )
    expect(record?.exit).toBeNull()
    expect(record?.outcome).toBe("cancelled")
  })

  it("parseNetworkEvidence accepts items and degradation records, rejects others", () => {
    const record = parseNetworkEvidence(
      JSON.stringify({ items: [{ method: "GET", url: "u", status: 200 }], degraded: false }),
    )
    expect(record?.items).toHaveLength(1)
    expect(record?.items[0].status).toBe(200)
    const degraded = parseNetworkEvidence(
      JSON.stringify({ items: [], degraded: true, reason: "x" }),
    )
    expect(degraded?.degraded).toBe(true)
    expect(degraded?.reason).toBe("x")
    expect(parseNetworkEvidence(JSON.stringify({ kind: "command" }))).toBeNull()
    expect(parseNetworkEvidence("nope")).toBeNull()
  })

  it("isTextMime / isBinaryEvidence classify file/trace/download kinds", () => {
    expect(isTextMime("text/plain")).toBe(true)
    expect(isTextMime("application/json")).toBe(true)
    expect(isTextMime("application/zip")).toBe(false)
    const base = { ...evidenceTextFixture }
    expect(isBinaryEvidence({ ...base, kind: "trace" })).toBe(true)
    expect(isBinaryEvidence({ ...base, kind: "download" })).toBe(true)
    expect(isBinaryEvidence({ ...base, kind: "file", mime: "text/plain" })).toBe(false)
    expect(isBinaryEvidence({ ...base, kind: "file", mime: "application/octet-stream" })).toBe(true)
    expect(isBinaryEvidence({ ...base, kind: "command" })).toBe(false)
  })
})

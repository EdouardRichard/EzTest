import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const agentCenterApi = vi.hoisted(() => ({ getRunTranscript: vi.fn() }))
vi.mock("./api", () => agentCenterApi)

const sse = vi.hoisted(() => ({ connectSSE: vi.fn(), disconnect: vi.fn() }))
vi.mock("@/lib/sse-client", () => ({ connectSSE: sse.connectSSE }))

import { ApiError, getApiBaseUrl } from "@/lib/api-client"
import type { ConnectSSEOptions } from "@/lib/sse-client"

import {
  applyTranscriptDelta,
  MAX_STALE_REREADS,
  normalizeTranscriptDelta,
  RunTranscriptDrawer,
  TRANSCRIPT_PENDING_LIMIT,
} from "./RunTranscriptDrawer"
import type { AgentRunView, TranscriptMessage } from "./types"

const runFixture: AgentRunView = {
  id: "42",
  profile_key: "case_executor",
  work_item_id: "501",
  work_type: "execute.case",
  version_id: "7",
  conversation_id: null,
  status: "running",
  terminal_reason: null,
  turns: 3,
  usage: {},
  started_at: "2026-11-17T09:00:00Z",
  finished_at: null,
}

/** Streaming placeholder row exactly as the server opens it: parts is empty. */
function streamingPlaceholder(): TranscriptMessage {
  return {
    id: "9101",
    run_id: "42",
    role: "assistant",
    parts: [],
    tool_calls: [],
    usage: {},
    stream_state: "streaming",
    created_at: "2026-11-17T09:01:00Z",
  }
}

function systemRow(): TranscriptMessage {
  return {
    id: "9001",
    run_id: "42",
    role: "system",
    parts: [{ type: "text", text: "你是测试执行助手" }],
    tool_calls: [],
    usage: {},
    stream_state: "complete",
    created_at: "2026-11-17T09:00:30Z",
  }
}

function snapshotFixture(): TranscriptMessage[] {
  return [systemRow(), streamingPlaceholder()]
}

let frameSeq = 0

function capturedOptions(): ConnectSSEOptions {
  expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  return sse.connectSSE.mock.calls[0][0] as ConnectSSEOptions
}

/** Pushes one run:{id} SSE frame through the captured single-owner connection. */
function pushFrame(payload: unknown, overrides: { event?: string; channel?: string } = {}): void {
  const options = capturedOptions()
  frameSeq += 1
  act(() => {
    options.onEvent({
      id: String(frameSeq),
      event: overrides.event ?? "agent.transcript.delta",
      data: JSON.stringify({
        channel: overrides.channel ?? "run:42",
        seq: frameSeq,
        type: overrides.event ?? "agent.transcript.delta",
        scope: { version_id: "7", agent_run_id: "42" },
        payload,
        created_at: "2026-11-17T09:02:00Z",
      }),
    })
  })
}

function appendDelta(increment: string, offset?: number) {
  return {
    run_id: "42",
    message_id: "9101",
    stream_state: "streaming",
    seq: 1,
    part_type: "text",
    ...(offset !== undefined ? { offset } : {}),
    increment,
  }
}

describe("RunTranscriptDrawer", () => {
  beforeEach(() => {
    frameSeq = 0
    agentCenterApi.getRunTranscript.mockReset()
    sse.connectSSE.mockReset()
    sse.disconnect.mockReset()
    sse.connectSSE.mockImplementation(() => sse.disconnect)
    agentCenterApi.getRunTranscript.mockResolvedValue(snapshotFixture())
  })

  afterEach(cleanup)

  it("loads the initial snapshot and owns exactly one run channel connection", async () => {
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)

    const system = await screen.findByTestId("agent-center-transcript-message-9001")
    expect(system).toHaveTextContent("你是测试执行助手")
    expect(system).toHaveTextContent("完成")
    expect(screen.getByTestId("agent-center-transcript-message-9101")).toBeInTheDocument()
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(1)
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledWith("42")

    // Single owner (defect 039 discipline): one connection per open drawer.
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
    const options = capturedOptions()
    expect(options.url).toBe(`${getApiBaseUrl()}/api/stream?channel=run:42`)

    // Frozen rows never show a streaming indicator.
    expect(screen.queryByTestId("agent-center-transcript-streaming-9001")).not.toBeInTheDocument()
  })

  it("accumulates append deltas: creates the missing first part, dedupes replays, discards gaps", async () => {
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    const row = await screen.findByTestId("agent-center-transcript-message-9101")

    // First append targets seq 1 while the placeholder has parts:[] — the part
    // must be created as empty text BEFORE the offset check (offset omitted = 0).
    pushFrame(appendDelta("你好"))
    expect(row).toHaveTextContent("你好")

    pushFrame(appendDelta("世界", 2))
    expect(row).toHaveTextContent("你好世界")

    // Replayed frame (offset already consumed) must not duplicate the suffix.
    pushFrame(appendDelta("世界", 2))
    expect(row).toHaveTextContent("你好世界")
    expect(row.textContent).not.toContain("世界世界")

    // Out-of-order gap frame must be discarded, not spliced.
    pushFrame(appendDelta("!!", 99))
    expect(row).toHaveTextContent("你好世界")
    expect(row.textContent).not.toContain("!!")
  })

  it("replaces the whole row on snapshot-mode deltas and freezes terminal rows", async () => {
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")

    pushFrame({
      run_id: "42",
      message_id: "9101",
      stream_state: "complete",
      message: {
        id: "9101",
        run_id: "42",
        role: "assistant",
        parts: [{ type: "text", text: "最终回复" }],
        tool_calls: [{ id: "call_1", name: "requirement.read", arguments: '{"id":"501"}' }],
        usage: { output_tokens: 9 },
        stream_state: "complete",
        created_at: "2026-11-17T09:01:00Z",
      },
    })

    const row = screen.getByTestId("agent-center-transcript-message-9101")
    expect(row).toHaveTextContent("最终回复")
    expect(row).toHaveTextContent("requirement.read")
    expect(row).toHaveTextContent("output_tokens: 9")
    expect(row).toHaveTextContent("完成")
    expect(screen.queryByTestId("agent-center-transcript-streaming-9101")).not.toBeInTheDocument()

    // A frame after the terminal seal must not reopen or mutate the frozen row.
    pushFrame(appendDelta("不应出现", 4))
    expect(row).toHaveTextContent("最终回复")
    expect(row.textContent).not.toContain("不应出现")
  })

  it("inserts unseen rows from snapshot-mode deltas in id order", async () => {
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")

    pushFrame({
      run_id: "42",
      message_id: "9200",
      stream_state: "complete",
      message: {
        id: "9200",
        run_id: "42",
        role: "tool",
        parts: [{ type: "text", text: "工具结果" }],
        tool_calls: [],
        tool_call_id: "call_1",
        usage: {},
        stream_state: "complete",
        created_at: "2026-11-17T09:03:00Z",
      },
    })

    const messages = screen.getAllByTestId(/agent-center-transcript-message-/)
    expect(messages.map((node) => node.getAttribute("data-testid"))).toEqual([
      "agent-center-transcript-message-9001",
      "agent-center-transcript-message-9101",
      "agent-center-transcript-message-9200",
    ])
  })

  it("shows the live streaming indicator while the run is running", async () => {
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")
    expect(screen.getByTestId("agent-center-transcript-streaming-9101")).toBeInTheDocument()
    expect(screen.queryByTestId("agent-center-transcript-stale-9101")).not.toBeInTheDocument()
  })

  it("shows a stale indication instead of an endless spinner when the run is not live", async () => {
    // Crash-recovery gap is inherited: a run can be terminal while a transcript
    // row stayed streaming. The drawer must not spin forever for such a row.
    render(<RunTranscriptDrawer run={{ ...runFixture, status: "done" }} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")
    expect(screen.getByTestId("agent-center-transcript-stale-9101")).toBeInTheDocument()
    expect(screen.queryByTestId("agent-center-transcript-streaming-9101")).not.toBeInTheDocument()
  })

  it("applies deltas that arrive before the snapshot resolves", async () => {
    let resolveSnapshot: (rows: TranscriptMessage[]) => void = () => {}
    agentCenterApi.getRunTranscript.mockImplementation(
      () =>
        new Promise<TranscriptMessage[]>((resolve) => {
          resolveSnapshot = resolve
        }),
    )
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)

    pushFrame(appendDelta("早到"))
    await act(async () => {
      resolveSnapshot(snapshotFixture())
    })

    const row = await screen.findByTestId("agent-center-transcript-message-9101")
    expect(row).toHaveTextContent("早到")
  })

  it("ignores unrelated event types, foreign channels and malformed payloads", async () => {
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    const row = await screen.findByTestId("agent-center-transcript-message-9101")

    pushFrame(appendDelta("别的类型"), { event: "job.progress" })
    pushFrame(appendDelta("别的通道"), { channel: "run:43" })
    pushFrame({
      run_id: "43",
      message_id: "9101",
      stream_state: "streaming",
      seq: 1,
      part_type: "text",
      increment: "别的run",
    })
    pushFrame({ run_id: "42", message_id: "9101", stream_state: "streaming" })
    pushFrame(null)

    expect(row.textContent).not.toContain("别的")
  })

  it("refetches the snapshot on stream.reset without opening a second connection", async () => {
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9001")

    act(() => {
      capturedOptions().onReset?.({ reason: "cursor_out_of_range" })
    })

    await waitFor(() => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(2))
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("renders a graceful error when the transcript snapshot is forbidden", async () => {
    agentCenterApi.getRunTranscript.mockRejectedValue(
      new ApiError({ status: 403, code: "forbidden", message: "permission denied" }),
    )
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)

    expect(await screen.findByText("permission denied")).toBeInTheDocument()
    expect(screen.getByText("转录加载失败")).toBeInTheDocument()
  })

  it("disconnects the single connection on unmount and closes via the dialog button", async () => {
    const onClose = vi.fn()
    const { unmount } = render(<RunTranscriptDrawer run={runFixture} onClose={onClose} />)
    await screen.findByTestId("agent-center-transcript-message-9001")

    fireEvent.click(screen.getByRole("button", { name: "关闭对话框" }))
    expect(onClose).toHaveBeenCalledTimes(1)

    unmount()
    expect(sse.disconnect).toHaveBeenCalledTimes(1)
  })

  it("renders the empty transcript state", async () => {
    agentCenterApi.getRunTranscript.mockResolvedValue([])
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    expect(await screen.findByText("暂无转录")).toBeInTheDocument()
    expect(
      within(screen.getByTestId("agent-center-run-transcript")).queryByText("生成中"),
    ).not.toBeInTheDocument()
  })
})

/** A full snapshot-mode payload exactly as the server's TranscriptDelta emits it. */
function snapshotPayload(message: Record<string, unknown>, messageId = "9101") {
  return { run_id: "42", message_id: messageId, stream_state: "streaming", message }
}

function fullRow(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return { ...streamingPlaceholder(), ...overrides }
}

/**
 * H-E21: the validator only checked `message.id`, then double-asserted the rest
 * into TranscriptMessage. `TranscriptRow` reads `message.parts.map`,
 * `message.tool_calls.map`, `message.created_at.length` and `formatUsage(usage)`,
 * so a snapshot missing those collections crashed the whole drawer.
 */
describe("normalizeTranscriptDelta validates the rendered shape (H-E21)", () => {
  it("rejects a snapshot whose message has no parts", () => {
    expect(normalizeTranscriptDelta(snapshotPayload({ id: "9101" }))).toBeNull()
  })

  it("rejects a snapshot whose collections are not arrays", () => {
    expect(normalizeTranscriptDelta(snapshotPayload(fullRow({ parts: {} })))).toBeNull()
    expect(normalizeTranscriptDelta(snapshotPayload(fullRow({ tool_calls: null })))).toBeNull()
  })

  it("rejects a part without a string type or with a non-string text", () => {
    expect(
      normalizeTranscriptDelta(snapshotPayload(fullRow({ parts: [{ text: "x" }] }))),
    ).toBeNull()
    expect(
      normalizeTranscriptDelta(snapshotPayload(fullRow({ parts: [{ type: "text", text: 3 }] }))),
    ).toBeNull()
    expect(normalizeTranscriptDelta(snapshotPayload(fullRow({ parts: ["text"] })))).toBeNull()
  })

  it("rejects a tool call missing id/name/arguments", () => {
    expect(
      normalizeTranscriptDelta(snapshotPayload(fullRow({ tool_calls: [{ id: "c1" }] }))),
    ).toBeNull()
  })

  it("rejects a non-numeric usage value, a non-string created_at and an unknown role", () => {
    expect(
      normalizeTranscriptDelta(snapshotPayload(fullRow({ usage: { output_tokens: "9" } }))),
    ).toBeNull()
    expect(normalizeTranscriptDelta(snapshotPayload(fullRow({ created_at: 12 })))).toBeNull()
    expect(normalizeTranscriptDelta(snapshotPayload(fullRow({ role: "robot" })))).toBeNull()
  })

  it("rejects a snapshot whose message.stream_state is not a shared stream state", () => {
    expect(
      normalizeTranscriptDelta(snapshotPayload(fullRow({ stream_state: "paused" }))),
    ).toBeNull()
  })

  it("rejects a snapshot whose message.id disagrees with message_id", () => {
    expect(normalizeTranscriptDelta(snapshotPayload(fullRow({ id: "9999" })))).toBeNull()
  })

  it("still accepts the authoritative full row", () => {
    const delta = normalizeTranscriptDelta(
      snapshotPayload(
        fullRow({
          parts: [{ type: "text", text: "最终回复" }],
          tool_calls: [{ id: "c1", name: "requirement.read", arguments: "{}" }],
          usage: { output_tokens: 9 },
          stream_state: "complete",
        }),
      ),
    )
    expect(delta).not.toBeNull()
    expect(delta?.message?.parts).toEqual([{ type: "text", text: "最终回复" }])
  })

  it("applyTranscriptDelta refuses a full row whose id disagrees with message_id", () => {
    const rows = [streamingPlaceholder()]
    const delta = {
      run_id: "42",
      message_id: "9101",
      stream_state: "streaming" as const,
      message: { ...streamingPlaceholder(), id: "9999" },
    }
    expect(applyTranscriptDelta(rows, delta)).toBe(rows)
  })
})

/**
 * H-E22: the replay comment claimed offset dedup made the whole replay
 * idempotent, but that only holds for APPEND mode. A buffered snapshot-mode frame
 * has no offset, so replaying it in arrival order overwrote a newer HTTP snapshot
 * with an older full row (the streaming placeholder wiped the stored reply).
 */
describe("buffered snapshot replay cannot regress the HTTP snapshot (H-E22)", () => {
  beforeEach(() => {
    frameSeq = 0
    agentCenterApi.getRunTranscript.mockReset()
    sse.connectSSE.mockReset()
    sse.connectSSE.mockImplementation(() => sse.disconnect)
  })

  afterEach(cleanup)

  it("re-reads authoritatively instead of replaying an older streaming full frame", async () => {
    let resolveFirst: (rows: TranscriptMessage[]) => void = () => {}
    const stored: TranscriptMessage = {
      ...streamingPlaceholder(),
      parts: [{ type: "text", text: "已生成的较长回复" }],
    }
    agentCenterApi.getRunTranscript
      .mockImplementationOnce(
        () =>
          new Promise<TranscriptMessage[]>((resolve) => {
            resolveFirst = resolve
          }),
      )
      .mockResolvedValue([systemRow(), stored])

    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    // The turn's placeholder frame races the in-flight snapshot: it is OLDER
    // (parts empty) than the row the snapshot will return.
    pushFrame(snapshotPayload(streamingPlaceholder()))
    await act(async () => {
      resolveFirst([systemRow(), stored])
    })

    const row = await screen.findByTestId("agent-center-transcript-message-9101")
    expect(row).toHaveTextContent("已生成的较长回复")
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(2)
    // Still exactly one SSE connection: the refresh is an HTTP re-read.
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("applies a buffered TERMINAL full frame directly (the row's last frame)", async () => {
    let resolveFirst: (rows: TranscriptMessage[]) => void = () => {}
    agentCenterApi.getRunTranscript
      .mockImplementationOnce(
        () =>
          new Promise<TranscriptMessage[]>((resolve) => {
            resolveFirst = resolve
          }),
      )
      .mockResolvedValue([systemRow()])
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    pushFrame({
      run_id: "42",
      message_id: "9101",
      stream_state: "complete",
      message: {
        ...streamingPlaceholder(),
        parts: [{ type: "text", text: "封口回复" }],
        stream_state: "complete",
      },
    })
    await act(async () => {
      resolveFirst([systemRow(), streamingPlaceholder()])
    })

    const row = await screen.findByTestId("agent-center-transcript-message-9101")
    expect(row).toHaveTextContent("封口回复")
    expect(row).toHaveTextContent("完成")
    // A terminal frame is the row's last frame, so no re-read is needed.
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(1)
  })

  it("bounds the stale-head re-read and falls back without livelocking (keeps appends, stays live)", async () => {
    // Two-plus non-terminal assistant heads arriving faster than the HTTP round
    // trip must not drive an unbounded fetch→re-read loop that leaves rows null
    // forever. After MAX_STALE_REREADS the drawer stops re-reading, drops only the
    // un-orderable stale head frame, and still applies appends so the UI updates.
    const resolvers: Array<(rows: TranscriptMessage[]) => void> = []
    agentCenterApi.getRunTranscript.mockImplementation(
      () =>
        new Promise<TranscriptMessage[]>((resolve) => {
          resolvers.push(resolve)
        }),
    )
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)

    for (let attempt = 0; attempt < MAX_STALE_REREADS; attempt += 1) {
      pushFrame(snapshotPayload(streamingPlaceholder()))
      await act(async () => {
        resolvers[attempt]([systemRow()])
      })
      // The drawer re-read; make sure the next fetch has been issued.
      await waitFor(() =>
        expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(attempt + 2),
      )
    }

    // Final fetch: a stale head AND a legitimate append are queued together.
    pushFrame(snapshotPayload(streamingPlaceholder()))
    pushFrame(appendDelta("保留增量"))
    await act(async () => {
      resolvers[MAX_STALE_REREADS]([systemRow(), streamingPlaceholder()])
    })

    const row = await screen.findByTestId("agent-center-transcript-message-9101")
    expect(row).toHaveTextContent("保留增量")
    // Re-reads stopped exactly at the cap: no further fetch was issued.
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(MAX_STALE_REREADS + 1)
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })
})

/**
 * H-E23 + the pending bound: the queue must not grow without limit (the only
 * H-E23 loss). The load effect (re)builds rows from a fresh snapshot on every
 * run, so a rejected load's buffer is never replayed.
 */
describe("pending transcript queue is bounded; retries rebuild from a fresh snapshot (H-E23)", () => {
  beforeEach(() => {
    frameSeq = 0
    agentCenterApi.getRunTranscript.mockReset()
    sse.connectSSE.mockReset()
    sse.connectSSE.mockImplementation(() => sse.disconnect)
  })

  afterEach(cleanup)

  it("bounds the pre-snapshot queue and reports the overflow instead of dropping silently", async () => {
    let resolveSnapshot: (rows: TranscriptMessage[]) => void = () => {}
    agentCenterApi.getRunTranscript.mockImplementation(
      () =>
        new Promise<TranscriptMessage[]>((resolve) => {
          resolveSnapshot = resolve
        }),
    )
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)

    const overflow = 40
    for (let index = 0; index < TRANSCRIPT_PENDING_LIMIT + overflow; index += 1) {
      pushFrame({
        run_id: "42",
        message_id: String(20000 + index),
        stream_state: "streaming",
        seq: 1,
        part_type: "text",
        increment: "x",
      })
    }
    await act(async () => {
      resolveSnapshot([systemRow()])
    })

    await screen.findByTestId("agent-center-transcript-message-9001")
    const rendered = screen.getAllByTestId(/agent-center-transcript-message-/)
    expect(rendered.length).toBeLessThanOrEqual(TRANSCRIPT_PENDING_LIMIT + 1)
    // Loud, not silent (constitution 七.6): the drop is surfaced with a retry.
    expect(screen.getByTestId("agent-center-transcript-lag")).toBeInTheDocument()
  })

  it("a retry rebuilds the transcript from a fresh snapshot (the load effect clears the buffer on every run)", async () => {
    // Guardrail for the load effect's OWN reset (`pendingRef.current = []` at the
    // top of every run): a retry/stream.reset/re-sync always starts from an empty
    // buffer, so a delta buffered during a rejected load is never replayed. This
    // pins that reset semantics — it is NOT evidence of a reject-branch clear
    // (there is none) and NOT evidence of an H-E23 fix (the only H-E23 loss is
    // unbounded growth in the error window, pinned by the bound test above).
    agentCenterApi.getRunTranscript.mockRejectedValueOnce(
      new ApiError({ status: 403, code: "forbidden", message: "permission denied" }),
    )
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByText("转录加载失败")

    pushFrame(appendDelta("陈旧增量"))
    agentCenterApi.getRunTranscript.mockResolvedValue([systemRow()])
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    await waitFor(() => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(2))
    expect(screen.queryByTestId("agent-center-transcript-message-9101")).not.toBeInTheDocument()
  })

  it("coalesces a contiguous same-head burst past the bound without lag or loss", async () => {
    // Differential guard for the lossless compaction: a contiguous single-head
    // burst larger than the bound must still be applied in full (nothing dropped,
    // no lag notice), because the increments coalesce to one buffered entry.
    let resolveSnapshot: (rows: TranscriptMessage[]) => void = () => {}
    agentCenterApi.getRunTranscript.mockImplementation(
      () =>
        new Promise<TranscriptMessage[]>((resolve) => {
          resolveSnapshot = resolve
        }),
    )
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)

    const burst = TRANSCRIPT_PENDING_LIMIT + 40
    for (let index = 0; index < burst; index += 1) {
      pushFrame(appendDelta("x", index))
    }
    await act(async () => {
      resolveSnapshot([systemRow(), streamingPlaceholder()])
    })

    const row = await screen.findByTestId("agent-center-transcript-message-9101")
    expect(row.textContent).toContain("x".repeat(burst))
    expect(screen.queryByTestId("agent-center-transcript-lag")).not.toBeInTheDocument()
  })
})

/**
 * H-E24: the append addressing rules had no direct tests (unknown message_id,
 * seq jump, same-seq type change). Expectations come from the delta contract.
 */
describe("applyTranscriptDelta addressing coverage (H-E24)", () => {
  function append(overrides: Record<string, unknown> = {}) {
    return {
      run_id: "42",
      message_id: "9101",
      stream_state: "streaming" as const,
      seq: 1,
      part_type: "text",
      increment: "x",
      ...overrides,
    }
  }

  it("creates a skeleton row for an append against an unknown message_id", () => {
    const rows = applyTranscriptDelta([], append())
    expect(rows).toHaveLength(1)
    expect(rows[0].id).toBe("9101")
    expect(rows[0].role).toBe("assistant")
    expect(rows[0].parts).toEqual([{ type: "text", text: "x" }])
    expect(rows[0].tool_calls).toEqual([])
    expect(rows[0].usage).toEqual({})
  })

  it("inserts an unknown message_id in id order relative to existing rows", () => {
    const rows = applyTranscriptDelta([systemRow()], append({ message_id: "9050" }))
    expect(rows.map((row) => row.id)).toEqual(["9001", "9050"])
  })

  it("refuses a seq jump past the next slot instead of fabricating parts", () => {
    const rows = [streamingPlaceholder()]
    expect(applyTranscriptDelta(rows, append({ seq: 3, offset: 0 }))).toBe(rows)
  })

  it("refuses an append against a different-type part at the same seq", () => {
    const rows: TranscriptMessage[] = [
      { ...streamingPlaceholder(), parts: [{ type: "thinking", text: "核对" }] },
    ]
    expect(applyTranscriptDelta(rows, append({ offset: 2 }))).toBe(rows)
  })

  it("refuses every frame against a frozen row", () => {
    const rows: TranscriptMessage[] = [{ ...streamingPlaceholder(), stream_state: "interrupted" }]
    expect(applyTranscriptDelta(rows, append())).toBe(rows)
  })
})

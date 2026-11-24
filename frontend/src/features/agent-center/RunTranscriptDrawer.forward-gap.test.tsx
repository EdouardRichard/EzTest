import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

/**
 * T28e: T28c coalesces transcript persistence to <=250ms/32 deltas but keeps
 * SSE offsets computed from the *published* text. A drawer opened mid-turn reads
 * a DB snapshot that can be shorter than the published text, so the next
 * increment's `offset` is AHEAD of the local accumulated length. The strict
 * equality check drops every such frame until the turn seals, freezing live
 * text. These tests pin the bounded, debounced snapshot re-read that heals a
 * FORWARD offset gap on a streaming assistant head.
 */

const agentCenterApi = vi.hoisted(() => ({ getRunTranscript: vi.fn() }))
vi.mock("./api", () => agentCenterApi)

const sse = vi.hoisted(() => ({ connectSSE: vi.fn(), disconnect: vi.fn() }))
vi.mock("@/lib/sse-client", () => ({ connectSSE: sse.connectSSE }))

import type { ConnectSSEOptions } from "@/lib/sse-client"

import { MAX_STALE_REREADS, RunTranscriptDrawer } from "./RunTranscriptDrawer"
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

function streamingHead(text: string): TranscriptMessage {
  return {
    id: "9101",
    run_id: "42",
    role: "assistant",
    parts: text === "" ? [] : [{ type: "text", text }],
    tool_calls: [],
    usage: {},
    stream_state: "streaming",
    created_at: "2026-11-17T09:01:00Z",
  }
}

let frameSeq = 0

function capturedOptions(): ConnectSSEOptions {
  expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  return sse.connectSSE.mock.calls[0][0] as ConnectSSEOptions
}

function pushFrame(payload: unknown): void {
  const options = capturedOptions()
  frameSeq += 1
  act(() => {
    options.onEvent({
      id: String(frameSeq),
      event: "agent.transcript.delta",
      data: JSON.stringify({
        channel: "run:42",
        seq: frameSeq,
        type: "agent.transcript.delta",
        scope: { version_id: "7", agent_run_id: "42" },
        payload,
        created_at: "2026-11-17T09:02:00Z",
      }),
    })
  })
}

function appendDelta(increment: string, offset: number) {
  return {
    run_id: "42",
    message_id: "9101",
    stream_state: "streaming",
    seq: 1,
    part_type: "text",
    offset,
    increment,
  }
}

/** Advances real time past the re-read debounce and flushes the resulting fetch. */
async function advance(ms = 400): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, ms))
  })
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

function transcriptRow(): HTMLElement {
  return screen.getByTestId("agent-center-transcript-message-9101")
}

describe("RunTranscriptDrawer forward offset gap re-read (T28e)", () => {
  beforeEach(() => {
    frameSeq = 0
    agentCenterApi.getRunTranscript.mockReset()
    sse.connectSSE.mockReset()
    sse.disconnect.mockReset()
    sse.connectSSE.mockImplementation(() => sse.disconnect)
  })

  afterEach(cleanup)

  it("re-reads the snapshot after a forward gap and resumes at the published edge", async () => {
    // First read (opened mid-turn) lags the published text; the follow-up read
    // has caught up to the offset the live increment carried.
    agentCenterApi.getRunTranscript
      .mockResolvedValueOnce([streamingHead("abcde")])
      .mockResolvedValue([streamingHead("abcdefghi")])

    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")
    await waitFor(() => expect(transcriptRow()).toHaveTextContent("abcde"))
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(1)

    // offset 9 > local length 5: a forward gap. The increment is dropped now,
    // and a debounced authoritative re-read is scheduled (not immediate).
    pushFrame(appendDelta("X", 9))
    expect(transcriptRow()).toHaveTextContent("abcde")
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(1)

    await waitFor(() => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(2), {
      timeout: 2000,
    })
    await flush()
    await waitFor(() => expect(transcriptRow()).toHaveTextContent("abcdefghi"))
    // Re-read is an HTTP fetch, never a second SSE connection.
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("debounces a burst of gaps into one re-read and bounds the total per load", async () => {
    // The DB never catches up in this simulation: every snapshot stays "abcde",
    // so each round re-gaps. Only the bounded budget may cap the re-reads.
    agentCenterApi.getRunTranscript.mockResolvedValue([streamingHead("abcde")])
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")
    await waitFor(() => expect(transcriptRow()).toHaveTextContent("abcde"))

    // Burst 1: five gap frames inside one debounce window -> exactly one re-read.
    for (let index = 0; index < 5; index += 1) pushFrame(appendDelta("X", 9))
    await waitFor(() => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(2), {
      timeout: 2000,
    })
    await flush()
    await advance()
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(2)

    // Burst 2: consumes the remaining budget -> one more re-read.
    for (let index = 0; index < 5; index += 1) pushFrame(appendDelta("X", 9))
    await waitFor(() => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(3), {
      timeout: 2000,
    })
    await flush()
    await advance()
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(3)

    // Burst 3: budget exhausted -> no further re-read (terminal seal heals later).
    for (let index = 0; index < 5; index += 1) pushFrame(appendDelta("X", 9))
    await advance()
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(3)
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("does not re-read on replay frames, terminal full frames, or frozen rows", async () => {
    agentCenterApi.getRunTranscript.mockResolvedValue([streamingHead("abcde")])
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")
    await waitFor(() => expect(transcriptRow()).toHaveTextContent("abcde"))

    // offset == local length: a legitimate append, never a gap.
    pushFrame(appendDelta("!", 5))
    expect(transcriptRow()).toHaveTextContent("abcde!")
    // offset < local length: a replayed frame, never a forward gap.
    pushFrame(appendDelta("!!", 3))
    expect(transcriptRow()).toHaveTextContent("abcde!")

    await advance()
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(1)

    // A terminal full frame is the row's last frame; it must not trigger a read.
    pushFrame({
      run_id: "42",
      message_id: "9101",
      stream_state: "complete",
      message: { ...streamingHead("abcdef"), stream_state: "complete" },
    })
    await waitFor(() => expect(transcriptRow()).toHaveTextContent("完成"))

    // A forward-offset append against the now-frozen row must not re-read either.
    pushFrame(appendDelta("X", 99))
    expect(transcriptRow()).toHaveTextContent("abcdef")

    await advance()
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(1)
  })

  it("restores the re-read budget after an external stream.reset", async () => {
    agentCenterApi.getRunTranscript.mockResolvedValue([streamingHead("abcde")])
    render(<RunTranscriptDrawer run={runFixture} onClose={vi.fn()} />)
    await screen.findByTestId("agent-center-transcript-message-9101")
    await waitFor(() => expect(transcriptRow()).toHaveTextContent("abcde"))

    // Consume the whole budget (one initial fetch plus MAX_STALE_REREADS reads).
    for (let attempt = 0; attempt < MAX_STALE_REREADS; attempt += 1) {
      pushFrame(appendDelta("X", 9))
      await waitFor(
        () => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(attempt + 2),
        { timeout: 2000 },
      )
      await flush()
    }

    // Budget exhausted: another gap must not re-read.
    pushFrame(appendDelta("X", 9))
    await advance()
    expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(MAX_STALE_REREADS + 1)

    // An external reset (cursor fell out of the window) restores the budget.
    act(() => {
      capturedOptions().onReset?.({ reason: "cursor_out_of_range" })
    })
    await waitFor(
      () => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(MAX_STALE_REREADS + 2),
      { timeout: 2000 },
    )
    await flush()

    pushFrame(appendDelta("X", 9))
    await waitFor(
      () => expect(agentCenterApi.getRunTranscript).toHaveBeenCalledTimes(MAX_STALE_REREADS + 3),
      { timeout: 2000 },
    )
  })
})

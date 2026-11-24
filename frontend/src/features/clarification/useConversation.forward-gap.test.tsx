import { act, cleanup, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

/**
 * T28e: the Host conversation stream (useConversation) shares the drawer's T28c
 * lag problem. A thread loaded mid-turn seeds its live projection from a DB
 * snapshot that can be behind the published text (bounded flush), so the next
 * `conversation.message.delta` carries an `offset` AHEAD of the accumulated
 * length and the strict-equality append drops it until the turn seals. These
 * tests pin the bounded, debounced snapshot re-read that heals a FORWARD offset
 * gap on a streaming assistant head (T28d will apply the same flush to Host).
 */

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  listQuestions: vi.fn(),
}))
vi.mock("./api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api")>()),
  listMessages: api.listMessages,
  postMessage: api.postMessage,
  listQuestions: api.listQuestions,
}))

type ConnectOptions = {
  url: string
  token: string
  onEvent: (event: { id?: string; event: string; data: string }) => void
  onStatus: (status: string) => void
  onReset?: (data: unknown) => void
}

const sse = vi.hoisted(() => ({
  connectSSE: vi.fn<(options: ConnectOptions) => () => void>(),
}))
vi.mock("@/lib/sse-client", () => ({ connectSSE: sse.connectSSE }))

import type { ConversationMessage } from "./types"
import { MAX_FORWARD_GAP_REREADS, publishStreamReset, useConversation } from "./useConversation"
import { useVersionEvents } from "./useVersionEvents"

const TIMESTAMP = "2026-11-17T10:00:00Z"

let connections: ConnectOptions[] = []

beforeEach(() => {
  connections = []
  api.listMessages.mockReset()
  api.postMessage.mockReset()
  api.listQuestions.mockReset()
  api.listQuestions.mockResolvedValue([])
  sse.connectSSE.mockReset()
  sse.connectSSE.mockImplementation((options: ConnectOptions) => {
    connections.push(options)
    return () => {}
  })
})

afterEach(cleanup)

function eventFrame(type: string, payload: Record<string, unknown>): string {
  return JSON.stringify({
    seq: 1,
    channel: "version:2001",
    type,
    scope: { version_id: "2001", conversation_id: "4001" },
    payload,
    actor_id: null,
    created_at: TIMESTAMP,
  })
}

function push(connection: ConnectOptions, type: string, payload: Record<string, unknown>): void {
  act(() => {
    connection.onEvent({ event: "message", data: eventFrame(type, payload) })
  })
}

function textDelta(
  messageId: string,
  offset: number | undefined,
  increment: string,
): Record<string, unknown> {
  return {
    message_id: messageId,
    conversation_id: "4001",
    seq: 1,
    part_type: "text",
    ...(offset === undefined ? {} : { offset }),
    increment,
  }
}

function streamingSnapshot(messageId: string, text: string): ConversationMessage {
  return {
    id: messageId,
    conversation_id: "4001",
    role: "assistant",
    parts: [{ type: "text", text }],
    refs: [],
    created_at: TIMESTAMP,
    stream_state: "streaming",
  }
}

function snapshotPage(messageId: string, text: string) {
  return { items: [streamingSnapshot(messageId, text)], nextCursor: "", hasMore: false }
}

function terminalFrame(messageId: string, text: string): Record<string, unknown> {
  return {
    id: messageId,
    conversation_id: "4001",
    role: "assistant",
    parts: [{ type: "text", text, seq: 1 }],
    refs: [],
    created_at: TIMESTAMP,
    stream_state: "complete",
  }
}

/** Renders the accumulated assistant text of the thread. */
function Harness() {
  const [liveMessages, setLiveMessages] = useState<ConversationMessage[]>([])
  useVersionEvents("2001", {
    onConversationMessage: (message) => {
      setLiveMessages((current) =>
        current.some((existing) => existing.id === message.id) ? current : [...current, message],
      )
    },
  })
  const { messages } = useConversation({ versionId: "2001", conversationId: "4001", liveMessages })
  const text = messages
    .filter((message) => message.role === "assistant")
    .flatMap((message) => message.parts)
    .map((part) => {
      const value = (part as { text?: unknown }).text
      return typeof value === "string" ? value : ""
    })
    .join("")
  return <output data-testid="reply-text">{text}</output>
}

function replyText(): string {
  return screen.getByTestId("reply-text").textContent ?? ""
}

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

describe("useConversation forward offset gap re-read (T28e)", () => {
  it("re-reads the snapshot after a forward gap and resumes at the published edge", async () => {
    api.listMessages
      .mockResolvedValueOnce(snapshotPage("8101", "abcde"))
      .mockResolvedValue(snapshotPage("8101", "abcdefghi"))
    render(<Harness />)
    const workspace = connections[0]
    await waitFor(() => expect(replyText()).toBe("abcde"))
    expect(api.listMessages).toHaveBeenCalledTimes(1)

    // offset 9 > local length 5: a forward gap. Dropped now; a debounced
    // authoritative snapshot refetch is scheduled.
    push(workspace, "conversation.message.delta", textDelta("8101", 9, "X"))
    expect(replyText()).toBe("abcde")
    expect(api.listMessages).toHaveBeenCalledTimes(1)

    await waitFor(() => expect(api.listMessages).toHaveBeenCalledTimes(2), { timeout: 2000 })
    await flush()
    // The buffered increment replays onto the caught-up snapshot (offset 9).
    await waitFor(() => expect(replyText()).toBe("abcdefghiX"))
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("debounces a burst of gaps into one re-read and bounds the total per load", async () => {
    // The DB never catches up here: every snapshot stays "abcde", so each round
    // re-gaps. Only the bounded budget may cap the re-reads.
    api.listMessages.mockResolvedValue(snapshotPage("8111", "abcde"))
    render(<Harness />)
    const workspace = connections[0]
    await waitFor(() => expect(replyText()).toBe("abcde"))

    // Burst 1: several gap frames inside one window -> one re-read.
    for (let index = 0; index < 5; index += 1) {
      push(workspace, "conversation.message.delta", textDelta("8111", 9, "X"))
    }
    await waitFor(() => expect(api.listMessages).toHaveBeenCalledTimes(2), { timeout: 2000 })
    await flush()
    await advance()
    expect(api.listMessages).toHaveBeenCalledTimes(2)

    // Burst 2: consumes the remaining budget -> one more re-read.
    for (let index = 0; index < 5; index += 1) {
      push(workspace, "conversation.message.delta", textDelta("8111", 9, "X"))
    }
    await waitFor(() => expect(api.listMessages).toHaveBeenCalledTimes(3), { timeout: 2000 })
    await flush()
    await advance()
    expect(api.listMessages).toHaveBeenCalledTimes(3)

    // Burst 3: budget exhausted -> no further re-read (terminal frame heals).
    for (let index = 0; index < 5; index += 1) {
      push(workspace, "conversation.message.delta", textDelta("8111", 9, "X"))
    }
    await advance()
    expect(api.listMessages).toHaveBeenCalledTimes(3)
    expect(api.listMessages.mock.calls.length).toBeLessThanOrEqual(MAX_FORWARD_GAP_REREADS + 1)
  })

  it("does not re-read on replay frames, terminal frames, or terminal heads", async () => {
    api.listMessages.mockResolvedValue(snapshotPage("8102", "abcde"))
    render(<Harness />)
    const workspace = connections[0]
    await waitFor(() => expect(replyText()).toBe("abcde"))

    // offset == local length: legitimate append, never a gap.
    push(workspace, "conversation.message.delta", textDelta("8102", 5, "!"))
    expect(replyText()).toBe("abcde!")
    // offset < local length: a replayed frame, never a forward gap.
    push(workspace, "conversation.message.delta", textDelta("8102", 3, "!!"))
    expect(replyText()).toBe("abcde!")

    await advance()
    expect(api.listMessages).toHaveBeenCalledTimes(1)

    // A terminal full frame seals the head; it must not trigger a re-read.
    push(workspace, "conversation.message", terminalFrame("8102", "abcdef"))
    await waitFor(() => expect(replyText()).toBe("abcdef"))

    // A forward-offset append against the now-terminal head must not re-read.
    push(workspace, "conversation.message.delta", textDelta("8102", 99, "X"))
    expect(replyText()).toBe("abcdef")

    await advance()
    expect(api.listMessages).toHaveBeenCalledTimes(1)
  })

  it("restores the re-read budget after an external stream.reset", async () => {
    api.listMessages.mockResolvedValue(snapshotPage("8103", "abcde"))
    render(<Harness />)
    const workspace = connections[0]
    await waitFor(() => expect(replyText()).toBe("abcde"))

    // Consume the whole budget (one initial fetch plus MAX_FORWARD_GAP_REREADS).
    for (let attempt = 0; attempt < MAX_FORWARD_GAP_REREADS; attempt += 1) {
      push(workspace, "conversation.message.delta", textDelta("8103", 9, "X"))
      await waitFor(() => expect(api.listMessages).toHaveBeenCalledTimes(attempt + 2), {
        timeout: 2000,
      })
      await flush()
    }

    // Budget exhausted: another gap must not re-read.
    push(workspace, "conversation.message.delta", textDelta("8103", 9, "X"))
    await advance()
    expect(api.listMessages).toHaveBeenCalledTimes(MAX_FORWARD_GAP_REREADS + 1)

    // An external stream.reset restores the budget (and refetches once).
    act(() => {
      publishStreamReset("2001", { reason: "cursor_out_of_range" })
    })
    await waitFor(
      () => expect(api.listMessages).toHaveBeenCalledTimes(MAX_FORWARD_GAP_REREADS + 2),
      { timeout: 2000 },
    )
    await flush()

    push(workspace, "conversation.message.delta", textDelta("8103", 9, "X"))
    await waitFor(
      () => expect(api.listMessages).toHaveBeenCalledTimes(MAX_FORWARD_GAP_REREADS + 3),
      { timeout: 2000 },
    )
  })
})

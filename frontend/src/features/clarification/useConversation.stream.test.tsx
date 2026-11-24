import { act, cleanup, render, screen } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { ConversationMessage } from "./types"

/**
 * SC-D2 end-to-end frontend proof: the workspace SSE owner routes
 * `conversation.message.delta` into the conversation stream projection and
 * `useConversation` renders the accumulated reply incrementally — even though
 * the workspace `liveMessages` handler dedupes by message id (the real
 * RequirementsTab/DesignTab behavior), so the terminal full frame must reach
 * the thread through the projection, not through the deduped prop.
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

import { MessageList } from "./MessageList"
import { useConversation } from "./useConversation"
import { useVersionEvents } from "./useVersionEvents"

const TIMESTAMP = "2026-11-17T10:00:00Z"

let connections: ConnectOptions[] = []

beforeEach(() => {
  connections = []
  api.listMessages.mockReset()
  api.postMessage.mockReset()
  api.listQuestions.mockReset()
  api.listMessages.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
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

function beginFrame(messageId: string): Record<string, unknown> {
  return {
    id: messageId,
    conversation_id: "4001",
    role: "assistant",
    parts: [],
    refs: [],
    created_at: TIMESTAMP,
    stream_state: "streaming",
  }
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

/** Mirrors the real workspace: one SSE owner + liveMessages deduped by id. */
function WorkspaceHarness() {
  const [liveMessages, setLiveMessages] = useState<ConversationMessage[]>([])
  useVersionEvents("2001", {
    onConversationMessage: (message) => {
      setLiveMessages((current) =>
        current.some((existing) => existing.id === message.id) ? current : [...current, message],
      )
    },
  })
  const { messages } = useConversation({ versionId: "2001", conversationId: "4001", liveMessages })
  return (
    <MessageList
      versionId="2001"
      messages={messages}
      activity={null}
      onRequirementSelect={() => {}}
    />
  )
}

/** Mirrors the page-level subscription: NO conversation handler (039). */
function PageLevelHarness() {
  useVersionEvents("2001", { onVersionChanged: () => {} })
  return null
}

function assistantTexts(): string[] {
  const bubble = screen.getByTestId("message-assistant")
  return Array.from(bubble.querySelectorAll('[data-testid="rich-text"]')).map(
    (node) => node.textContent ?? "",
  )
}

describe("useConversation incremental stream rendering (SC-D2)", () => {
  it("accumulates text deltas into one paragraph despite workspace id-dedup", () => {
    render(<WorkspaceHarness />)
    const workspace = connections[0]
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)

    push(workspace, "conversation.message", beginFrame("8001"))
    push(workspace, "conversation.message.delta", textDelta("8001", undefined, "Hel"))
    expect(assistantTexts()).toEqual(["Hel"])

    push(workspace, "conversation.message.delta", textDelta("8001", 3, "lo"))
    expect(assistantTexts()).toEqual(["Hello"])

    // Replayed frame (reconnect): no double append, still one paragraph.
    push(workspace, "conversation.message.delta", textDelta("8001", 3, "lo"))
    expect(assistantTexts()).toEqual(["Hello"])

    // The terminal full frame heals/replaces even though the workspace prop
    // deduped it away (same message id as the Begin placeholder).
    push(workspace, "conversation.message", terminalFrame("8001", "Hello!"))
    expect(assistantTexts()).toEqual(["Hello!"])
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("does not corrupt on an offset gap and heals from the terminal full frame", () => {
    render(<WorkspaceHarness />)
    const workspace = connections[0]

    push(workspace, "conversation.message", beginFrame("8002"))
    push(workspace, "conversation.message.delta", textDelta("8002", undefined, "Hel"))
    // Out-of-order gap: cur=3 < offset=9. The thread keeps what it has.
    push(workspace, "conversation.message.delta", textDelta("8002", 9, "!!!"))
    expect(assistantTexts()).toEqual(["Hel"])

    push(workspace, "conversation.message", terminalFrame("8002", "Hello world!!!"))
    expect(assistantTexts()).toEqual(["Hello world!!!"])
  })

  it("renders thinking increments collapsed; duration arrives via a replace delta", () => {
    render(<WorkspaceHarness />)
    const workspace = connections[0]

    push(workspace, "conversation.message", beginFrame("8003"))
    push(workspace, "conversation.message.delta", {
      message_id: "8003",
      conversation_id: "4001",
      seq: 1,
      part_type: "thinking",
      increment: "核对",
    })
    expect(screen.getByTestId("thinking-block")).toHaveTextContent("思考了 0s")
    expect(screen.queryByText("核对")).not.toBeInTheDocument()

    push(workspace, "conversation.message.delta", {
      message_id: "8003",
      conversation_id: "4001",
      seq: 1,
      part_type: "thinking",
      part: { type: "thinking", text: "核对退款时效", duration_ms: 2000 },
    })
    expect(screen.getByTestId("thinking-block")).toHaveTextContent("思考了 2s")
    // Collapse semantics unchanged: the text stays hidden until expanded.
    expect(screen.queryByText("核对退款时效")).not.toBeInTheDocument()
  })

  it("accumulates once when the same delta arrives on the page-level and workspace connections (039)", () => {
    render(
      <>
        <PageLevelHarness />
        <WorkspaceHarness />
      </>,
    )
    // Two documented-intentional subscriptions: page-level + workspace.
    expect(sse.connectSSE).toHaveBeenCalledTimes(2)
    const [pageLevel, workspace] = connections

    for (const connection of [pageLevel, workspace]) {
      push(connection, "conversation.message", beginFrame("8004"))
    }
    for (const connection of [pageLevel, workspace]) {
      push(connection, "conversation.message.delta", textDelta("8004", undefined, "Hel"))
    }
    for (const connection of [pageLevel, workspace]) {
      push(connection, "conversation.message.delta", textDelta("8004", 3, "lo"))
    }
    // The page-level subscription has no conversation handler: its copy of the
    // increment must NOT accumulate (defect 039 double-accumulation guard).
    expect(assistantTexts()).toEqual(["Hello"])
  })

  /**
   * I-1 user scenario: the reader reloads the page (or opens a second tab) while
   * the Host is still replying. The thread is loaded over HTTP — no Begin frame
   * will ever arrive for that head — and the next increment carries the server's
   * accumulated offset. The reply must CONTINUE from the stored text, not freeze
   * until the turn ends.
   */
  it("resumes an in-flight reply from the HTTP snapshot after a reload (I-1)", async () => {
    const stored = "已经生成的一半"
    api.listMessages.mockResolvedValue({
      items: [
        {
          id: "8005",
          conversation_id: "4001",
          role: "assistant",
          // The REST snapshot carries no per-part seq.
          parts: [{ type: "text", text: stored }],
          refs: [],
          created_at: TIMESTAMP,
          stream_state: "streaming",
        },
      ],
      nextCursor: "",
      hasMore: false,
    })
    render(<WorkspaceHarness />)
    const workspace = connections[0]
    await act(async () => {
      await Promise.resolve()
    })
    expect(assistantTexts()).toEqual([stored])
    expect(screen.getByTestId("message-streaming-badge")).toBeInTheDocument()

    push(workspace, "conversation.message.delta", textDelta("8005", stored.length, "继续"))
    expect(assistantTexts()).toEqual([`${stored}继续`])

    push(workspace, "conversation.message", terminalFrame("8005", `${stored}继续。`))
    expect(assistantTexts()).toEqual([`${stored}继续。`])
    expect(screen.queryByTestId("message-streaming-badge")).not.toBeInTheDocument()
  })

  /**
   * #24 GET/SSE interleave: increments can land before the snapshot response.
   * They must be kept and replayed onto the snapshot, not dropped as gaps.
   */
  it("replays increments that arrived before the HTTP snapshot landed (#24)", async () => {
    let resolveSnapshot: (value: unknown) => void = () => {}
    api.listMessages.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveSnapshot = resolve
        }),
    )
    render(<WorkspaceHarness />)
    const workspace = connections[0]

    push(workspace, "conversation.message.delta", textDelta("8006", 3, "lo"))
    expect(screen.queryByTestId("message-assistant")).not.toBeInTheDocument()

    await act(async () => {
      resolveSnapshot({
        items: [
          {
            id: "8006",
            conversation_id: "4001",
            role: "assistant",
            parts: [{ type: "text", text: "Hel" }],
            refs: [],
            created_at: TIMESTAMP,
            stream_state: "streaming",
          },
        ],
        nextCursor: "",
        hasMore: false,
      })
      await Promise.resolve()
    })
    expect(assistantTexts()).toEqual(["Hello"])

    push(workspace, "conversation.message.delta", textDelta("8006", 5, "!"))
    expect(assistantTexts()).toEqual(["Hello!"])
  })
})

import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { ConversationMessage } from "@/features/clarification/types"
import type { ApiPage } from "@/lib/api-client"

import type {
  ErrorPart as ErrorPartPayload,
  JobCardPart,
  MessagePart,
  ThinkingPart,
  ToolCallPart,
} from "./types"

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
}))
vi.mock("@/features/clarification/api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/features/clarification/api")>()),
  listMessages: api.listMessages,
  postMessage: api.postMessage,
}))

const sse = vi.hoisted(() => ({ connectSSE: vi.fn() }))
vi.mock("@/lib/sse-client", () => ({ connectSSE: sse.connectSSE }))

const apiClient = vi.hoisted(() => ({ apiPost: vi.fn() }))
vi.mock("@/lib/api-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-client")>()),
  apiPost: apiClient.apiPost,
}))

import { MessageList } from "@/features/clarification/MessageList"
import { useConversation } from "@/features/clarification/useConversation"
import { useVersionEvents } from "@/features/clarification/useVersionEvents"

import { ErrorPart } from "./ErrorPart"
import { JobCard } from "./JobCard"
import { ThinkingBlock } from "./ThinkingBlock"
import { ToolCallBlock } from "./ToolCallBlock"

const TIMESTAMP = "2026-11-01T10:00:00Z"

type SequencedPart = MessagePart & { seq?: number }

function assistantMessage(id: string, parts: SequencedPart[]): ConversationMessage {
  return {
    id,
    conversation_id: "4001",
    role: "assistant",
    parts: parts as unknown as ConversationMessage["parts"],
    refs: [],
    created_at: TIMESTAMP,
  }
}

function pageOf(
  items: ConversationMessage[],
  nextCursor = "",
  hasMore = false,
): ApiPage<ConversationMessage> {
  return { items, nextCursor, hasMore }
}

function eventFrame(type: string, payload: Record<string, unknown>): string {
  return JSON.stringify({
    seq: 1,
    channel: "version:2001",
    type,
    scope: { version_id: "2001" },
    payload,
    actor_id: null,
    created_at: TIMESTAMP,
  })
}

type ConnectOptions = {
  url: string
  token: string
  onEvent: (event: { id?: string; event: string; data: string }) => void
  onStatus: (status: string) => void
  onReset?: (data: unknown) => void
}

let connectOptions: ConnectOptions

beforeEach(() => {
  api.listMessages.mockReset()
  api.postMessage.mockReset()
  api.listMessages.mockResolvedValue(pageOf([]))
  apiClient.apiPost.mockReset()
  sse.connectSSE.mockReset()
  sse.connectSSE.mockImplementation((options: ConnectOptions) => {
    connectOptions = options
    return () => {}
  })
})

afterEach(cleanup)

const runningJob: JobCardPart = {
  type: "job_card",
  job_id: "501",
  kind: "design.cases",
  status: "running",
  current: 12,
  total: 27,
}

function ConversationHarness({ liveMessages }: { liveMessages: ConversationMessage[] }) {
  const { messages } = useConversation({
    versionId: "2001",
    conversationId: "4001",
    liveMessages,
  })
  return (
    <MessageList
      versionId="2001"
      messages={messages}
      activity={null}
      onRequirementSelect={() => {}}
    />
  )
}

function JobStreamHarness({ part }: { part: JobCardPart }) {
  useVersionEvents("2001", {})
  return (
    <MessageList
      versionId="2001"
      messages={[assistantMessage("5001", [part])]}
      activity={null}
      onRequirementSelect={() => {}}
    />
  )
}

function emitJobEvent(type: string, payload: Record<string, unknown>): void {
  act(() => {
    connectOptions.onEvent({ event: "message", data: eventFrame(type, payload) })
  })
}

function PaginationHarness({ liveMessages = [] }: { liveMessages?: ConversationMessage[] }) {
  const { messages, hasMore, loadingMore, loadMore } = useConversation({
    versionId: "2001",
    conversationId: "4001",
    liveMessages,
  })
  return (
    <div>
      <span data-testid="message-count">{messages.length}</span>
      <span data-testid="has-more">{String(hasMore)}</span>
      <button type="button" disabled={loadingMore} onClick={() => void loadMore()}>
        加载更多
      </button>
    </div>
  )
}

function LoadMoreHarness() {
  const { messages, hasMore, loadingMore, loadMore, loadMoreError, error } = useConversation({
    versionId: "2001",
    conversationId: "4001",
  })
  return (
    <div>
      <span data-testid="thread-error">{error === null ? "none" : "error"}</span>
      <span data-testid="thread-count">{messages.length}</span>
      <MessageList
        versionId="2001"
        messages={messages}
        activity={null}
        onRequirementSelect={() => {}}
        hasMore={hasMore}
        loadingMore={loadingMore}
        loadMoreError={loadMoreError}
        onLoadMore={() => void loadMore()}
        onRetryLoadMore={() => void loadMore()}
      />
      <textarea data-testid="composer-draft" />
    </div>
  )
}

function SwitchHarness({
  conversationId,
  liveMessages = [],
}: {
  conversationId: string
  liveMessages?: ConversationMessage[]
}) {
  const { messages, hasMore, loadingMore, loadMore, loadMoreError } = useConversation({
    versionId: "2001",
    conversationId,
    liveMessages,
  })
  return (
    <div>
      <span data-testid="switch-count">{messages.length}</span>
      <span data-testid="switch-has-more">{String(hasMore)}</span>
      <span data-testid="switch-load-error">{loadMoreError === null ? "none" : "error"}</span>
      <button type="button" disabled={loadingMore} onClick={() => void loadMore()}>
        加载下一页
      </button>
    </div>
  )
}

function ResetHarness() {
  useVersionEvents("2001", {})
  const { messages } = useConversation({ versionId: "2001", conversationId: "4001" })
  return <span data-testid="thread-count">{messages.length}</span>
}

function SseMessageHarness({
  onConversationMessage,
}: {
  onConversationMessage: (message: ConversationMessage) => void
}) {
  useVersionEvents("2001", { onConversationMessage })
  return null
}

describe("thinking part", () => {
  it("is collapsed by default and reports how long it thought", () => {
    const part: ThinkingPart = {
      type: "thinking",
      text: "先核对退款规则。",
      duration_ms: 3_200,
    }
    render(<ThinkingBlock part={part} />)

    const toggle = screen.getByRole("button", { name: /思考了 3s/ })
    expect(toggle).toHaveAttribute("aria-expanded", "false")
    expect(screen.queryByText("先核对退款规则。")).not.toBeInTheDocument()

    fireEvent.click(toggle)
    expect(toggle).toHaveAttribute("aria-expanded", "true")
    expect(screen.getByText("先核对退款规则。")).toBeInTheDocument()
  })
})

describe("tool call part", () => {
  it("shows the tool name, status and duration with a folded result", () => {
    const part: ToolCallPart = {
      type: "tool_call",
      call_id: "call_1",
      name: "knowledge.search",
      status: "done",
      duration_ms: 1_250,
      arguments: { query: "退款" },
      result: { hits: 2 },
    }
    render(<ToolCallBlock part={part} />)

    expect(screen.getByText("knowledge.search")).toBeInTheDocument()
    expect(screen.getByText("完成")).toBeInTheDocument()
    expect(screen.getByText("1.3s")).toBeInTheDocument()
    // The arguments summary stays visible; the result is folded.
    expect(screen.getByText('{"query":"退款"}')).toBeInTheDocument()
    expect(screen.queryByText(/"hits": 2/)).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: /查看结果/ }))
    expect(screen.getByText(/"hits": 2/)).toBeInTheDocument()
  })
})

describe("job card part", () => {
  it("shows current/total progress and offers cancel while running", async () => {
    const onCancel = vi.fn()
    render(<JobCard versionId="2001" part={runningJob} onCancel={onCancel} />)

    expect(screen.getByText("12/27")).toBeInTheDocument()
    const progress = screen.getByTestId("job-progress")
    expect(progress).toHaveAttribute("aria-valuenow", "12")
    expect(progress).toHaveAttribute("aria-valuemax", "27")

    fireEvent.click(screen.getByRole("button", { name: "取消作业" }))
    await waitFor(() => expect(onCancel).toHaveBeenCalledWith("501"))
  })

  it("cancels through the jobs endpoint when no override is supplied", async () => {
    apiClient.apiPost.mockResolvedValue({ state: "cancelled", current: 12, total: 27 })
    render(<JobCard versionId="2001" part={runningJob} />)

    fireEvent.click(screen.getByRole("button", { name: "取消作业" }))

    await waitFor(() =>
      expect(apiClient.apiPost).toHaveBeenCalledWith("/api/versions/2001/jobs/501/cancel", {}),
    )
    expect(await screen.findByText("已取消")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })

  it("offers retry when the job failed", async () => {
    const onRetry = vi.fn()
    render(
      <JobCard versionId="2001" part={{ ...runningJob, status: "failed" }} onRetry={onRetry} />,
    )

    fireEvent.click(screen.getByRole("button", { name: "重试作业" }))
    await waitFor(() => expect(onRetry).toHaveBeenCalledWith("501"))
  })

  it("keeps the cancelled local state when the parent re-renders", async () => {
    apiClient.apiPost.mockResolvedValue({ state: "cancelled", current: 12, total: 27 })
    const { rerender } = render(<JobCard versionId="2001" part={runningJob} />)

    fireEvent.click(screen.getByRole("button", { name: "取消作业" }))
    expect(await screen.findByText("已取消")).toBeInTheDocument()

    // A parent re-render hands down a fresh but stale snapshot of the same job.
    rerender(<JobCard versionId="2001" part={{ ...runningJob }} />)

    expect(screen.getByText("已取消")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })
})

describe("JobEventConsumer", () => {
  it("applies job.canceled and the cancelled terminal event to a rendered card", async () => {
    render(<JobStreamHarness part={{ ...runningJob, job_id: "601" }} />)
    expect(screen.getByRole("button", { name: "取消作业" })).toBeInTheDocument()

    emitJobEvent("job.canceled", { job_id: "601", kind: "design.cases", status: "cancelled" })

    expect(await screen.findByText("已取消")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()

    // Task 46 emits job.finished{status:"cancelled"} right after job.canceled.
    emitJobEvent("job.finished", {
      job_id: "601",
      kind: "design.cases",
      status: "cancelled",
      result_ref: "",
    })

    expect(screen.getByText("已取消")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })

  it("applies job.requeued and accepts the new owner's progress and terminal", async () => {
    render(
      <JobStreamHarness part={{ ...runningJob, job_id: "602", status: "failed", current: 12 }} />,
    )
    expect(screen.getByRole("button", { name: "重试作业" })).toBeInTheDocument()

    emitJobEvent("job.requeued", { job_id: "602", kind: "design.cases", status: "open" })

    expect(await screen.findByText("排队中")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "重试作业" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "取消作业" })).toBeInTheDocument()

    emitJobEvent("job.progress", {
      job_id: "602",
      kind: "design.cases",
      status: "running",
      current: 1,
      total: 10,
    })
    expect(await screen.findByText("1/10")).toBeInTheDocument()

    emitJobEvent("job.finished", {
      job_id: "602",
      kind: "design.cases",
      status: "done",
      result_ref: "agent_run:9",
    })
    expect(await screen.findByText("完成")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })

  it("revives a cancelled card when job.requeued arrives", async () => {
    render(<JobStreamHarness part={{ ...runningJob, job_id: "603" }} />)

    emitJobEvent("job.canceled", { job_id: "603", kind: "design.cases", status: "cancelled" })
    expect(await screen.findByText("已取消")).toBeInTheDocument()

    emitJobEvent("job.requeued", { job_id: "603", kind: "design.cases", status: "open" })

    expect(await screen.findByText("排队中")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "取消作业" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "重试作业" })).not.toBeInTheDocument()
  })

  it("keeps a cancelled card terminal against a stale old-owner update", async () => {
    render(<JobStreamHarness part={{ ...runningJob, job_id: "604" }} />)

    emitJobEvent("job.canceled", { job_id: "604", kind: "design.cases", status: "cancelled" })
    expect(await screen.findByText("已取消")).toBeInTheDocument()

    // A coalesced in-flight progress from the fenced owner can arrive later.
    emitJobEvent("job.progress", {
      job_id: "604",
      kind: "design.cases",
      status: "running",
      current: 20,
      total: 27,
    })
    emitJobEvent("job.progress", {
      job_id: "604",
      kind: "design.cases",
      status: "failed",
      current: 21,
      total: 27,
    })
    emitJobEvent("job.finished", {
      job_id: "604",
      kind: "design.cases",
      status: "failed",
      result_ref: "",
      error: "lease lost",
    })

    expect(screen.getByText("已取消")).toBeInTheDocument()
    expect(screen.queryByText("运行中")).not.toBeInTheDocument()
    expect(screen.queryByText("失败")).not.toBeInTheDocument()
    // Stale progress counters are discarded too.
    expect(screen.getByText("12/27")).toBeInTheDocument()
    expect(screen.queryByText("21/27")).not.toBeInTheDocument()
  })

  it("keeps a done card terminal against a stale running progress", async () => {
    render(<JobStreamHarness part={{ ...runningJob, job_id: "605" }} />)

    emitJobEvent("job.finished", {
      job_id: "605",
      kind: "design.cases",
      status: "done",
      result_ref: "agent_run:10",
    })
    expect(await screen.findByText("完成")).toBeInTheDocument()

    emitJobEvent("job.progress", {
      job_id: "605",
      kind: "design.cases",
      status: "running",
      current: 9,
      total: 27,
    })

    expect(screen.getByText("完成")).toBeInTheDocument()
    expect(screen.queryByText("运行中")).not.toBeInTheDocument()
    expect(screen.queryByText("9/27")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
  })

  it("keeps a failed card terminal against a stale running progress", async () => {
    render(<JobStreamHarness part={{ ...runningJob, job_id: "606" }} />)

    emitJobEvent("job.finished", {
      job_id: "606",
      kind: "design.cases",
      status: "failed",
      result_ref: "",
      error: "agent failed",
    })
    expect(await screen.findByText("失败")).toBeInTheDocument()

    emitJobEvent("job.progress", {
      job_id: "606",
      kind: "design.cases",
      status: "running",
      current: 9,
      total: 27,
    })

    expect(screen.getByText("失败")).toBeInTheDocument()
    expect(screen.queryByText("运行中")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "重试作业" })).toBeInTheDocument()
  })
})

describe("error part", () => {
  it("renders the error message and its action button", () => {
    const onAction = vi.fn()
    const part: ErrorPartPayload = {
      type: "error",
      code: "model_error",
      message: "模型调用失败，请重试。",
      retryable: true,
      action: "重试",
    }
    render(<ErrorPart part={part} onAction={onAction} />)

    expect(screen.getByText("模型调用失败，请重试。")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(onAction).toHaveBeenCalledWith(part)
  })

  it("does not render a clickable no-op action when no handler is supplied", () => {
    const part: ErrorPartPayload = {
      type: "error",
      code: "model_error",
      message: "模型调用失败，请重试。",
      retryable: true,
      action: "重试",
    }
    render(<ErrorPart part={part} />)

    expect(screen.getByText("模型调用失败，请重试。")).toBeInTheDocument()
    expect(screen.queryByRole("button")).not.toBeInTheDocument()
  })
})

describe("streamed parts in the conversation thread", () => {
  it("merges SSE parts by seq without rendering duplicates", async () => {
    const first = assistantMessage("5001", [{ type: "text", text: "我已读取退款规则。", seq: 1 }])
    const duplicate = assistantMessage("5001", [
      { type: "text", text: "我已读取退款规则。", seq: 1 },
      { type: "thinking", text: "核对退款时效。", duration_ms: 2_000, seq: 2 },
    ])

    const { rerender } = render(<ConversationHarness liveMessages={[]} />)
    await waitFor(() => expect(api.listMessages).toHaveBeenCalledTimes(1))

    rerender(<ConversationHarness liveMessages={[first, duplicate]} />)

    expect(await screen.findAllByText("我已读取退款规则。")).toHaveLength(1)
    expect(screen.getByText("思考了 2s")).toBeInTheDocument()
    expect(screen.queryByText("核对退款时效。")).not.toBeInTheDocument()
  })

  it("normalizes duplicate part seqs at the SSE boundary", () => {
    const onConversationMessage = vi.fn()
    render(<SseMessageHarness onConversationMessage={onConversationMessage} />)

    act(() => {
      connectOptions.onEvent({
        event: "message",
        data: eventFrame("conversation.message", {
          id: "5001",
          conversation_id: "4001",
          role: "assistant",
          parts: [
            { type: "text", text: "重复的片段", seq: 2 },
            { type: "text", text: "重复的片段", seq: 2 },
            { type: "thinking", text: "补充思考", duration_ms: 1_000, seq: 1 },
          ],
          refs: [],
          created_at: TIMESTAMP,
        }),
      })
    })

    expect(onConversationMessage).toHaveBeenCalledTimes(1)
    const message = onConversationMessage.mock.calls[0][0] as ConversationMessage
    expect(message.parts).toHaveLength(2)
    expect(message.parts.map((part) => (part as { seq?: number }).seq)).toEqual([1, 2])
  })

  it("applies job.progress and job.finished SSE events to a rendered job card", async () => {
    render(<JobStreamHarness part={runningJob} />)
    expect(screen.getByText("12/27")).toBeInTheDocument()

    act(() => {
      connectOptions.onEvent({
        event: "message",
        data: eventFrame("job.progress", {
          job_id: "501",
          kind: "design.cases",
          current: 13,
          total: 27,
          status: "running",
        }),
      })
    })
    expect(await screen.findByText("13/27")).toBeInTheDocument()

    act(() => {
      connectOptions.onEvent({
        event: "message",
        data: eventFrame("job.finished", {
          job_id: "501",
          kind: "design.cases",
          status: "done",
          result_ref: "",
        }),
      })
    })
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "取消作业" })).not.toBeInTheDocument(),
    )
    expect(screen.getByText("完成")).toBeInTheDocument()
  })

  it("loads the next history page from the server cursor after a live insert", async () => {
    const firstPage = Array.from({ length: 100 }, (_, index) =>
      assistantMessage(String(index + 1), [{ type: "text", text: `历史消息 ${index + 1}` }]),
    )
    const secondPage = [
      assistantMessage("101", [{ type: "text", text: "历史消息 101" }]),
      assistantMessage("102", [{ type: "text", text: "历史消息 102" }]),
    ]
    api.listMessages
      .mockResolvedValueOnce(pageOf(firstPage, "100", true))
      .mockResolvedValueOnce(pageOf(secondPage, "", false))
    const live = assistantMessage("999", [{ type: "text", text: "实时消息 999" }])

    const { rerender } = render(<PaginationHarness />)
    await waitFor(() => expect(screen.getByTestId("has-more")).toHaveTextContent("true"))
    expect(screen.getByTestId("message-count")).toHaveTextContent("100")

    // A live SSE/optimistic message with a higher id must not move the history
    // cursor; the next page still starts after the last snapshot message.
    rerender(<PaginationHarness liveMessages={[live]} />)
    await waitFor(() => expect(screen.getByTestId("message-count")).toHaveTextContent("101"))

    fireEvent.click(screen.getByRole("button", { name: "加载更多" }))
    await waitFor(() => expect(screen.getByTestId("message-count")).toHaveTextContent("103"))
    expect(screen.getByTestId("has-more")).toHaveTextContent("false")
    expect(api.listMessages).toHaveBeenLastCalledWith("2001", "4001", {
      after: "100",
      limit: 100,
    })
  })

  it("keeps the thread mounted with an inline retry when loadMore fails", async () => {
    const firstPage = Array.from({ length: 100 }, (_, index) =>
      assistantMessage(String(index + 1), [{ type: "text", text: `历史消息 ${index + 1}` }]),
    )
    api.listMessages
      .mockResolvedValueOnce(pageOf(firstPage, "100", true))
      .mockRejectedValueOnce(new Error("page failed"))
      .mockResolvedValueOnce(
        pageOf([assistantMessage("101", [{ type: "text", text: "历史消息 101" }])], "", false),
      )

    render(<LoadMoreHarness />)
    await waitFor(() => expect(screen.getByTestId("thread-count")).toHaveTextContent("100"))

    fireEvent.click(screen.getByRole("button", { name: "加载更多消息" }))

    expect(await screen.findByTestId("load-more-error")).toHaveTextContent("加载更多消息失败")
    expect(screen.getByTestId("thread-error")).toHaveTextContent("none")
    expect(screen.getByTestId("thread-count")).toHaveTextContent("100")
    expect(screen.getByTestId("composer-draft")).toBeInTheDocument()
    expect(screen.getByTestId("message-list")).toBeInTheDocument()

    fireEvent.click(screen.getByRole("button", { name: "重试加载" }))
    await waitFor(() => expect(screen.getByTestId("thread-count")).toHaveTextContent("101"))
    expect(screen.queryByTestId("load-more-error")).not.toBeInTheDocument()
  })

  it("ignores a loadMore result that arrives after switching conversations", async () => {
    let resolvePending: (page: ApiPage<ConversationMessage>) => void = () => {}
    const pending = new Promise<ApiPage<ConversationMessage>>((resolve) => {
      resolvePending = resolve
    })
    const firstPage = Array.from({ length: 100 }, (_, index) =>
      assistantMessage(String(index + 1), [{ type: "text", text: `历史消息 ${index + 1}` }]),
    )
    api.listMessages.mockImplementation(
      (_versionId: string, conversationId: string, options?: { after?: string }) => {
        if (conversationId === "4001" && options?.after === undefined) {
          return Promise.resolve(pageOf(firstPage, "100", true))
        }
        if (conversationId === "4001") return pending
        return Promise.resolve(
          pageOf([assistantMessage("7001", [{ type: "text", text: "新对话消息" }])], "", false),
        )
      },
    )

    const { rerender } = render(<SwitchHarness conversationId="4001" />)
    await waitFor(() => expect(screen.getByTestId("switch-has-more")).toHaveTextContent("true"))
    fireEvent.click(screen.getByRole("button", { name: "加载下一页" }))

    rerender(<SwitchHarness conversationId="4002" />)
    await waitFor(() => expect(screen.getByTestId("switch-count")).toHaveTextContent("1"))

    await act(async () => {
      resolvePending(
        pageOf([assistantMessage("101", [{ type: "text", text: "旧对话下一页" }])], "", false),
      )
      await pending
    })

    expect(screen.getByTestId("switch-count")).toHaveTextContent("1")
    expect(screen.getByTestId("switch-load-error")).toHaveTextContent("none")
  })

  it("refetches the conversation snapshot on stream.reset", async () => {
    api.listMessages
      .mockResolvedValueOnce(
        pageOf([assistantMessage("5001", [{ type: "text", text: "首条消息" }])]),
      )
      .mockResolvedValueOnce(
        pageOf([
          assistantMessage("5001", [{ type: "text", text: "首条消息" }]),
          assistantMessage("5002", [{ type: "text", text: "重置后消息" }]),
        ]),
      )

    render(<ResetHarness />)
    await waitFor(() => expect(screen.getByTestId("thread-count")).toHaveTextContent("1"))

    act(() => {
      connectOptions.onReset?.({ reason: "cursor_too_old", snapshot_cursor: 99 })
    })

    await waitFor(() => expect(screen.getByTestId("thread-count")).toHaveTextContent("2"))
    expect(api.listMessages).toHaveBeenCalledTimes(2)
  })
})

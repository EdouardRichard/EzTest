import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { ApiError } from "@/lib/api-client"
import type { SSEStatus } from "@/lib/sse-client"

const mocks = vi.hoisted(() => ({
  listClarificationMessages: vi.fn(),
  sendClarificationMessage: vi.fn(),
  startClarificationSession: vi.fn(),
  analyzeClarification: vi.fn(),
  reconClarification: vi.fn(),
}))

vi.mock("./api", () => ({
  listClarificationMessages: mocks.listClarificationMessages,
  sendClarificationMessage: mocks.sendClarificationMessage,
  startClarificationSession: mocks.startClarificationSession,
  analyzeClarification: mocks.analyzeClarification,
  reconClarification: mocks.reconClarification,
}))

import { ClarificationChat } from "./ClarificationChat"
import type {
  ClarificationMessage,
  ClarificationMessagesPage,
  ClarificationSession,
  VersionEvent,
} from "./types"

const VERSION_ID = "1844674407370955161"

const ACTIVE_SESSION: ClarificationSession = {
  id: "1844674407370957001",
  status: "active",
  agentSessionId: 77,
  startedAt: "2026-10-27T10:00:00Z",
  endedAt: null,
  contextSummary: "",
  summaryUpdatedAt: null,
}

function message(
  overrides: Partial<ClarificationMessage> & Pick<ClarificationMessage, "id" | "role" | "parts">,
): ClarificationMessage {
  return {
    reasoning: "",
    toolCalls: null,
    toolName: null,
    runId: 501,
    createdAt: "2026-10-27T10:00:00Z",
    ...overrides,
  }
}

const USER_MESSAGE = message({
  id: 11,
  role: "user",
  parts: [{ type: "text", text: "订单创建需要哪些字段？" }],
  runId: 500,
  createdAt: "2026-10-27T10:00:00Z",
})

const ASSISTANT_MESSAGE = message({
  id: 12,
  role: "assistant",
  parts: [{ type: "text", text: "请确认是否需要收货地址。" }],
  createdAt: "2026-10-27T10:00:05Z",
})

/** P1 persists an empty assistant placeholder before the run streams content. */
const PLACEHOLDER_MESSAGE = message({
  id: 101,
  role: "assistant",
  parts: [],
  createdAt: "2026-10-27T10:00:05Z",
})

/** The same placeholder after the run finished and P1 persisted the answer. */
const STREAMED_ANSWER = message({
  id: 101,
  role: "assistant",
  parts: [{ type: "text", text: "请确认收货地址是否必填，并补充联系人。" }],
  createdAt: "2026-10-27T10:00:05Z",
})

const ASSISTANT_WITH_TOOL_CALLS = message({
  id: 20,
  role: "assistant",
  parts: [],
  toolCalls: [
    { id: "call_a", name: "tool_a", arguments: '{"a":1}' },
    { id: "call_b", name: "tool_b", arguments: '{"b":2}' },
  ],
  createdAt: "2026-10-27T10:00:01Z",
})

const TOOL_RESULT_A = message({
  id: 21,
  role: "tool",
  parts: [{ type: "text", text: "结果一" }],
  toolName: "tool_a",
  createdAt: "2026-10-27T10:00:02Z",
})

const TOOL_RESULT_B = message({
  id: 22,
  role: "tool",
  parts: [{ type: "text", text: "结果二" }],
  toolName: "tool_b",
  createdAt: "2026-10-27T10:00:03Z",
})

/**
 * The runner bounds `agent.activity` previews to 2048 bytes (`args_truncated`/
 * `result_truncated`), while the persisted message keeps the full content.
 */
const FULL_TOOL_ARGS = JSON.stringify({ query: "a".repeat(3000) })
const FULL_TOOL_RESULT = `完整结果：${"r".repeat(3000)}`
const TRUNCATED_TOOL_ARGS = FULL_TOOL_ARGS.slice(0, 2048)
const TRUNCATED_TOOL_RESULT = FULL_TOOL_RESULT.slice(0, 2048)

const ASSISTANT_WITH_LONG_TOOL_CALL = message({
  id: 30,
  role: "assistant",
  parts: [],
  toolCalls: [{ id: "call_long", name: "tool_long", arguments: FULL_TOOL_ARGS }],
  createdAt: "2026-10-27T10:00:10Z",
})

const LONG_TOOL_RESULT = message({
  id: 31,
  role: "tool",
  parts: [{ type: "text", text: FULL_TOOL_RESULT }],
  toolName: "tool_long",
  createdAt: "2026-10-27T10:00:11Z",
})

function longToolEvents(): VersionEvent[] {
  return [
    activity(40, {
      kind: "tool_use",
      run_id: "501",
      call_id: "call_long",
      tool: "tool_long",
      args: TRUNCATED_TOOL_ARGS,
      args_truncated: true,
    }),
    activity(41, {
      kind: "tool_result",
      run_id: "501",
      call_id: "call_long",
      tool: "tool_long",
      result: TRUNCATED_TOOL_RESULT,
      result_truncated: true,
      status: "ok",
      duration_ms: 8,
    }),
  ]
}

function messagesPage(items: ClarificationMessage[]): ClarificationMessagesPage {
  return { items, nextCursor: "", hasMore: false }
}

function activity(
  seq: number,
  detail: Record<string, unknown>,
  overrides: Partial<VersionEvent> = {},
): VersionEvent {
  return {
    seq,
    channel: `version:${VERSION_ID}`,
    type: "agent.activity",
    version_id: VERSION_ID,
    batch_id: null,
    case_id: null,
    step_id: null,
    agent_role: "clarification-dialog",
    session_id: "77",
    ts: new Date(Date.UTC(2026, 9, 27, 10, 0, seq)).toISOString(),
    summary: "",
    detail,
    refs: { evidence: [], case: [], requirement: [], code: [] },
    ...overrides,
  }
}

type RenderChatOptions = {
  session?: ClarificationSession | null
  sessionLoading?: boolean
  events?: VersionEvent[]
  status?: SSEStatus
  onChanged?: () => void
  onReconnect?: () => void
}

function chatElement(options: RenderChatOptions = {}) {
  const onChanged = options.onChanged ?? vi.fn()
  return (
    <ClarificationChat
      versionId={VERSION_ID}
      session={options.session === undefined ? ACTIVE_SESSION : options.session}
      sessionLoading={options.sessionLoading ?? false}
      events={options.events ?? []}
      status={options.status ?? "live"}
      onChanged={onChanged}
      onReconnect={options.onReconnect ?? vi.fn()}
    />
  )
}

function renderChat(options: RenderChatOptions = {}) {
  return render(chatElement(options))
}

function aiUnavailableError(): ApiError {
  return new ApiError({
    status: 503,
    code: "unavailable",
    message: "AI service is unavailable",
    retryable: true,
    details: { reason: "ai_unavailable" },
  })
}

describe("ClarificationChat", () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset()
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))
    mocks.sendClarificationMessage.mockResolvedValue({
      sessionId: ACTIVE_SESSION.id,
      agentSessionId: 77,
      runId: 501,
      status: "running",
    })
    mocks.startClarificationSession.mockResolvedValue(ACTIVE_SESSION)
    mocks.analyzeClarification.mockResolvedValue({ agentSessionId: 77, runId: 501 })
    mocks.reconClarification.mockResolvedValue({ agentSessionId: 77, runId: 502 })
  })

  afterEach(cleanup)

  it("restores the conversation from the persisted message snapshot", async () => {
    mocks.listClarificationMessages.mockResolvedValue(
      messagesPage([USER_MESSAGE, ASSISTANT_MESSAGE]),
    )

    renderChat()

    expect(await screen.findByText("订单创建需要哪些字段？")).toBeTruthy()
    expect(screen.getByText("请确认是否需要收货地址。")).toBeTruthy()
    expect(mocks.listClarificationMessages).toHaveBeenCalledWith(VERSION_ID, { limit: 50 })
    expect(screen.getByText("实时连接")).toBeTruthy()
  })

  it("shows the empty conversation state after loading with no messages", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))

    renderChat()

    expect(await screen.findByText("暂无消息")).toBeTruthy()
    expect(screen.getByText("发送第一条澄清消息开始对话。")).toBeTruthy()
    expect((screen.getByLabelText("消息内容") as HTMLTextAreaElement).disabled).toBe(false)
  })

  it("does not duplicate a restored message that also appears in the event snapshot", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([ASSISTANT_MESSAGE]))

    renderChat({
      events: [
        activity(4, {
          kind: "text",
          message_id: 12,
          part: 0,
          text: "请确认是否需要收货地址。",
        }),
      ],
    })

    await screen.findByText("请确认是否需要收货地址。")
    expect(screen.getAllByText("请确认是否需要收货地址。").length).toBe(1)
  })

  it("sends a message, keeps it visible, and folds live text and tool activity", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([USER_MESSAGE]))
    const { rerender } = renderChat()
    await screen.findByText("订单创建需要哪些字段？")

    const input = screen.getByLabelText("消息内容") as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: "收货地址是否必填？" } })
    fireEvent.click(screen.getByRole("button", { name: "发送" }))

    await waitFor(() =>
      expect(mocks.sendClarificationMessage).toHaveBeenCalledWith(VERSION_ID, "收货地址是否必填？"),
    )
    expect(input.value).toBe("")
    await waitFor(() => expect(screen.getByText("收货地址是否必填？")).toBeTruthy())

    rerender(
      chatElement({
        events: [
          activity(10, { kind: "text", message_id: 101, part: 0, text: "请确认", run_id: "501" }),
          activity(11, { kind: "text", message_id: 101, part: 0, text: "收货地址。" }),
          activity(12, {
            kind: "tool_use",
            message_id: 101,
            call_id: "call_1",
            tool: "version.context_get",
            args: '{"versionId":"1844674407370955161"}',
            run_id: "501",
          }),
          activity(13, {
            kind: "tool_result",
            message_id: 102,
            call_id: "call_1",
            tool: "version.context_get",
            result: '{"ok":true}',
            status: "ok",
            duration_ms: 12,
            run_id: "501",
          }),
        ],
      }),
    )

    // Two text chunks of the same part are folded into one assistant bubble.
    expect(await screen.findByText("请确认收货地址。")).toBeTruthy()
    const toolButton = screen.getByRole("button", { name: "工具 version.context_get" })
    expect(toolButton.getAttribute("aria-expanded")).toBe("false")
    expect(screen.queryByText(/versionId/)).toBeNull()

    fireEvent.click(toolButton)
    expect(
      screen
        .getByRole("button", { name: "工具 version.context_get" })
        .getAttribute("aria-expanded"),
    ).toBe("true")
    expect(screen.getByText(/versionId/)).toBeTruthy()
    expect(screen.getByText(/"ok":true/)).toBeTruthy()
  })

  it("folds live thinking behind a toggle", async () => {
    renderChat({
      events: [
        activity(5, { kind: "thinking", message_id: 103, part: 0, text: "先核对需求上下文" }),
      ],
    })

    const toggle = await screen.findByRole("button", { name: "思考" })
    expect(screen.queryByText("先核对需求上下文")).toBeNull()
    fireEvent.click(toggle)
    expect(screen.getByText("先核对需求上下文")).toBeTruthy()
  })

  it("shows the reconnecting state and never claims the chat is empty", async () => {
    const onReconnect = vi.fn()
    renderChat({ status: "reconnecting", onReconnect })

    expect(await screen.findByText("重连中…")).toBeTruthy()
    expect(screen.getByText("连接中断，正在尝试恢复；对话内容不会丢失。")).toBeTruthy()
    expect(screen.queryByText("暂无消息")).toBeNull()

    fireEvent.click(screen.getByRole("button", { name: "重新连接" }))
    expect(onReconnect).toHaveBeenCalledTimes(1)
  })

  it("keeps the input and offers an AI retry when sending hits a 503", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))
    mocks.sendClarificationMessage
      .mockRejectedValueOnce(aiUnavailableError())
      .mockResolvedValueOnce({
        sessionId: ACTIVE_SESSION.id,
        agentSessionId: 77,
        runId: 501,
        status: "running",
      })

    renderChat()
    const input = screen.getByLabelText("消息内容") as HTMLTextAreaElement
    fireEvent.change(input, { target: { value: "库存如何扣减？" } })
    fireEvent.click(screen.getByRole("button", { name: "发送" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("AI 服务暂不可用")
    expect(input.value).toBe("库存如何扣减？")

    fireEvent.click(within(alert).getByRole("button", { name: "重试 AI" }))
    await waitFor(() => expect(mocks.sendClarificationMessage).toHaveBeenCalledTimes(2))
    expect(mocks.sendClarificationMessage).toHaveBeenLastCalledWith(VERSION_ID, "库存如何扣减？")
    await waitFor(() => expect(input.value).toBe(""))
    expect(screen.queryByText(/AI 服务暂不可用/)).toBeNull()
  })

  it("disables analyze and recon while an agent run is active", async () => {
    const running = [
      activity(6, { kind: "run_started", run_id: "501", profile_key: "clarification-dialog" }),
    ]
    const { rerender } = renderChat({ events: running })

    const analyze = await screen.findByRole("button", { name: "需求分析" })
    const recon = screen.getByRole("button", { name: "上下文侦察" })
    expect((analyze as HTMLButtonElement).disabled).toBe(true)
    expect((recon as HTMLButtonElement).disabled).toBe(true)

    rerender(
      chatElement({
        events: [...running, activity(7, { kind: "run_finished", run_id: "501", status: "ok" })],
      }),
    )
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "需求分析" }) as HTMLButtonElement).disabled).toBe(
        false,
      ),
    )
    expect((screen.getByRole("button", { name: "上下文侦察" }) as HTMLButtonElement).disabled).toBe(
      false,
    )
  })

  it("degrades the AI actions after a 503 while keeping manual input available", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))
    mocks.analyzeClarification.mockRejectedValueOnce(aiUnavailableError())

    renderChat()
    fireEvent.click(await screen.findByRole("button", { name: "需求分析" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("AI 服务暂不可用")
    await waitFor(() => expect(mocks.analyzeClarification).toHaveBeenCalledWith(VERSION_ID))
    expect((screen.getByRole("button", { name: "上下文侦察" }) as HTMLButtonElement).disabled).toBe(
      true,
    )
    expect((screen.getByLabelText("消息内容") as HTMLTextAreaElement).disabled).toBe(false)

    fireEvent.click(within(alert).getByRole("button", { name: "重试 AI" }))
    await waitFor(() => expect(mocks.analyzeClarification).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.queryByText(/AI 服务暂不可用/)).toBeNull())
  })

  it("keeps the chat usable and retries the message snapshot after a load failure", async () => {
    mocks.listClarificationMessages
      .mockRejectedValueOnce(new ApiError({ status: 500, code: "internal", message: "boom" }))
      .mockResolvedValueOnce(messagesPage([USER_MESSAGE]))

    renderChat()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载对话失败")
    fireEvent.click(within(alert).getByRole("button", { name: "重试" }))
    expect(await screen.findByText("订单创建需要哪些字段？")).toBeTruthy()
    expect(mocks.listClarificationMessages).toHaveBeenCalledTimes(2)
  })

  it("starts a clarification session when none is active", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))
    const onChanged = vi.fn()
    renderChat({ session: null, onChanged })

    expect(await screen.findByText("尚未开始澄清会话。")).toBeTruthy()
    expect((screen.getByLabelText("消息内容") as HTMLTextAreaElement).disabled).toBe(true)

    fireEvent.click(screen.getByRole("button", { name: "开始澄清会话" }))
    await waitFor(() => expect(mocks.startClarificationSession).toHaveBeenCalledWith(VERSION_ID))
    await waitFor(() =>
      expect((screen.getByLabelText("消息内容") as HTMLTextAreaElement).disabled).toBe(false),
    )
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it("renders live answer chunks into an empty persisted assistant placeholder", async () => {
    mocks.listClarificationMessages.mockResolvedValue(
      messagesPage([USER_MESSAGE, PLACEHOLDER_MESSAGE]),
    )

    renderChat({
      events: [
        activity(10, { kind: "text", message_id: 101, part: 0, text: "请确认收货地址" }),
        activity(11, { kind: "text", message_id: 101, part: 0, text: "是否必填。" }),
      ],
    })

    // Wait for the snapshot (the persisted user message) before asserting the
    // placeholder's live answer: the live chunks must survive that snapshot.
    expect(await screen.findByText("订单创建需要哪些字段？")).toBeTruthy()
    expect(await screen.findByText("请确认收货地址是否必填。")).toBeTruthy()
  })

  it("refetches the snapshot when a run finishes so the persisted answer replaces live chunks", async () => {
    mocks.listClarificationMessages
      .mockResolvedValueOnce(messagesPage([USER_MESSAGE, PLACEHOLDER_MESSAGE]))
      .mockResolvedValueOnce(messagesPage([USER_MESSAGE, STREAMED_ANSWER]))

    const liveChunks = [
      activity(10, { kind: "text", message_id: 101, part: 0, text: "请确认收货地址" }),
      activity(11, { kind: "text", message_id: 101, part: 0, text: "是否必填。" }),
    ]
    const { rerender } = renderChat({ events: liveChunks })

    expect(await screen.findByText("请确认收货地址是否必填。")).toBeTruthy()
    expect(mocks.listClarificationMessages).toHaveBeenCalledTimes(1)

    rerender(
      chatElement({
        events: [
          ...liveChunks,
          activity(12, { kind: "run_finished", run_id: "501", status: "done" }),
        ],
      }),
    )

    await waitFor(() => expect(mocks.listClarificationMessages).toHaveBeenCalledTimes(2))
    expect(await screen.findByText("请确认收货地址是否必填，并补充联系人。")).toBeTruthy()
    // The persisted message is authoritative now; the live chunk rows are gone.
    expect(screen.queryByText("请确认收货地址是否必填。")).toBeNull()
  })

  it("does not duplicate the optimistic echo when a finished run refetches the snapshot", async () => {
    const persistedUser = message({
      id: 11,
      role: "user",
      parts: [{ type: "text", text: "收货地址是否必填？" }],
      runId: 500,
      createdAt: "2026-10-27T10:00:00Z",
    })
    mocks.listClarificationMessages
      .mockResolvedValueOnce(messagesPage([]))
      .mockResolvedValueOnce(messagesPage([persistedUser, STREAMED_ANSWER]))

    const { rerender } = renderChat()
    expect(await screen.findByText("暂无消息")).toBeTruthy()

    fireEvent.change(screen.getByLabelText("消息内容"), {
      target: { value: "收货地址是否必填？" },
    })
    fireEvent.click(screen.getByRole("button", { name: "发送" }))
    await waitFor(() =>
      expect(mocks.sendClarificationMessage).toHaveBeenCalledWith(VERSION_ID, "收货地址是否必填？"),
    )
    expect(await screen.findByText("收货地址是否必填？")).toBeTruthy()

    rerender(
      chatElement({
        events: [activity(12, { kind: "run_finished", run_id: "501", status: "done" })],
      }),
    )

    await waitFor(() => expect(mocks.listClarificationMessages).toHaveBeenCalledTimes(2))
    expect(await screen.findByText("请确认收货地址是否必填，并补充联系人。")).toBeTruthy()
    // The snapshot now carries the persisted user message: the optimistic echo
    // must be dropped instead of rendering the same text twice.
    expect(screen.getAllByText("收货地址是否必填？").length).toBe(1)
  })

  it("pairs persisted tool results with tool calls in call order", async () => {
    mocks.listClarificationMessages.mockResolvedValue(
      messagesPage([ASSISTANT_WITH_TOOL_CALLS, TOOL_RESULT_A, TOOL_RESULT_B]),
    )

    renderChat()

    const rowA = (await screen.findByRole("button", { name: "工具 tool_a" })).parentElement
    const rowB = screen.getByRole("button", { name: "工具 tool_b" }).parentElement
    expect(rowA).not.toBeNull()
    expect(rowB).not.toBeNull()
    if (rowA === null || rowB === null) return

    fireEvent.click(within(rowA).getByRole("button", { name: "工具 tool_a" }))
    fireEvent.click(within(rowB).getByRole("button", { name: "工具 tool_b" }))

    expect(within(rowA).getByText("结果一")).toBeTruthy()
    expect(within(rowB).getByText("结果二")).toBeTruthy()
    expect(within(rowA).queryByText("结果二")).toBeNull()
    expect(within(rowB).queryByText("结果一")).toBeNull()
  })

  it("keys persisted tool calls per run so equal call ids do not cross-attach", async () => {
    const ASSISTANT_RUN_501 = message({
      id: 40,
      role: "assistant",
      parts: [],
      toolCalls: [{ id: "call_shared", name: "tool_shared", arguments: '{"run":501}' }],
      runId: 501,
      createdAt: "2026-10-27T10:00:10Z",
    })
    const ASSISTANT_RUN_502 = message({
      id: 41,
      role: "assistant",
      parts: [],
      toolCalls: [{ id: "call_shared", name: "tool_shared", arguments: '{"run":502}' }],
      runId: 502,
      createdAt: "2026-10-27T10:00:20Z",
    })
    mocks.listClarificationMessages.mockResolvedValue(
      messagesPage([ASSISTANT_RUN_501, ASSISTANT_RUN_502]),
    )

    const { rerender } = renderChat()
    // Wait for the persisted snapshot before attaching events so the rows below
    // are the restored calls, not live-only placeholders.
    expect(await screen.findAllByRole("button", { name: "工具 tool_shared" })).toHaveLength(2)

    rerender(
      chatElement({
        events: [
          activity(50, {
            kind: "tool_result",
            run_id: "501",
            call_id: "call_shared",
            tool: "tool_shared",
            result: "结果一",
            status: "ok",
          }),
          activity(51, {
            kind: "tool_result",
            run_id: "502",
            call_id: "call_shared",
            tool: "tool_shared",
            result: "结果二",
            status: "ok",
          }),
        ],
      }),
    )

    const buttons = screen.getAllByRole("button", { name: "工具 tool_shared" })
    expect(buttons.length).toBe(2)
    const row501 = buttons[0].parentElement
    const row502 = buttons[1].parentElement
    expect(row501).not.toBeNull()
    expect(row502).not.toBeNull()
    if (row501 === null || row502 === null) return

    fireEvent.click(buttons[0])
    fireEvent.click(buttons[1])

    expect(within(row501).getByText("结果一")).toBeTruthy()
    expect(within(row501).queryByText("结果二")).toBeNull()
    expect(within(row502).getByText("结果二")).toBeTruthy()
    expect(within(row502).queryByText("结果一")).toBeNull()
  })

  it("falls back to the call id when the persisted message has no run id", async () => {
    const ASSISTANT_NO_RUN = message({
      id: 42,
      role: "assistant",
      parts: [],
      toolCalls: [{ id: "call_no_run", name: "tool_no_run", arguments: '{"x":1}' }],
      runId: null,
      createdAt: "2026-10-27T10:00:10Z",
    })
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([ASSISTANT_NO_RUN]))

    const { rerender } = renderChat()
    expect(await screen.findAllByRole("button", { name: "工具 tool_no_run" })).toHaveLength(1)

    rerender(
      chatElement({
        events: [
          activity(60, {
            kind: "tool_result",
            run_id: "501",
            call_id: "call_no_run",
            tool: "tool_no_run",
            result: "结果无运行 ID",
            status: "ok",
          }),
        ],
      }),
    )

    // The persisted call carries no run id, so the bare call id is the only
    // possible key; the result must attach instead of creating a duplicate row.
    const buttons = screen.getAllByRole("button", { name: "工具 tool_no_run" })
    expect(buttons.length).toBe(1)
    fireEvent.click(buttons[0])
    expect(screen.getByText("结果无运行 ID")).toBeTruthy()
  })

  it("keeps the full persisted args and result when a truncated event preview arrives", async () => {
    mocks.listClarificationMessages.mockResolvedValue(
      messagesPage([ASSISTANT_WITH_LONG_TOOL_CALL, LONG_TOOL_RESULT]),
    )

    const { rerender } = renderChat()
    expect(await screen.findByRole("button", { name: "工具 tool_long" })).toBeTruthy()

    rerender(chatElement({ events: longToolEvents() }))

    fireEvent.click(screen.getByRole("button", { name: "工具 tool_long" }))

    expect(screen.getByText(FULL_TOOL_ARGS)).toBeTruthy()
    expect(screen.getByText(FULL_TOOL_RESULT)).toBeTruthy()
    // A persisted full value is authoritative: no truncation indicator claims
    // the visible content was silently shortened.
    expect(screen.queryByText("参数已截断，仅显示预览。")).toBeNull()
    expect(screen.queryByText("结果已截断，仅显示预览。")).toBeNull()
  })

  it("marks truncated previews for a live-only tool call", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))

    const { rerender } = renderChat()
    expect(await screen.findByText("暂无消息")).toBeTruthy()

    rerender(chatElement({ events: longToolEvents() }))

    fireEvent.click(screen.getByRole("button", { name: "工具 tool_long" }))

    expect(screen.getByText(TRUNCATED_TOOL_ARGS)).toBeTruthy()
    expect(screen.getByText(TRUNCATED_TOOL_RESULT)).toBeTruthy()
    expect(screen.getByText("参数已截断，仅显示预览。")).toBeTruthy()
    expect(screen.getByText("结果已截断，仅显示预览。")).toBeTruthy()
  })

  it("refetches the message snapshot when the stream reconnects", async () => {
    mocks.listClarificationMessages
      .mockResolvedValueOnce(messagesPage([USER_MESSAGE]))
      .mockResolvedValueOnce(messagesPage([USER_MESSAGE, ASSISTANT_MESSAGE]))

    const { rerender } = renderChat({ status: "reconnecting" })
    expect(await screen.findByText("订单创建需要哪些字段？")).toBeTruthy()
    expect(mocks.listClarificationMessages).toHaveBeenCalledTimes(1)

    rerender(chatElement({ status: "live" }))

    expect(await screen.findByText("请确认是否需要收货地址。")).toBeTruthy()
    expect(mocks.listClarificationMessages).toHaveBeenCalledTimes(2)
  })

  it("surfaces a failed run status instead of finishing silently", async () => {
    renderChat({
      events: [
        activity(6, {
          kind: "run_finished",
          run_id: "501",
          status: "failed",
          terminal_reason: "model_error",
          error_code: "llm_error",
        }),
      ],
    })

    expect(await screen.findByText(/运行出错/)).toBeTruthy()
    expect(screen.getByText(/model_error/)).toBeTruthy()
    expect(screen.getByText(/llm_error/)).toBeTruthy()
  })

  it("folds usage activity into one expandable summary", async () => {
    renderChat({
      events: [
        activity(5, {
          kind: "usage",
          run_id: "501",
          turn: 1,
          prompt_tokens: 10,
          completion_tokens: 4,
          reasoning_tokens: 0,
          total_tokens: 14,
        }),
        activity(6, {
          kind: "usage",
          run_id: "501",
          turn: 2,
          prompt_tokens: 20,
          completion_tokens: 8,
          reasoning_tokens: 2,
          total_tokens: 30,
        }),
      ],
    })

    const toggle = await screen.findByRole("button", { name: "用量" })
    expect(screen.queryByText("共 44 tokens")).toBeNull()

    fireEvent.click(toggle)

    expect(screen.getByText("共 44 tokens")).toBeTruthy()
    expect(screen.getByText("输入 30 · 输出 12 · 推理 2")).toBeTruthy()
  })

  it("keys live tool activity per run so equal call ids do not collide", async () => {
    renderChat({
      events: [
        activity(20, {
          kind: "tool_use",
          run_id: "501",
          call_id: "call_1",
          tool: "version.context_get",
          args: '{"run":1}',
        }),
        activity(21, {
          kind: "tool_result",
          run_id: "501",
          call_id: "call_1",
          tool: "version.context_get",
          result: "结果一",
          status: "ok",
        }),
        activity(30, {
          kind: "tool_use",
          run_id: "502",
          call_id: "call_1",
          tool: "version.context_get",
          args: '{"run":2}',
        }),
        activity(31, {
          kind: "tool_result",
          run_id: "502",
          call_id: "call_1",
          tool: "version.context_get",
          result: "结果二",
          status: "ok",
        }),
      ],
    })

    const buttons = await screen.findAllByRole("button", { name: "工具 version.context_get" })
    expect(buttons.length).toBe(2)

    fireEvent.click(buttons[0])
    expect(screen.getByText("结果一")).toBeTruthy()
    expect(screen.queryByText("结果二")).toBeNull()

    fireEvent.click(buttons[1])
    expect(screen.getByText("结果二")).toBeTruthy()
  })

  it("maps an active-session conflict to a friendly message", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))
    mocks.startClarificationSession.mockRejectedValueOnce(
      new ApiError({
        status: 409,
        code: "conflict",
        message: "version already has an active session",
        details: { reason: "clarification_session_active" },
      }),
    )

    renderChat({ session: null })
    fireEvent.click(await screen.findByRole("button", { name: "开始澄清会话" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("已有进行中的澄清会话")
  })

  it("maps requirements_required to a friendly message", async () => {
    mocks.listClarificationMessages.mockResolvedValue(messagesPage([]))
    mocks.startClarificationSession.mockRejectedValueOnce(
      new ApiError({
        status: 422,
        code: "invalid_argument",
        message: "at least one requirement is required",
        details: { reason: "requirements_required" },
      }),
    )

    renderChat({ session: null })
    fireEvent.click(await screen.findByRole("button", { name: "开始澄清会话" }))

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("开始澄清前请至少录入一条需求")
  })
})

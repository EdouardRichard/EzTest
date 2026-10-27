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
})

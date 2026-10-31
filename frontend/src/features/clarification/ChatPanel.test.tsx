import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { ApiError } from "@/lib/api-client"

import {
  confirmedRequirementDetailFixture,
  conversationFixture,
  conversationSecondFixture,
  messageFixture,
  requirementCardMessageFixture,
  rollbackProposalMessageFixture,
  userMessageFixture,
} from "./test-fixtures"

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  rollbackRequirement: vi.fn(),
  uploadContent: vi.fn(),
}))
vi.mock("./api", () => api)

import { ChatPanel } from "./ChatPanel"

function renderPanel(overrides: Partial<Parameters<typeof ChatPanel>[0]> = {}) {
  return render(
    <ChatPanel
      versionId="2001"
      conversationId="4001"
      conversations={[conversationFixture, conversationSecondFixture]}
      onSelectConversation={vi.fn()}
      onCreateConversation={vi.fn()}
      onRequirementSelect={vi.fn()}
      liveMessages={[]}
      activity={null}
      {...overrides}
    />,
  )
}

describe("ChatPanel", () => {
  beforeEach(() => {
    api.listMessages.mockReset()
    api.postMessage.mockReset()
    api.rollbackRequirement.mockReset()
    api.uploadContent.mockReset()
    api.listMessages.mockResolvedValue([messageFixture, userMessageFixture])
  })

  afterEach(cleanup)

  it("loads and renders the active conversation messages, attachments and header", async () => {
    renderPanel()

    expect(await screen.findByText("我已载入版本上下文，请描述需求。")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledWith("2001", "4001")
    expect(screen.getByText("需求澄清 · 与澄清团队对话")).toBeInTheDocument()
    expect(screen.getByText("新增退款时效：审核通过后 T+1 到账。")).toBeInTheDocument()
    expect(screen.getByText("退款规则V2.pdf")).toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toBeInTheDocument()
  })

  it("shows an empty conversation state with the composer", async () => {
    api.listMessages.mockResolvedValue([])
    renderPanel()

    expect(await screen.findByText("暂无消息")).toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toBeInTheDocument()
  })

  it("sends a message, posts parts and appends the returned message", async () => {
    const sent = {
      ...userMessageFixture,
      id: "5010",
      parts: [{ type: "text" as const, text: "新增退款时效需求" }],
    }
    api.postMessage.mockResolvedValue(sent)
    const onRequirementSelect = vi.fn()
    renderPanel({ onRequirementSelect })

    await screen.findByText("我已载入版本上下文，请描述需求。")
    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "新增退款时效需求" } })
    fireEvent.keyDown(textarea, { key: "Enter" })

    await waitFor(() => expect(api.postMessage).toHaveBeenCalledTimes(1))
    const [versionId, conversationId, payload] = api.postMessage.mock.calls[0]
    expect(versionId).toBe("2001")
    expect(conversationId).toBe("4001")
    expect(payload.parts).toEqual([{ type: "text", text: "新增退款时效需求" }])
    expect(payload.client_message_id.length).toBeGreaterThan(0)
    expect(payload.client_message_id.length).toBeLessThanOrEqual(64)
    expect(await screen.findByText("新增退款时效需求")).toBeInTheDocument()
    expect(onRequirementSelect).not.toHaveBeenCalled()
  })

  it("merges live SSE messages without refetching the thread", async () => {
    const { rerender } = renderPanel()
    await screen.findByText("我已载入版本上下文，请描述需求。")
    expect(api.listMessages).toHaveBeenCalledTimes(1)

    rerender(
      <ChatPanel
        versionId="2001"
        conversationId="4001"
        conversations={[conversationFixture]}
        onSelectConversation={vi.fn()}
        onCreateConversation={vi.fn()}
        onRequirementSelect={vi.fn()}
        liveMessages={[requirementCardMessageFixture]}
        activity={null}
      />,
    )

    expect(await screen.findByText("我把这段拆成 1 条需求草案。")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledTimes(1)
  })

  it("shows the agent activity indicator while the AI is thinking", async () => {
    renderPanel({ activity: "正在分析需求关系与页面覆盖…" })

    expect(await screen.findByTestId("agent-activity")).toHaveTextContent(
      "正在分析需求关系与页面覆盖…",
    )
  })

  it("renders a requirement card and selects the requirement on click", async () => {
    api.listMessages.mockResolvedValue([requirementCardMessageFixture])
    const onRequirementSelect = vi.fn()
    renderPanel({ onRequirementSelect })

    const card = await screen.findByTestId("requirement-card-6001")
    expect(card).toHaveTextContent("退款到账时效")
    expect(card).toHaveTextContent("草案")
    expect(card).toHaveTextContent("v1")

    fireEvent.click(card)
    expect(onRequirementSelect).toHaveBeenCalledWith("6001")
  })

  it("confirms a rollback proposal and notifies the workspace", async () => {
    api.listMessages.mockResolvedValue([rollbackProposalMessageFixture])
    api.rollbackRequirement.mockResolvedValue(confirmedRequirementDetailFixture)
    const onRequirementChanged = vi.fn()
    renderPanel({ onRequirementChanged })

    expect(await screen.findByText(/新需求改变了到账时效定义/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "确认回退" }))

    await waitFor(() =>
      expect(api.rollbackRequirement).toHaveBeenCalledWith("2001", "6002", {
        reason: "新需求改变了到账时效定义，需要重新确认。",
        proposal_message_id: "5004",
      }),
    )
    expect(onRequirementChanged).toHaveBeenCalledWith(confirmedRequirementDetailFixture)
    expect(await screen.findByText("已回退为新的草案版本")).toBeInTheDocument()
  })

  it("dismisses a rollback proposal locally without calling the API", async () => {
    api.listMessages.mockResolvedValue([rollbackProposalMessageFixture])
    renderPanel()

    await screen.findByText(/新需求改变了到账时效定义/)
    fireEvent.click(screen.getByRole("button", { name: "暂不处理" }))

    expect(screen.queryByTestId("rollback-card")).not.toBeInTheDocument()
    expect(api.rollbackRequirement).not.toHaveBeenCalled()
  })

  it("shows an inline error when the rollback fails", async () => {
    api.listMessages.mockResolvedValue([rollbackProposalMessageFixture])
    api.rollbackRequirement.mockRejectedValue(
      new ApiError({ status: 409, code: "state_conflict", message: "需求已回退" }),
    )
    renderPanel()

    await screen.findByText(/新需求改变了到账时效定义/)
    fireEvent.click(screen.getByRole("button", { name: "确认回退" }))

    expect(await screen.findByText("需求已回退")).toBeInTheDocument()
  })

  it("shows a retryable error when messages fail to load", async () => {
    api.listMessages.mockRejectedValueOnce(
      new ApiError({ status: 404, code: "not_found", message: "对话不存在" }),
    )
    renderPanel()

    expect(await screen.findByText("对话不存在")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("我已载入版本上下文，请描述需求。")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledTimes(2)
  })

  it("switches conversations through the history dropdown", async () => {
    const onSelectConversation = vi.fn()
    renderPanel({ onSelectConversation })

    await screen.findByText("我已载入版本上下文，请描述需求。")
    fireEvent.click(screen.getByRole("button", { name: "历史对话" }))

    const history = screen.getByTestId("conversation-history-list")
    expect(history).toHaveTextContent("需求澄清 2026-10-30 16:00")

    fireEvent.click(screen.getByRole("button", { name: /需求澄清 2026-10-30 16:00/ }))
    expect(onSelectConversation).toHaveBeenCalledWith("4002")
    expect(screen.queryByTestId("conversation-history-list")).not.toBeInTheDocument()
  })

  it("creates a new conversation from the header", async () => {
    const onCreateConversation = vi.fn()
    renderPanel({ onCreateConversation })

    await screen.findByText("我已载入版本上下文，请描述需求。")
    fireEvent.click(screen.getByRole("button", { name: /新增对话/ }))
    expect(onCreateConversation).toHaveBeenCalledTimes(1)
  })

  it("shows the SSE connection state", async () => {
    renderPanel({ sseStatus: "live" })
    await screen.findByText("我已载入版本上下文，请描述需求。")
    expect(screen.getByTestId("sse-status")).toHaveTextContent("版本通道已连接")
  })
})

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { useState } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import {
  conversationFixture,
  conversationSecondFixture,
  messageFixture,
  teamRoomFixture,
} from "@/features/clarification/test-fixtures"
import type { ConversationMessage } from "@/features/clarification/types"
import { ApiError } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
  rollbackRequirement: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => api)

import { DesignChat, type DesignChatProps } from "./DesignChat"

const designConversation = {
  ...conversationFixture,
  kind: "design" as const,
  title: "用例设计 2026-10-31 12:00",
}
const designConversationSecond = {
  ...conversationSecondFixture,
  kind: "design" as const,
  title: "用例设计 2026-10-30 16:00",
}

const liveMessage: ConversationMessage = {
  ...messageFixture,
  id: "5009",
  parts: [{ type: "text", text: "我已新增退款失败用例草案。" }],
}

/** Mirrors the case-detail "请求修改" flow: an external draft fills the composer. */
function DraftHarness() {
  const [draft, setDraft] = useState("请修改：退款 T+1 到账")
  return (
    <DesignChat
      versionId="2001"
      conversations={[designConversation]}
      activeConversationId="4001"
      onSelectConversation={vi.fn()}
      onCreateConversation={vi.fn()}
      draftText={draft}
      onDraftTextChange={setDraft}
    />
  )
}

function renderChat(overrides: Partial<DesignChatProps> = {}) {
  const props: DesignChatProps = {
    versionId: "2001",
    conversations: [designConversation, designConversationSecond],
    activeConversationId: "4001",
    sseStatus: "live",
    onSelectConversation: vi.fn(),
    onCreateConversation: vi.fn(),
    liveMessages: [],
    ...overrides,
  }
  render(<DesignChat {...props} />)
  return props
}

describe("DesignChat", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    api.listMessages.mockResolvedValue([messageFixture])
    api.postMessage.mockResolvedValue({
      ...messageFixture,
      id: "5010",
      role: "user",
      parts: [{ type: "text", text: "请补充退款失败用例" }],
    })
    api.getTeamRoom.mockResolvedValue(teamRoomFixture)
    api.uploadContent.mockResolvedValue({
      sha256: "a".repeat(64),
      size: 12,
      mime: "text/plain",
      url: `/api/content/${"a".repeat(64)}`,
    })
  })

  afterEach(cleanup)

  it("renders the thread, sends text and has no end-round control", async () => {
    renderChat()

    expect(await screen.findByText("我已载入版本上下文，请描述需求。")).toBeInTheDocument()
    expect(screen.queryByText("结束本轮")).not.toBeInTheDocument()

    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "请补充退款失败用例" },
    })
    fireEvent.click(screen.getByRole("button", { name: "发送" }))

    await waitFor(() =>
      expect(api.postMessage).toHaveBeenCalledWith("2001", "4001", {
        client_message_id: expect.any(String),
        parts: [{ type: "text", text: "请补充退款失败用例" }],
      }),
    )
    expect(await screen.findByText("请补充退款失败用例")).toBeInTheDocument()
  })

  it("uploads an attachment and sends it with the message", async () => {
    renderChat()
    await screen.findByText("我已载入版本上下文，请描述需求。")

    fireEvent.change(screen.getByTestId("attachment-input"), {
      target: { files: [new File(["hello"], "设计约束.txt", { type: "text/plain" })] },
    })
    await screen.findByText("设计约束.txt")

    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "参考附件" },
    })
    fireEvent.click(screen.getByRole("button", { name: "发送" }))

    await waitFor(() => expect(api.postMessage).toHaveBeenCalled())
    expect(api.postMessage.mock.calls[0][2].parts).toEqual([
      { type: "text", text: "参考附件" },
      {
        type: "attachment",
        sha256: "a".repeat(64),
        name: "设计约束.txt",
        mime: "text/plain",
        size: 12,
      },
    ])
  })

  it("switches conversations through history and creates a new conversation", async () => {
    const props = renderChat()
    await screen.findByText("我已载入版本上下文，请描述需求。")

    fireEvent.click(screen.getByRole("button", { name: "历史对话" }))
    expect(screen.getByTestId("design-chat-history-list")).toHaveTextContent(
      "用例设计 2026-10-30 16:00",
    )
    fireEvent.click(screen.getByText("用例设计 2026-10-30 16:00"))
    expect(props.onSelectConversation).toHaveBeenCalledWith("4002")

    fireEvent.click(screen.getByTestId("design-chat-new-conversation"))
    expect(props.onCreateConversation).toHaveBeenCalledTimes(1)
  })

  it("shows the empty state without a conversation", () => {
    const props = renderChat({ activeConversationId: null, conversations: [] })

    expect(screen.getByText("暂无设计对话")).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("design-chat-empty-create"))
    expect(props.onCreateConversation).toHaveBeenCalledTimes(1)
    expect(api.listMessages).not.toHaveBeenCalled()
  })

  it("merges messages pushed over the version SSE channel", async () => {
    const props: DesignChatProps = {
      versionId: "2001",
      conversations: [designConversation],
      activeConversationId: "4001",
      onSelectConversation: vi.fn(),
      onCreateConversation: vi.fn(),
    }
    const { rerender } = render(<DesignChat {...props} />)
    await screen.findByText("我已载入版本上下文，请描述需求。")

    rerender(<DesignChat {...props} liveMessages={[liveMessage]} />)

    expect(await screen.findByText("我已新增退款失败用例草案。")).toBeInTheDocument()
  })

  it("shows a retryable error when the thread cannot be loaded", async () => {
    api.listMessages
      .mockRejectedValueOnce(
        new ApiError({ status: 500, code: "internal", message: "消息加载失败" }),
      )
      .mockResolvedValueOnce([messageFixture])
    renderChat()

    expect(await screen.findByText("消息加载失败")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))
    expect(await screen.findByText("我已载入版本上下文，请描述需求。")).toBeInTheDocument()
  })

  it("renders the stale notice and the read-only design team room", async () => {
    renderChat({ staleNotice: "用例 7001 因需求变更已过期" })
    await screen.findByText("我已载入版本上下文，请描述需求。")

    expect(screen.getByTestId("design-stale-notice")).toHaveTextContent(
      "用例 7001 因需求变更已过期",
    )

    fireEvent.click(screen.getByRole("button", { name: /设计团队频道/ }))
    await waitFor(() => expect(api.getTeamRoom).toHaveBeenCalledWith("2001", "design"))
    expect(await screen.findByText("只读")).toBeInTheDocument()
  })

  it("accepts an externally filled draft without sending it", async () => {
    render(<DraftHarness />)

    const textarea = await screen.findByTestId("composer-textarea")
    expect(textarea).toHaveValue("请修改：退款 T+1 到账")
    expect(api.postMessage).not.toHaveBeenCalled()

    fireEvent.change(textarea, { target: { value: "请修改：退款 T+1 到账（补充）" } })
    expect(textarea).toHaveValue("请修改：退款 T+1 到账（补充）")
  })

  it("keeps the design conversation header showing the SSE status", async () => {
    renderChat({ sseStatus: "reconnecting" })

    expect(await screen.findByTestId("design-sse-status")).toHaveTextContent("版本通道重连中…")
  })
})

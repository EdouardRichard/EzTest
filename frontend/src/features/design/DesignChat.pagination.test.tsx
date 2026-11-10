import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import { conversationFixture } from "@/features/clarification/test-fixtures"
import type { ConversationMessage } from "@/features/clarification/types"
import type { ErrorPart } from "@/features/conversation/types"
import type { ApiPage } from "@/lib/api-client"

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => api)

import { DesignChat } from "./DesignChat"

const TIMESTAMP = "2026-11-01T10:00:00Z"

const designConversation = {
  ...conversationFixture,
  kind: "design" as const,
  title: "用例设计 2026-10-31 10:00",
}

function assistantMessage(id: string, text: string): ConversationMessage {
  return {
    id,
    conversation_id: "4001",
    role: "assistant",
    parts: [{ type: "text", text }],
    refs: [],
    created_at: TIMESTAMP,
  }
}

function errorMessage(id: string, part: ErrorPart): ConversationMessage {
  return {
    id,
    conversation_id: "4001",
    role: "assistant",
    parts: [part] as unknown as ConversationMessage["parts"],
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

function renderChat() {
  return render(
    <DesignChat
      versionId="2001"
      conversations={[designConversation]}
      activeConversationId="4001"
      onSelectConversation={vi.fn()}
      onCreateConversation={vi.fn()}
      liveMessages={[]}
    />,
  )
}

describe("DesignChatPaginationWiring", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    api.listMessages.mockResolvedValue(pageOf([]))
  })

  afterEach(cleanup)

  it("loads the next history page without unmounting the design composer or draft", async () => {
    api.listMessages
      .mockResolvedValueOnce(pageOf([assistantMessage("5001", "设计第一页消息")], "5001", true))
      .mockResolvedValueOnce(pageOf([assistantMessage("5002", "设计第二页消息")], "", false))

    renderChat()

    expect(await screen.findByText("设计第一页消息")).toBeInTheDocument()
    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "未发送的设计草稿" },
    })

    fireEvent.click(screen.getByRole("button", { name: "加载更多消息" }))

    expect(await screen.findByText("设计第二页消息")).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "加载更多消息" })).not.toBeInTheDocument(),
    )
    expect(api.listMessages).toHaveBeenLastCalledWith("2001", "4001", {
      after: "5001",
      limit: 100,
    })
    expect(screen.getByTestId("composer-textarea")).toHaveValue("未发送的设计草稿")
  })

  it("keeps the design thread and draft mounted with an inline retry when loading more fails", async () => {
    api.listMessages
      .mockResolvedValueOnce(pageOf([assistantMessage("5001", "设计第一页消息")], "5001", true))
      .mockRejectedValueOnce(new Error("page failed"))
      .mockResolvedValueOnce(pageOf([assistantMessage("5002", "设计第二页消息")], "", false))

    renderChat()

    expect(await screen.findByText("设计第一页消息")).toBeInTheDocument()
    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "失败后仍要保留的设计草稿" },
    })
    fireEvent.click(screen.getByRole("button", { name: "加载更多消息" }))

    expect(await screen.findByTestId("load-more-error")).toHaveTextContent("加载更多消息失败")
    expect(screen.queryByText("对话加载失败")).not.toBeInTheDocument()
    expect(screen.getByTestId("message-list")).toBeInTheDocument()
    expect(screen.getByText("设计第一页消息")).toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("失败后仍要保留的设计草稿")

    fireEvent.click(screen.getByRole("button", { name: "重试加载" }))

    expect(await screen.findByText("设计第二页消息")).toBeInTheDocument()
    expect(screen.queryByTestId("load-more-error")).not.toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("失败后仍要保留的设计草稿")
  })

  it("refetches the design thread when a retryable error action is clicked", async () => {
    const retryableError: ErrorPart = {
      type: "error",
      code: "model_error",
      message: "模型调用失败，请重试。",
      retryable: true,
    }
    api.listMessages
      .mockResolvedValueOnce(
        pageOf([assistantMessage("5001", "设计第一页消息"), errorMessage("5002", retryableError)]),
      )
      .mockResolvedValueOnce(
        pageOf([
          assistantMessage("5001", "设计第一页消息"),
          assistantMessage("5009", "设计重试后的回复"),
        ]),
      )

    renderChat()

    expect(await screen.findByText("模型调用失败，请重试。")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("设计重试后的回复")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledTimes(2)
    expect(api.listMessages).toHaveBeenLastCalledWith("2001", "4001")
    expect(screen.getByTestId("composer-textarea")).toBeInTheDocument()
  })
})

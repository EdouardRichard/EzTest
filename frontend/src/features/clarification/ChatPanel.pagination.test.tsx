import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { ErrorPart } from "@/features/conversation/types"
import type { ApiPage } from "@/lib/api-client"

import { conversationFixture, conversationSecondFixture, messageFixture } from "./test-fixtures"
import type { ConversationMessage } from "./types"

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  rollbackRequirement: vi.fn(),
  uploadContent: vi.fn(),
}))
vi.mock("./api", () => api)

import { ChatPanel } from "./ChatPanel"

const TIMESTAMP = "2026-11-01T10:00:00Z"

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

/** The assistant message that carries the backend's failure + next action. */
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

function renderPanel() {
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
    />,
  )
}

describe("ChatPanelPaginationWiring", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    api.listMessages.mockResolvedValue(pageOf([messageFixture]))
  })

  afterEach(cleanup)

  it("loads the next history page from the inline load-more control", async () => {
    api.listMessages
      .mockResolvedValueOnce(pageOf([assistantMessage("5001", "第一页消息")], "5001", true))
      .mockResolvedValueOnce(pageOf([assistantMessage("5002", "第二页消息")], "", false))

    renderPanel()

    expect(await screen.findByText("第一页消息")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledWith("2001", "4001")

    fireEvent.click(screen.getByRole("button", { name: "加载更多消息" }))

    expect(await screen.findByText("第二页消息")).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "加载更多消息" })).not.toBeInTheDocument(),
    )
    // The cursor comes from the server page envelope, not from a live message.
    expect(api.listMessages).toHaveBeenLastCalledWith("2001", "4001", {
      after: "5001",
      limit: 100,
    })
  })

  it("shows the loading indicator and keeps the thread and draft mounted while loading", async () => {
    let resolveNextPage: (page: ApiPage<ConversationMessage>) => void = () => {}
    const pendingPage = new Promise<ApiPage<ConversationMessage>>((resolve) => {
      resolveNextPage = resolve
    })
    api.listMessages
      .mockResolvedValueOnce(pageOf([assistantMessage("5001", "第一页消息")], "5001", true))
      .mockReturnValueOnce(pendingPage)

    renderPanel()

    expect(await screen.findByText("第一页消息")).toBeInTheDocument()
    const composer = screen.getByTestId("composer-textarea")
    fireEvent.change(composer, { target: { value: "还没发送的草稿" } })

    fireEvent.click(screen.getByRole("button", { name: "加载更多消息" }))

    const loading = await screen.findByRole("button", { name: "加载中…" })
    expect(loading).toBeDisabled()
    expect(screen.getByTestId("message-list")).toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("还没发送的草稿")

    await act(async () => {
      resolveNextPage(pageOf([assistantMessage("5002", "第二页消息")], "", false))
      await pendingPage
    })

    expect(await screen.findByText("第二页消息")).toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("还没发送的草稿")
  })

  it("keeps the thread, composer and draft mounted with an inline retry when loading more fails", async () => {
    api.listMessages
      .mockResolvedValueOnce(pageOf([assistantMessage("5001", "第一页消息")], "5001", true))
      .mockRejectedValueOnce(new Error("page failed"))
      .mockResolvedValueOnce(pageOf([assistantMessage("5002", "第二页消息")], "", false))

    renderPanel()

    expect(await screen.findByText("第一页消息")).toBeInTheDocument()
    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "失败后仍要保留的草稿" },
    })
    fireEvent.click(screen.getByRole("button", { name: "加载更多消息" }))

    expect(await screen.findByTestId("load-more-error")).toHaveTextContent("加载更多消息失败")
    // A page failure is not a thread failure: no full-page error replaces the chat.
    expect(screen.queryByText("对话加载失败")).not.toBeInTheDocument()
    expect(screen.getByTestId("message-list")).toBeInTheDocument()
    expect(screen.getByText("第一页消息")).toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("失败后仍要保留的草稿")

    fireEvent.click(screen.getByRole("button", { name: "重试加载" }))

    expect(await screen.findByText("第二页消息")).toBeInTheDocument()
    expect(screen.queryByTestId("load-more-error")).not.toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("失败后仍要保留的草稿")
  })

  it("refetches the thread when a retryable error action is clicked", async () => {
    const retryableError: ErrorPart = {
      type: "error",
      code: "model_error",
      message: "模型调用失败，请重试。",
      retryable: true,
    }
    api.listMessages
      .mockResolvedValueOnce(
        pageOf([assistantMessage("5001", "第一页消息"), errorMessage("5002", retryableError)]),
      )
      .mockResolvedValueOnce(
        pageOf([assistantMessage("5001", "第一页消息"), assistantMessage("5009", "重试后的回复")]),
      )

    renderPanel()

    expect(await screen.findByText("模型调用失败，请重试。")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    expect(await screen.findByText("重试后的回复")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledTimes(2)
    expect(api.listMessages).toHaveBeenLastCalledWith("2001", "4001")
    expect(screen.getByTestId("composer-textarea")).toBeInTheDocument()
  })

  it("keeps the thread and composer draft mounted while an error action refreshes the snapshot", async () => {
    const retryableError: ErrorPart = {
      type: "error",
      code: "model_error",
      message: "模型调用失败，请重试。",
      retryable: true,
    }
    let resolveRefresh: (page: ApiPage<ConversationMessage>) => void = () => {}
    const pendingRefresh = new Promise<ApiPage<ConversationMessage>>((resolve) => {
      resolveRefresh = resolve
    })
    api.listMessages
      .mockResolvedValueOnce(
        pageOf([assistantMessage("5001", "第一页消息"), errorMessage("5002", retryableError)]),
      )
      .mockReturnValueOnce(pendingRefresh)

    renderPanel()

    expect(await screen.findByText("模型调用失败，请重试。")).toBeInTheDocument()
    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "错误动作刷新时要保留的草稿" },
    })
    fireEvent.click(screen.getByRole("button", { name: "重试" }))

    // Fetching the next snapshot is a refresh of an already loaded thread, not
    // a new load: the thread and the Composer (whose draft lives in component
    // state) stay mounted behind a subtle indicator instead of LoadingCard.
    expect(screen.getByTestId("composer-textarea")).toHaveValue("错误动作刷新时要保留的草稿")
    expect(screen.getByTestId("message-list")).toBeInTheDocument()
    expect(screen.getByText("模型调用失败，请重试。")).toBeInTheDocument()
    expect(screen.queryByText("正在加载对话…")).not.toBeInTheDocument()
    expect(await screen.findByTestId("thread-refreshing")).toBeInTheDocument()

    await act(async () => {
      resolveRefresh(
        pageOf([assistantMessage("5001", "第一页消息"), assistantMessage("5009", "重试后的回复")]),
      )
      await pendingRefresh
    })

    expect(await screen.findByText("重试后的回复")).toBeInTheDocument()
    await waitFor(() => expect(screen.queryByTestId("thread-refreshing")).not.toBeInTheDocument())
    expect(screen.getByTestId("composer-textarea")).toHaveValue("错误动作刷新时要保留的草稿")
    expect(api.listMessages).toHaveBeenCalledTimes(2)
    expect(api.listMessages).toHaveBeenLastCalledWith("2001", "4001")
  })
})

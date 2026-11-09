import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import type { ApiPage } from "@/lib/api-client"

import { MessageList, type MessageListProps } from "./MessageList"
import type { ConversationMessage } from "./types"
import { useConversation } from "./useConversation"

const api = vi.hoisted(() => ({
  downloadContent: vi.fn(),
  listMessages: vi.fn(),
  postMessage: vi.fn(),
}))
vi.mock("./api", () => api)

const TIMESTAMP = "2026-11-05T00:00:00Z"

function message(id: string, text: string, conversationId = "4001"): ConversationMessage {
  return {
    id,
    conversation_id: conversationId,
    role: "user",
    parts: [{ type: "text", text }],
    refs: [],
    created_at: TIMESTAMP,
  }
}

function listElement(
  messages: ConversationMessage[],
  props: Omit<MessageListProps, "versionId" | "messages" | "activity" | "onRequirementSelect"> = {},
) {
  return (
    <MessageList
      versionId="2001"
      messages={messages}
      activity={null}
      onRequirementSelect={vi.fn()}
      {...props}
    />
  )
}

function pageOf(
  items: ConversationMessage[],
  nextCursor = "",
  hasMore = false,
): ApiPage<ConversationMessage> {
  return { items, nextCursor, hasMore }
}

/* ------------------------------ jsdom geometry ----------------------------- */

let listScrollHeight = 1000
let listClientHeight = 400
let scrollTo: ReturnType<typeof vi.fn>

beforeEach(() => {
  listScrollHeight = 1000
  listClientHeight = 400
  scrollTo = vi.fn(function (this: HTMLElement, options?: ScrollToOptions | number) {
    const top = typeof options === "number" ? options : options?.top
    if (typeof top === "number") this.scrollTop = top
  })
  vi.spyOn(Element.prototype, "scrollHeight", "get").mockImplementation(() => listScrollHeight)
  vi.spyOn(Element.prototype, "clientHeight", "get").mockImplementation(() => listClientHeight)
  Object.defineProperty(Element.prototype, "scrollTo", {
    configurable: true,
    writable: true,
    value: scrollTo,
  })
  api.downloadContent.mockReset()
  api.listMessages.mockReset()
  api.postMessage.mockReset()
})

afterEach(() => {
  cleanup()
  delete (Element.prototype as { scrollTo?: unknown }).scrollTo
})

function scrolledList(): HTMLElement {
  return screen.getByTestId("message-list")
}

describe("MessageList stick-to-bottom scrolling", () => {
  it("renders an a11y log and starts at the bottom", () => {
    render(listElement([message("1", "第一条消息")]))

    const list = scrolledList()
    expect(list).toHaveAttribute("role", "log")
    expect(list).toHaveAttribute("aria-live", "polite")
    expect(list).toHaveAttribute("aria-label", "对话消息")
    expect(scrollTo).toHaveBeenCalled()
    expect(list.scrollTop).toBe(listScrollHeight)
  })

  it("sticks to the bottom when a new message or a streaming increment arrives", async () => {
    const first = message("1", "第一条消息")
    const { rerender } = render(listElement([first]))
    const list = scrolledList()
    const initialCalls = scrollTo.mock.calls.length

    rerender(listElement([first, message("2", "第二条消息")]))

    await waitFor(() => expect(scrollTo.mock.calls.length).toBeGreaterThan(initialCalls))
    expect(list.scrollTop).toBe(listScrollHeight)

    // A streamed part update changes the message identity but not its key; the
    // thread still follows the bottom without a new message row.
    const streamed = {
      ...first,
      parts: [{ type: "text" as const, text: "第一条消息（流式更新）" }],
    }
    const beforeStream = scrollTo.mock.calls.length
    rerender(listElement([streamed, message("2", "第二条消息")]))

    await waitFor(() => expect(scrollTo.mock.calls.length).toBeGreaterThan(beforeStream))
    expect(screen.getAllByTestId("message-user")).toHaveLength(2)
  })

  it("keeps following when the content grows without an upward scroll (D-05)", async () => {
    const first = message("1", "第一条消息")
    const { rerender } = render(listElement([first]))
    const list = scrolledList()

    // A streamed increment, a rendered image or the activity bubble makes the
    // content 600px taller while the reader has not moved. The browser may
    // dispatch the scroll event only after that growth, so the distance from
    // the bottom no longer reflects a real upward scroll: it must not pause the
    // follow.
    listScrollHeight = 1600
    fireEvent.scroll(list)

    expect(screen.queryByRole("button", { name: /回到最新/ })).not.toBeInTheDocument()

    // The next appended message still anchors the viewport at the new bottom.
    rerender(listElement([first, message("2", "第二条消息")]))

    await waitFor(() => expect(list.scrollTop).toBe(listScrollHeight))
    expect(screen.queryByRole("button", { name: /回到最新/ })).not.toBeInTheDocument()
  })

  it("pauses after scrolling more than 80px up, counts unread and resumes from the button", async () => {
    const first = message("1", "第一条消息")
    const { rerender } = render(listElement([first]))
    const list = scrolledList()

    list.scrollTop = 0
    fireEvent.scroll(list)

    // The pause affordance is available immediately; the counter appears after
    // the first message that arrived while auto-follow was paused.
    expect(screen.getByRole("button", { name: /回到最新/ })).toBeInTheDocument()

    const callsBeforeSecondMessage = scrollTo.mock.calls.length
    rerender(listElement([first, message("2", "第二条消息")]))

    const returnButton = await screen.findByRole("button", { name: "回到最新 (1)" })
    expect(returnButton).toHaveAttribute("aria-label", "回到最新 (1)")
    // Paused means the new message must not move the viewport.
    expect(scrollTo.mock.calls.length).toBe(callsBeforeSecondMessage)
    expect(list.scrollTop).toBe(0)

    fireEvent.click(returnButton)

    await waitFor(() =>
      expect(screen.queryByRole("button", { name: /回到最新/ })).not.toBeInTheDocument(),
    )
    expect(list.scrollTop).toBe(listScrollHeight)

    const callsAfterResume = scrollTo.mock.calls.length
    rerender(listElement([first, message("2", "第二条消息"), message("3", "第三条消息")]))
    await waitFor(() => expect(scrollTo.mock.calls.length).toBeGreaterThan(callsAfterResume))
    expect(list.scrollTop).toBe(listScrollHeight)
  })

  it("does not count hidden kind=answer messages as unread or new rows", async () => {
    const first = message("1", "第一条消息")
    const { rerender } = render(listElement([first]))
    const list = scrolledList()

    list.scrollTop = 0
    fireEvent.scroll(list)
    expect(screen.getByRole("button", { name: "回到最新" })).toBeInTheDocument()

    // The structured answer is echoed by the question card, so its message row
    // is hidden. It must not inflate the unread counter or the scroll math.
    const hiddenAnswer = {
      ...message("2", ""),
      kind: "answer",
      parts: [],
    } as unknown as ConversationMessage
    rerender(listElement([first, hiddenAnswer]))
    expect(screen.getByRole("button", { name: "回到最新" })).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /回到最新 \(/ })).not.toBeInTheDocument()
    expect(screen.queryAllByTestId("message-user")).toHaveLength(1)

    rerender(listElement([first, hiddenAnswer, message("3", "第三条消息")]))
    expect(await screen.findByRole("button", { name: "回到最新 (1)" })).toBeInTheDocument()
  })

  it("resumes auto-follow when the reader scrolls back to the bottom", async () => {
    const first = message("1", "第一条消息")
    const { rerender } = render(listElement([first]))
    const list = scrolledList()

    list.scrollTop = 0
    fireEvent.scroll(list)
    const returnButton = screen.getByRole("button", { name: /回到最新/ })

    list.scrollTop = listScrollHeight
    fireEvent.scroll(list)
    await waitFor(() => expect(returnButton).not.toBeInTheDocument())

    const callsBeforeNewMessage = scrollTo.mock.calls.length
    rerender(listElement([first, message("2", "第二条消息")]))
    await waitFor(() => expect(scrollTo.mock.calls.length).toBeGreaterThan(callsBeforeNewMessage))
    expect(list.scrollTop).toBe(listScrollHeight)
  })

  it("compensates scrollTop when older messages are prepended", async () => {
    const second = message("2", "第二条消息")
    const third = message("3", "第三条消息")
    const { rerender } = render(listElement([second, third]))
    const list = scrolledList()

    list.scrollTop = 200
    fireEvent.scroll(list)
    expect(screen.getByRole("button", { name: /回到最新/ })).toBeInTheDocument()

    // The newly prepended history makes the content 300px taller. The reader
    // must keep looking at the same message, not jump to the top or bottom.
    listScrollHeight = 1300
    const callsBeforePrepend = scrollTo.mock.calls.length
    rerender(listElement([message("1", "第一条消息"), second, third]))

    await waitFor(() => expect(list.scrollTop).toBe(500))
    expect(scrollTo.mock.calls.length).toBe(callsBeforePrepend)
    expect(screen.queryByRole("button", { name: /回到最新 \(/ })).not.toBeInTheDocument()
  })

  it("requests older history when the reader scrolls to the top", () => {
    const onLoadMore = vi.fn()
    render(listElement([message("5", "已有消息")], { hasMore: true, onLoadMore }))
    const list = scrolledList()

    list.scrollTop = 0
    fireEvent.scroll(list)

    expect(onLoadMore).toHaveBeenCalledTimes(1)

    // A second scroll event while the same request is in flight is ignored.
    fireEvent.scroll(list)
    expect(onLoadMore).toHaveBeenCalledTimes(1)
  })

  it("resets to the bottom on a conversation switch and restores a saved manual position", async () => {
    const conversationA = [message("1", "会话 A", "conv-a")]
    const conversationB = [message("2", "会话 B", "conv-b")]
    const { rerender } = render(listElement(conversationA))
    const list = scrolledList()

    list.scrollTop = 200
    fireEvent.scroll(list)

    rerender(listElement(conversationB))
    await waitFor(() => expect(list.scrollTop).toBe(listScrollHeight))

    rerender(listElement(conversationA))
    await waitFor(() => expect(list.scrollTop).toBe(200))
    expect(screen.getByRole("button", { name: "回到最新" })).toBeInTheDocument()
  })

  it("keeps message keys stable across rerenders", () => {
    const { rerender } = render(listElement([message("1", "稳定 key")]))
    const firstNode = screen.getByTestId("message-user")

    rerender(listElement([message("1", "稳定 key")]))

    expect(screen.getByTestId("message-user")).toBe(firstNode)
  })
})

describe("useConversation history pagination", () => {
  it("loads the latest page first and older pages with the before cursor", async () => {
    api.listMessages
      .mockResolvedValueOnce(
        pageOf([message("9001", "最新一页 A"), message("9002", "最新一页 B")], "9001", true),
      )
      .mockResolvedValueOnce(
        pageOf([message("8001", "更旧一页 A"), message("8002", "更旧一页 B")], "8001", false),
      )

    const { result } = renderHook(() =>
      useConversation({ versionId: "2001", conversationId: "4001" }),
    )

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.messages.map((item) => item.id)).toEqual(["9001", "9002"])
    // No cursor is sent for the first page: the API-8 endpoint defaults to the
    // latest page (it is the `before`-cursor contract).
    expect(api.listMessages).toHaveBeenNthCalledWith(1, "2001", "4001")

    await act(async () => {
      await result.current.loadMore()
    })

    expect(api.listMessages).toHaveBeenNthCalledWith(2, "2001", "4001", {
      before: "9001",
      limit: 100,
    })
    expect(result.current.messages.map((item) => item.id)).toEqual(["8001", "8002", "9001", "9002"])
    expect(result.current.hasMore).toBe(false)
  })

  it("dedupes repeated SSE frames and message updates by message id", async () => {
    api.listMessages.mockResolvedValue(pageOf([message("7001", "已有消息")], "", false))
    const live = message("7002", "实时消息")
    const { result, rerender } = renderHook(
      (props: { liveMessages: ConversationMessage[] }) =>
        useConversation({
          versionId: "2001",
          conversationId: "4001",
          liveMessages: props.liveMessages,
        }),
      { initialProps: { liveMessages: [] as ConversationMessage[] } },
    )

    await waitFor(() => expect(result.current.messages.map((item) => item.id)).toEqual(["7001"]))

    rerender({ liveMessages: [live] })
    await waitFor(() =>
      expect(result.current.messages.map((item) => item.id)).toEqual(["7001", "7002"]),
    )

    // The same SSE frame (same id) arriving twice must update in place.
    const updatedLive = {
      ...live,
      parts: [{ type: "text" as const, text: "实时消息（补充）" }],
    }
    rerender({ liveMessages: [updatedLive] })
    await waitFor(() => expect(result.current.messages).toHaveLength(2))
    expect(result.current.messages.filter((item) => item.id === "7002")).toHaveLength(1)

    const textParts = result.current.messages.filter((item) => item.id === "7002")[0].parts
    expect(textParts).toEqual([{ type: "text", text: "实时消息（补充）" }])
  })
})

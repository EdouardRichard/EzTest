import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import {
  conversationFixture,
  conversationSecondFixture,
  messageFixture,
  teamRoomFixture,
} from "@/features/clarification/test-fixtures"

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
  rollbackRequirement: vi.fn(),
  getAttachmentPendingDetails: vi.fn(),
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

function chatProps(overrides: Partial<DesignChatProps> = {}): DesignChatProps {
  return {
    versionId: "2001",
    conversations: [designConversation, designConversationSecond],
    activeConversationId: "4001",
    onSelectConversation: vi.fn(),
    onCreateConversation: vi.fn(),
    liveMessages: [],
    draftText: "请修改：退款 T+1 到账",
    onDraftTextChange: vi.fn(),
    ...overrides,
  }
}

function renderChat(overrides: Partial<DesignChatProps> = {}) {
  return render(<DesignChat {...chatProps(overrides)} />)
}

describe("DesignChat floating expansion", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    api.listMessages.mockResolvedValue([messageFixture])
    api.getTeamRoom.mockResolvedValue(teamRoomFixture)
  })

  afterEach(cleanup)

  it("expands without losing the thread, the draft or the loaded snapshot", async () => {
    const onDraftTextChange = vi.fn()
    renderChat({ onDraftTextChange })
    expect(await screen.findByText("我已载入版本上下文，请描述需求。")).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledTimes(1)
    const textarea = screen.getByTestId("composer-textarea")
    expect(textarea).toHaveValue("请修改：退款 T+1 到账")
    const thread = screen.getByTestId("message-list")
    thread.scrollTop = 96

    fireEvent.click(screen.getByTestId("design-chat-expand"))

    const dialog = screen.getByRole("dialog", { name: "设计对话" })
    expect(dialog).toContainElement(thread)
    expect(screen.getByTestId("message-list")).toBe(thread)
    expect(thread.scrollTop).toBe(96)
    expect(screen.getByTestId("composer-textarea")).toBe(textarea)
    expect(textarea).toHaveValue("请修改：退款 T+1 到账")
    expect(api.listMessages).toHaveBeenCalledTimes(1)

    fireEvent.keyDown(window, { key: "Escape" })
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(screen.getByTestId("design-chat-expand")).toHaveAttribute("aria-expanded", "false")
    expect(screen.getByTestId("composer-textarea")).toHaveValue("请修改：退款 T+1 到账")
    expect(onDraftTextChange).not.toHaveBeenCalled()
  })

  it("expands a collapsed chat into the full thread and returns to the collapsed bar", async () => {
    const onToggleCollapsed = vi.fn()
    renderChat({ collapsed: true, onToggleCollapsed })
    expect(screen.queryByTestId("message-list")).not.toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("请修改：退款 T+1 到账")

    fireEvent.click(screen.getByTestId("design-chat-expand"))

    const dialog = screen.getByRole("dialog", { name: "设计对话" })
    expect(dialog).toContainElement(await screen.findByTestId("message-list"))
    expect(screen.getByTestId("composer-textarea")).toHaveValue("请修改：退款 T+1 到账")

    fireEvent.keyDown(window, { key: "Escape" })

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(screen.queryByTestId("message-list")).not.toBeInTheDocument()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("请修改：退款 T+1 到账")
    // Expansion is not the collapse control: the layer split is untouched.
    expect(onToggleCollapsed).not.toHaveBeenCalled()
  })
})

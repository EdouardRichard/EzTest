import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

import {
  conversationFixture,
  conversationSecondFixture,
  messageFixture,
  requirementCardMessageFixture,
  userMessageFixture,
} from "./test-fixtures"

const api = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  rollbackRequirement: vi.fn(),
  uploadContent: vi.fn(),
}))
vi.mock("./api", () => api)

import { ChatPanel, type ChatPanelProps } from "./ChatPanel"

const ASSISTANT_TEXT = "我已载入版本上下文，请描述需求。"

function panelProps(overrides: Partial<ChatPanelProps> = {}): ChatPanelProps {
  return {
    versionId: "2001",
    conversationId: "4001",
    conversations: [conversationFixture, conversationSecondFixture],
    onSelectConversation: vi.fn(),
    onCreateConversation: vi.fn(),
    onRequirementSelect: vi.fn(),
    liveMessages: [],
    activity: null,
    ...overrides,
  }
}

function renderPanel(overrides: Partial<ChatPanelProps> = {}) {
  return render(<ChatPanel {...panelProps(overrides)} />)
}

/** Every `data-testid` rendered twice would make the floating copy ambiguous. */
function duplicateTestIds(): string[] {
  const counts = new Map<string, number>()
  for (const element of document.querySelectorAll<HTMLElement>("[data-testid]")) {
    const testId = element.dataset.testid ?? ""
    counts.set(testId, (counts.get(testId) ?? 0) + 1)
  }
  return [...counts.entries()].filter(([, count]) => count > 1).map(([testId]) => testId)
}

describe("ChatPanel floating expansion", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) fn.mockReset()
    api.listMessages.mockResolvedValue([messageFixture, userMessageFixture])
  })

  afterEach(cleanup)

  it("expands without losing the draft, the scroll position or the loaded thread", async () => {
    const view = renderPanel()
    expect(await screen.findByText(ASSISTANT_TEXT)).toBeInTheDocument()
    expect(api.listMessages).toHaveBeenCalledTimes(1)

    const textarea = screen.getByTestId("composer-textarea")
    fireEvent.change(textarea, { target: { value: "放大后仍要保留的草稿" } })
    const thread = screen.getByTestId("message-list")
    thread.scrollTop = 240

    fireEvent.click(screen.getByTestId("chat-panel-expand"))

    const dialog = screen.getByRole("dialog", { name: "需求澄清对话" })
    expect(dialog).toContainElement(thread)
    expect(dialog.contains(document.activeElement)).toBe(true)
    // Same DOM nodes: the thread was moved, not re-rendered, so the hook state,
    // the composer draft and the scroll offset survive the expansion.
    expect(screen.getByTestId("message-list")).toBe(thread)
    expect(thread.scrollTop).toBe(240)
    expect(screen.getByTestId("composer-textarea")).toBe(textarea)
    expect(textarea).toHaveValue("放大后仍要保留的草稿")
    expect(api.listMessages).toHaveBeenCalledTimes(1)

    // The version SSE channel keeps feeding the same useConversation instance:
    // a live message merges into the expanded thread with no refetch.
    view.rerender(<ChatPanel {...panelProps({ liveMessages: [requirementCardMessageFixture] })} />)

    expect(await screen.findByText("我把这段拆成 1 条需求草案。")).toBeInTheDocument()
    expect(screen.getByTestId("message-list")).toBe(thread)
    expect(screen.getByTestId("composer-textarea")).toBe(textarea)
    expect(api.listMessages).toHaveBeenCalledTimes(1)
  })

  it("renders a single set of test ids while expanded", async () => {
    renderPanel()
    await screen.findByText(ASSISTANT_TEXT)
    // `rich-text` intentionally repeats per message; expansion must not add any
    // new duplicate (in particular no second inline chat copy behind the panel).
    const inlineDuplicates = duplicateTestIds()

    fireEvent.click(screen.getByTestId("chat-panel-expand"))

    expect(duplicateTestIds()).toEqual(inlineDuplicates)
    expect(screen.getAllByTestId("chat-panel")).toHaveLength(1)
    expect(screen.getAllByTestId("message-list")).toHaveLength(1)
    expect(screen.getAllByTestId("composer-textarea")).toHaveLength(1)
    expect(screen.getByTestId("floating-chat-dialog")).toHaveAttribute("aria-modal", "true")
    expect(screen.getByTestId("floating-chat-close")).toBeInTheDocument()
  })

  it("closes on Escape and restores the inline layout", async () => {
    const view = renderPanel()
    await screen.findByText(ASSISTANT_TEXT)
    const inlineHost = view.container.querySelector("[data-floating-panel-host]")
    expect(inlineHost).not.toBeNull()
    fireEvent.change(screen.getByTestId("composer-textarea"), {
      target: { value: "关闭后仍要保留的草稿" },
    })

    fireEvent.click(screen.getByTestId("chat-panel-expand"))
    expect(screen.getByRole("dialog", { name: "需求澄清对话" })).toBeInTheDocument()
    const floatingHost = screen.getByTestId("chat-panel").closest("[data-floating-panel-host]")
    expect(floatingHost).not.toBeNull()
    expect(floatingHost?.closest('[role="dialog"]')).not.toBeNull()

    fireEvent.keyDown(window, { key: "Escape" })

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(screen.getByTestId("chat-panel").closest("[data-floating-panel-host]")).toBe(inlineHost)
    expect(screen.getByTestId("chat-panel").closest('[role="dialog"]')).toBeNull()
    expect(screen.getByTestId("composer-textarea")).toHaveValue("关闭后仍要保留的草稿")
    expect(screen.getByTestId("chat-panel-expand")).toHaveAttribute("aria-expanded", "false")
  })

  it("fills the grid row it occupies while closed", async () => {
    const view = renderPanel()
    await screen.findByText(ASSISTANT_TEXT)

    const panel = screen.getByTestId("chat-panel")
    const inlineSlot = view.container.querySelector<HTMLElement>("[data-floating-panel-inline]")
    const host = view.container.querySelector<HTMLElement>("[data-floating-panel-host]")
    expect(inlineSlot).not.toBeNull()
    expect(host).not.toBeNull()
    if (inlineSlot === null || host === null) return

    // `RequirementsTab` places this wrapper in the workspace grid and the
    // primitive's host is `h-full`. jsdom has no layout engine, so the closed
    // card's fill behaviour is pinned structurally: without the flex fill the
    // card stops at its 520px min-height and ends above the row bottom whenever
    // the right sidebar column is taller.
    expect(panel).toHaveAttribute("data-floating", "false")
    expect(panel).toHaveClass("flex-1")
    // The fill class must not replace the closed-state size contract.
    expect(panel).toHaveClass("min-h-[520px]", "lg:max-h-[calc(100vh-260px)]")
    expect(inlineSlot).toContainElement(host)
    expect(host).toHaveClass("h-full", "min-h-0", "flex-col")
  })

  it("closes from the backdrop and toggles closed from the expand control", async () => {
    renderPanel()
    await screen.findByText(ASSISTANT_TEXT)

    fireEvent.click(screen.getByTestId("chat-panel-expand"))
    expect(screen.getByTestId("chat-panel-expand")).toHaveAttribute("aria-expanded", "true")
    fireEvent.click(screen.getByTestId("floating-chat-backdrop"))
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("chat-panel-expand"))
    expect(screen.getByRole("dialog", { name: "需求澄清对话" })).toBeInTheDocument()
    fireEvent.click(screen.getByTestId("chat-panel-expand"))
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })
})

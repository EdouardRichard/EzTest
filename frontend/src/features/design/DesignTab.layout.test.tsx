import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const designApi = vi.hoisted(() => ({
  getDesignGraph: vi.fn(),
  getDesignOverview: vi.fn(),
  listDesignConversations: vi.fn(),
  createDesignConversation: vi.fn(),
  getDesignCase: vi.fn(),
  confirmCases: vi.fn(),
  confirmStageCases: vi.fn(),
  createExport: vi.fn(),
  downloadExport: vi.fn(),
  listExports: vi.fn(),
}))
vi.mock("./api", () => designApi)

// DesignChat renders the conversation thread through the clarification module.
const clarificationApi = vi.hoisted(() => ({
  listMessages: vi.fn(),
  postMessage: vi.fn(),
  getTeamRoom: vi.fn(),
  uploadContent: vi.fn(),
  rollbackRequirement: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => clarificationApi)

const versionsApi = vi.hoisted(() => ({ updateVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: () => "live",
  subscribeStreamReset: () => () => {},
}))

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

// The three-layer contract is about the DesignTab shell. React Flow is
// unrelated to it, so the canvas is replaced with a marker node.
vi.mock("./DesignMindmap", async () => {
  const React = await import("react")
  return {
    DesignMindmap: () => React.createElement("div", { "data-testid": "design-mindmap" }),
  }
})

import { versionFixture } from "@/features/versions/test-fixtures"

import { DesignTab } from "./DesignTab"
import { designGraphFixture, designOverviewFixture } from "./test-fixtures"

const TIMESTAMP = "2026-10-31T12:00:00Z"

/** `app-content` bottom padding; the layout reserves it so the page never scrolls. */
const PAGE_BOTTOM_INSET = 48

const LAYER_ORDER = [
  "design-layer-chat",
  "design-divider-chat",
  "design-layer-mindmap",
  "design-divider-mindmap",
  "design-layer-detail",
]

function mockWorkspace(): void {
  designApi.getDesignGraph.mockResolvedValue(designGraphFixture)
  designApi.getDesignOverview.mockResolvedValue(designOverviewFixture)
  designApi.listDesignConversations.mockResolvedValue([
    {
      id: "4009",
      title: "用例设计 2026-10-31 12:00",
      kind: "design",
      status: "active",
      message_count: 2,
      last_message_at: TIMESTAMP,
      created_at: TIMESTAMP,
    },
  ])
  clarificationApi.listMessages.mockResolvedValue([])
  clarificationApi.getTeamRoom.mockResolvedValue({ team: "design", digest: null, posts: [] })
  versionsApi.updateVersion.mockResolvedValue(versionFixture)
}

async function renderWorkspace() {
  render(<DesignTab versionId="2001" />)
  await screen.findByTestId("design-mindmap")
  return {
    root: screen.getByTestId("design-tab"),
    layers: screen.getByTestId("design-layers"),
    chat: screen.getByTestId("design-layer-chat"),
    mindmap: screen.getByTestId("design-layer-mindmap"),
    detail: screen.getByTestId("design-layer-detail"),
    chatDivider: screen.getByTestId("design-divider-chat"),
    mindmapDivider: screen.getByTestId("design-divider-mindmap"),
  }
}

/** jsdom has no layout engine; the drag maths only needs a definite height. */
function mockLayerHeight(element: HTMLElement, height: number): void {
  vi.spyOn(element, "getBoundingClientRect").mockReturnValue({
    x: 0,
    y: 0,
    width: 800,
    height,
    top: 0,
    right: 800,
    bottom: height,
    left: 0,
    toJSON: () => ({}),
  } as DOMRect)
}

function dragDivider(divider: HTMLElement, fromY: number, toY: number, pointerId = 1): void {
  fireEvent.pointerDown(divider, { pointerId, clientY: fromY })
  fireEvent.pointerMove(divider, { pointerId, clientY: toY })
  fireEvent.pointerUp(divider, { pointerId, clientY: toY })
}

describe("DesignTab three-layer layout", () => {
  beforeEach(() => {
    for (const fn of Object.values(designApi)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    versionsApi.updateVersion.mockReset()
    navigation.replace.mockReset()
    mockWorkspace()
  })

  afterEach(cleanup)

  it("stacks chat, mindmap and detail in DOM order", async () => {
    const { layers } = await renderWorkspace()

    expect(Array.from(layers.children).map((child) => child.getAttribute("data-testid"))).toEqual(
      LAYER_ORDER,
    )
  })

  it("drags the dividers to rebalance the layer heights", async () => {
    const { layers, chat, mindmap, detail, chatDivider, mindmapDivider } = await renderWorkspace()
    mockLayerHeight(layers, 1000)

    expect(chat.style.flexBasis).toBe("35%")
    expect(mindmap.style.flexBasis).toBe("40%")
    expect(detail.style.flexBasis).toBe("25%")

    // Down 5% of the split area: the chat grows, the mindmap shrinks.
    dragDivider(chatDivider, 100, 150)
    expect(chat.style.flexBasis).toBe("40%")
    expect(mindmap.style.flexBasis).toBe("35%")
    expect(detail.style.flexBasis).toBe("25%")

    // Down 10% of the split area: the mindmap grows, the detail shrinks.
    dragDivider(mindmapDivider, 300, 400)
    expect(mindmap.style.flexBasis).toBe("45%")
    expect(detail.style.flexBasis).toBe("15%")
  })

  it("clamps a divider drag so every layer stays visible", async () => {
    const { layers, chat, mindmap, chatDivider } = await renderWorkspace()
    mockLayerHeight(layers, 1000)

    dragDivider(chatDivider, 750, 0)

    const chatPercent = Number.parseFloat(chat.style.flexBasis)
    const mindmapPercent = Number.parseFloat(mindmap.style.flexBasis)
    expect(chatPercent).toBeGreaterThanOrEqual(10)
    expect(mindmapPercent).toBeGreaterThanOrEqual(10)
    expect(chatPercent + mindmapPercent).toBe(75)
  })

  it("exposes horizontal separators that resize with the keyboard", async () => {
    const { chat, mindmap, chatDivider } = await renderWorkspace()

    expect(chatDivider).toHaveAttribute("role", "separator")
    expect(chatDivider).toHaveAttribute("aria-orientation", "horizontal")

    fireEvent.keyDown(chatDivider, { key: "ArrowDown" })
    expect(chat.style.flexBasis).toBe("40%")
    expect(mindmap.style.flexBasis).toBe("35%")
  })

  it("collapses the chat to an input bar and hands its space to the mindmap", async () => {
    const { chat, mindmap, detail, chatDivider } = await renderWorkspace()
    expect(await screen.findByTestId("composer-textarea")).toBeInTheDocument()
    expect(screen.getByTestId("message-list")).toBeInTheDocument()

    fireEvent.click(screen.getByTestId("design-chat-collapse-toggle"))

    expect(chat).toHaveAttribute("data-collapsed", "true")
    expect(chat.style.flexBasis).toBe("auto")
    // A growing mindmap absorbs the space the fixed-size input bar releases.
    expect(mindmap.style.flexGrow).toBe("1")
    expect(mindmap.style.flexBasis).toBe("0%")
    expect(detail.style.flexBasis).toBe("25%")
    expect(chatDivider).toHaveAttribute("aria-disabled", "true")
    expect(screen.getByTestId("composer-textarea")).toBeInTheDocument()
    expect(screen.queryByTestId("message-list")).not.toBeInTheDocument()

    fireEvent.click(screen.getByTestId("design-chat-collapse-toggle"))

    expect(chat).toHaveAttribute("data-collapsed", "false")
    expect(chat.style.flexBasis).toBe("35%")
    expect(mindmap.style.flexGrow).toBe("0")
    expect(mindmap.style.flexBasis).toBe("40%")
  })

  it("caps the workspace to the viewport and scrolls inside each layer", async () => {
    const { root, layers, chat, mindmap, detail } = await renderWorkspace()

    await waitFor(() => expect(root.style.height).toMatch(/^\d+px$/))
    const available = window.innerHeight - root.getBoundingClientRect().top - PAGE_BOTTOM_INSET
    expect(root.style.height).toBe(`${available}px`)
    expect(root).toHaveClass("overflow-hidden")
    expect(root.className).not.toMatch(/(?:^|\s)space-y-/)
    expect(layers).toHaveClass("flex-1")
    expect(layers).toHaveClass("min-h-0")

    for (const layer of [chat, mindmap, detail]) {
      expect(layer).toHaveClass("overflow-auto")
      expect(layer).toHaveClass("min-h-0")
    }
  })

  it("keeps the mobile DOM order unchanged", async () => {
    const { layers, chat, mindmap, detail } = await renderWorkspace()

    // The same column is the mobile layout: no CSS reordering on small screens.
    expect(layers).toHaveClass("flex-col")
    expect(layers.className).not.toMatch(/(?:^|\s)flex-col-reverse(?:\s|$)/)
    expect(layers.className).not.toMatch(/(?:^|\s)order-/)
    expect(layers.className).not.toMatch(/:(?:flex-row|order-)/)
    expect(layers.children).toHaveLength(5)

    expect(chat.compareDocumentPosition(mindmap) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(mindmap.compareDocumentPosition(detail) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})

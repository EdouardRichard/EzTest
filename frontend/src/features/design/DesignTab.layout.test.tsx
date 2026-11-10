import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
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
  getAttachmentPendingDetails: vi.fn(),
}))
vi.mock("@/features/clarification/api", () => clarificationApi)

// The chat header mounts the login-user model selector.
const conversationApi = vi.hoisted(() => ({
  listPublicModels: vi.fn(),
  updateConversationModel: vi.fn(),
}))
vi.mock("@/features/conversation/api", () => conversationApi)

const versionsApi = vi.hoisted(() => ({ updateVersion: vi.fn() }))
vi.mock("@/features/versions/api", () => versionsApi)

vi.mock("@/features/clarification/useVersionEvents", () => ({
  useVersionEvents: () => "live",
}))

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

// The split-layout contract is about the DesignTab shell. React Flow is
// unrelated to it, so the canvas is replaced with a marker node.
vi.mock("./DesignMindmap", async () => {
  const React = await import("react")
  return {
    DesignMindmap: () => React.createElement("div", { "data-testid": "design-mindmap" }),
  }
})

import { versionFixture } from "@/features/versions/test-fixtures"

import { DesignTab } from "./DesignTab"
import { designCaseDetailFixture, designGraphFixture, designOverviewFixture } from "./test-fixtures"

const TIMESTAMP = "2026-10-31T12:00:00Z"

/**
 * Persisted workbench split preference (R-D2-2). The key is a literal here so
 * the first red run fails on the missing layout instead of a missing export.
 */
const WORKBENCH_SPLIT_KEY = "eztest.design.workbench-split"

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
  designApi.getDesignCase.mockResolvedValue(designCaseDetailFixture)
  conversationApi.listPublicModels.mockResolvedValue([])
  clarificationApi.listMessages.mockResolvedValue([])
  clarificationApi.getTeamRoom.mockResolvedValue({ team: "design", digest: null, posts: [] })
  versionsApi.updateVersion.mockResolvedValue(versionFixture)
}

async function renderWorkspace() {
  render(<DesignTab versionId="2001" />)
  await screen.findByTestId("design-mindmap")
  return {
    root: screen.getByTestId("design-scroll"),
    header: screen.getByTestId("design-header"),
    chat: screen.getByTestId("design-chat"),
    chatPane: screen.getByTestId("design-chat-pane"),
    main: screen.getByTestId("design-split-main"),
    workbenchSlot: screen.getByTestId("design-workbench-slot"),
    workbench: screen.getByTestId("design-workbench"),
    graph: screen.getByTestId("design-graph"),
    detail: screen.getByTestId("design-detail"),
    workbenchSplit: screen.getByTestId("design-split-workbench"),
    workbenchDivider: screen.getByTestId("design-split-workbench-divider"),
  }
}

/** jsdom has no layout engine; the drag maths only needs a definite height. */
function mockHeight(element: HTMLElement, height: number): void {
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

/**
 * jsdom has no layout engine, so the workspace viewport maths (D-01/D-03) needs
 * deterministic element boxes. Only the listed test ids get a box; everything
 * else stays at the jsdom default (top 0, height 0), which keeps the divider
 * overflow detection off unless a test asks for it.
 */
function mockViewportRects(rects: Record<string, Partial<DOMRect>>): void {
  const base: DOMRect = {
    x: 0,
    y: 0,
    width: 800,
    height: 0,
    top: 0,
    right: 800,
    bottom: 0,
    left: 0,
    toJSON: () => ({}),
  }
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement,
  ) {
    const override = rects[this.dataset.testid ?? ""] ?? {}
    return { ...base, ...override } as DOMRect
  })
}

/**
 * jsdom's matchMedia is a static false stub (vitest.setup.ts). The short-viewport
 * inset rule reads the real media query, so layout tests opt into a fixed match.
 */
function mockViewportMedia(matches: boolean): void {
  vi.spyOn(window, "matchMedia").mockReturnValue({
    matches,
    media: "(max-height: 840px)",
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
    dispatchEvent: vi.fn(() => false),
  } as unknown as MediaQueryList)
}

describe("DesignTab split layout (R-D2)", () => {
  beforeEach(() => {
    for (const fn of Object.values(designApi)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    for (const fn of Object.values(conversationApi)) fn.mockReset()
    versionsApi.updateVersion.mockReset()
    navigation.replace.mockReset()
    window.localStorage.clear()
    mockWorkspace()
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
    window.localStorage.clear()
  })

  it("renders the design header with version, host, context, model and actions", async () => {
    const { header } = await renderWorkspace()

    expect(screen.getByTestId("design-header-version")).toHaveTextContent("2001")
    expect(within(header).getByText("用例设计")).toBeInTheDocument()
    expect(screen.getByTestId("design-header-host")).toHaveTextContent("Design Host 单写者")
    // The status label belongs to the version event channel, not host health.
    expect(screen.getByTestId("design-header-host")).toHaveTextContent("事件通道：版本通道已连接")
    expect(screen.getByTestId("design-header-context")).toHaveTextContent("上下文水位")
    expect(screen.getByTestId("design-header-model")).toHaveTextContent("模型")
    expect(screen.getByTestId("design-stats")).toHaveTextContent("需求 2 · 场景 2 · 用例 3")
    expect(screen.getByTestId("export-cases")).toBeInTheDocument()
    expect(screen.getByTestId("auto-design-switch")).toBeInTheDocument()
    expect(screen.getByTestId("design-new-conversation")).toBeInTheDocument()
    expect(screen.getByTestId("design-workbench-zoom")).toHaveAttribute("aria-expanded", "false")
  })

  it("places the chat left of the workbench in a fixed grid without scrolling the page", async () => {
    const { root, chat, chatPane, graph, detail, main, workbench, workbenchSlot, workbenchSplit } =
      await renderWorkspace()

    // The outer shell is fixed to the viewport remainder and clips: only the
    // panels inside scroll.
    await waitFor(() => expect(root.style.height).toMatch(/^\d+px$/))
    expect(root).toHaveStyle({ overflow: "hidden" })
    expect(root).toHaveClass("overflow-hidden", "min-h-0", "min-w-0")

    // Fixed two-column grid: chat 40% (2fr) / workbench 60% (3fr). The outer
    // 40/60 split is not draggable any more.
    expect(main).toHaveClass(
      "grid",
      "min-h-0",
      "flex-1",
      "gap-3",
      "grid-cols-[minmax(0,2fr)_minmax(0,3fr)]",
    )
    expect(screen.queryByTestId("design-split-main-divider")).not.toBeInTheDocument()
    expect(screen.queryByTestId("design-split-main-top")).not.toBeInTheDocument()
    expect(screen.queryByTestId("design-split-main-bottom")).not.toBeInTheDocument()

    // Chat is the left column; the workbench owns the right column and keeps
    // its own graph/detail split.
    expect(main).toContainElement(chatPane)
    expect(chatPane).toContainElement(chat)
    expect(main).toContainElement(workbenchSlot)
    expect(workbenchSlot).toContainElement(workbench)
    expect(workbench).toContainElement(workbenchSplit)
    expect(workbenchSlot).toHaveClass("flex-1", "min-h-0", "min-w-0", "overflow-hidden")
    expect(chatPane.compareDocumentPosition(workbench) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
    expect(graph.compareDocumentPosition(detail) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )

    // Each panel owns its scroll: graph and detail auto, chat is clipped and
    // scrolls internally (message list + composer).
    expect(graph).toHaveStyle({ overflow: "auto" })
    expect(detail).toHaveStyle({ overflow: "auto" })
    expect(chatPane).toHaveStyle({ overflow: "hidden" })
    expect(chatPane).toHaveClass(
      "flex",
      "h-full",
      "min-h-0",
      "min-w-0",
      "flex-col",
      "overflow-hidden",
    )
  })

  it("defaults to a 65/35 graph/detail split inside the workbench", async () => {
    const { graph, detail } = await renderWorkspace()

    expect(graph.style.flexBasis).toBe("65%")
    expect(detail.style.flexBasis).toBe("35%")
  })

  it("drags the graph/detail divider and persists the ratio", async () => {
    const { workbenchSplit, workbenchDivider, graph, detail } = await renderWorkspace()
    mockHeight(workbenchSplit, 600)

    dragDivider(workbenchDivider, 100, 160)

    expect(graph.style.flexBasis).toBe("75%")
    expect(detail.style.flexBasis).toBe("25%")
    expect(window.localStorage.getItem(WORKBENCH_SPLIT_KEY)).toBe("75")

    cleanup()
    await renderWorkspace()
    expect(screen.getByTestId("design-graph").style.flexBasis).toBe("75%")
    expect(screen.getByTestId("design-detail").style.flexBasis).toBe("25%")
  })

  it("clamps the workbench split, resets on double-click and resizes from the keyboard", async () => {
    const { workbenchSplit, workbenchDivider, graph } = await renderWorkspace()
    mockHeight(workbenchSplit, 1000)

    // Overshoot both directions: the panes clamp exactly at their limits.
    dragDivider(workbenchDivider, 500, 1500)
    expect(Number.parseFloat(graph.style.flexBasis)).toBe(85)
    dragDivider(workbenchDivider, 500, -1500)
    expect(Number.parseFloat(graph.style.flexBasis)).toBe(25)

    // Double-click restores the approved default (65/35).
    fireEvent.doubleClick(workbenchDivider)
    expect(graph.style.flexBasis).toBe("65%")

    // Keyboard resize is the accessible equivalent of dragging: arrows step
    // ±5%, Home/End jump to the min/max split and the aria-label documents it.
    expect(workbenchDivider).toHaveAttribute("aria-label", expect.stringContaining("Home 最小"))
    expect(workbenchDivider).toHaveAttribute("aria-label", expect.stringContaining("End 最大"))
    fireEvent.keyDown(workbenchDivider, { key: "ArrowDown" })
    expect(graph.style.flexBasis).toBe("70%")
    fireEvent.keyDown(workbenchDivider, { key: "ArrowUp" })
    expect(graph.style.flexBasis).toBe("65%")
    fireEvent.keyDown(workbenchDivider, { key: "Home" })
    expect(graph.style.flexBasis).toBe("25%")
    fireEvent.keyDown(workbenchDivider, { key: "End" })
    expect(graph.style.flexBasis).toBe("85%")
  })

  it("opens the whole workbench as a 90vw×90vh dialog and closes with Escape", async () => {
    const { chat, graph, detail } = await renderWorkspace()

    fireEvent.click(screen.getByRole("button", { name: "放大工作区" }))

    const dialog = await screen.findByRole("dialog", { name: "用例工作区" })
    expect(dialog).toHaveClass("w-[90vw]", "h-[90vh]")

    // Graph above detail, same live components (not a second copy).
    expect(within(dialog).getByTestId("design-graph")).toBe(graph)
    expect(within(dialog).getByTestId("design-detail")).toBe(detail)
    expect(graph.compareDocumentPosition(detail) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(within(dialog).queryByTestId("design-chat")).not.toBeInTheDocument()
    expect(screen.getByTestId("design-chat")).toBe(chat)
    expect(screen.getByRole("button", { name: "还原工作区" })).toBeInTheDocument()

    fireEvent.keyDown(window, { key: "Escape" })

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    expect(screen.getByTestId("design-graph")).toBe(graph)
    expect(screen.getByRole("button", { name: "放大工作区" })).toBeInTheDocument()
  })

  it("closes the enlarged workbench with the restore button and the backdrop", async () => {
    await renderWorkspace()

    fireEvent.click(screen.getByRole("button", { name: "放大工作区" }))
    await screen.findByRole("dialog", { name: "用例工作区" })
    fireEvent.click(screen.getByRole("button", { name: "还原工作区" }))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    fireEvent.click(screen.getByRole("button", { name: "放大工作区" }))
    await screen.findByRole("dialog", { name: "用例工作区" })
    fireEvent.click(screen.getByTestId("design-focus-backdrop"))
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  })

  it("folds the chat into a full-width title bar below 960px and expands back to the left column", async () => {
    // Width alone triggers the collapse: 800px < 960px while the available
    // height stays above the short-viewport band (900px viewport).
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(800)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(900)

    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    const titleBar = await screen.findByTestId("design-chat-titlebar")
    expect(within(titleBar).getByRole("heading", { name: "设计对话" })).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat")).not.toBeInTheDocument()
    expect(screen.queryByTestId("message-list")).not.toBeInTheDocument()

    // Collapsed keeps the stacked form: the title bar on top and the workbench
    // filling the remaining full width below it.
    const main = screen.getByTestId("design-split-main")
    expect(main).toHaveAttribute("data-collapsed", "true")
    expect(main).toHaveClass("flex", "flex-col", "min-h-0", "flex-1")
    expect(main).not.toHaveClass("grid")
    expect(main).toContainElement(titleBar)
    expect(main).toContainElement(screen.getByTestId("design-workbench-slot"))
    expect(main).toContainElement(screen.getByTestId("design-workbench"))
    // The workbench takes the full remaining height below the title bar.
    expect(screen.getByTestId("design-workbench-slot")).toHaveClass("flex-1", "min-h-0")
    expect(screen.getByTestId("design-graph")).toBeInTheDocument()
    expect(screen.getByTestId("design-detail")).toBeInTheDocument()
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument()

    // No horizontal overflow: the column stays min-w-0 all the way down.
    const root = screen.getByTestId("design-scroll")
    expect(root).toHaveClass("overflow-hidden", "min-w-0")
    expect(main).toHaveClass("min-w-0")

    // The title bar expands the chat back into the left column.
    fireEvent.click(within(titleBar).getByTestId("design-chat-collapse-toggle"))
    expect(await screen.findByTestId("design-chat")).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat-titlebar")).not.toBeInTheDocument()
    expect(screen.getByTestId("design-split-main")).toHaveClass(
      "grid",
      "grid-cols-[minmax(0,2fr)_minmax(0,3fr)]",
    )
    expect(screen.getByTestId("design-chat-pane")).toContainElement(
      screen.getByTestId("design-chat"),
    )
  })

  it.each([
    [1440, 900],
    [1280, 800],
  ])("keeps the left chat on a %ix%i viewport", async (width, height) => {
    // The approved desktop sizes must show the chat inline: only a narrow
    // width (< 960px) auto-folds it.
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(width)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(height)

    const { main } = await renderWorkspace()

    expect(screen.getByTestId("design-chat")).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat-titlebar")).not.toBeInTheDocument()
    expect(main).toHaveAttribute("data-collapsed", "false")
    expect(main).toHaveClass("grid")
  })

  it("does not auto-collapse the chat on a short but wide viewport", async () => {
    // Short height only compacts the chrome now; it must not hide the chat.
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(560)

    const { main } = await renderWorkspace()

    expect(screen.getByTestId("design-chat")).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat-titlebar")).not.toBeInTheDocument()
    expect(main).toHaveClass("grid")
  })

  it("only auto-collapses when the width crosses into the narrow band", async () => {
    const width = vi.spyOn(window, "innerWidth", "get").mockReturnValue(1440)
    await renderWorkspace()
    expect(screen.getByTestId("design-chat")).toBeInTheDocument()

    // Entering the narrow band folds the chat.
    width.mockReturnValue(800)
    fireEvent(window, new Event("resize"))
    expect(await screen.findByTestId("design-chat-titlebar")).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat")).not.toBeInTheDocument()

    // The user expands it again: resizes inside the narrow band keep it open.
    fireEvent.click(screen.getByTestId("design-chat-collapse-toggle"))
    expect(await screen.findByTestId("design-chat")).toBeInTheDocument()
    width.mockReturnValue(880)
    fireEvent(window, new Event("resize"))
    expect(screen.getByTestId("design-chat")).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat-titlebar")).not.toBeInTheDocument()

    // Leaving and re-entering the band folds it again.
    width.mockReturnValue(1200)
    fireEvent(window, new Event("resize"))
    width.mockReturnValue(800)
    fireEvent(window, new Event("resize"))
    expect(await screen.findByTestId("design-chat-titlebar")).toBeInTheDocument()
  })

  it("gives the graph and detail panes readable pixel floors with internal scroll", async () => {
    const { graph, detail, workbenchSplit } = await renderWorkspace()

    // D-01: percentage-only panes collapsed the graph to ~110px and the canvas
    // to 0 on 1440x900. The floors keep both panes usable; the split scrolls
    // instead of squeezing them below their minimum.
    expect(graph.style.minHeight).toBe("220px")
    expect(detail.style.minHeight).toBe("180px")
    expect(workbenchSplit).toHaveClass("overflow-y-auto")
  })

  it("fits the workspace to a short viewport instead of forcing 320px", async () => {
    // D-03: 1280x800 has ~245px left below the version chrome (507 + the 48px
    // app-content inset); the old hard MIN of 320 pushed the page 75px tall.
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(1280)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(800)
    mockViewportRects({
      "design-scroll": { top: 507, height: 245, bottom: 752 },
      "design-split-workbench": { top: 0, height: 100, bottom: 100 },
    })

    // Short height compacts the chrome but keeps the left chat inline; the
    // workspace still fits the 245px remainder.
    const { root, workbenchSplit, workbenchDivider } = await renderWorkspace()

    await waitFor(() => expect(root.style.height).toBe("245px"))
    expect(screen.getByTestId("design-chat")).toBeInTheDocument()
    expect(workbenchSplit).toHaveClass("overflow-y-auto")
    expect(workbenchSplit).toHaveAttribute("data-overflowing", "true")

    // The short viewport compacts the design header: the duplicated host /
    // context / model rows are dropped, the title and stats stay.
    expect(screen.getByText("用例设计")).toBeInTheDocument()
    expect(screen.getByTestId("design-stats")).toBeInTheDocument()
    expect(screen.queryByTestId("design-header-host")).not.toBeInTheDocument()
    expect(screen.queryByTestId("design-header-context")).not.toBeInTheDocument()
    expect(screen.queryByTestId("design-header-model")).not.toBeInTheDocument()

    // Both panes keep their floors, so the divider is locked while the split
    // cannot honour the minimums.
    expect(screen.getByTestId("design-graph").style.minHeight).toBe("220px")
    expect(screen.getByTestId("design-detail").style.minHeight).toBe("180px")
    await waitFor(() => expect(workbenchDivider).toHaveAttribute("aria-disabled", "true"))
  })

  it("uses the full remaining viewport height and contains page scroll on a short viewport", async () => {
    // R2-D-01/D-02: compact chrome + inset 0 gives the workspace the whole
    // remainder; the negative margin absorbs the .app-content bottom padding so
    // the document still ends exactly at the viewport bottom.
    mockViewportMedia(true)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(800)
    mockViewportRects({ "design-scroll": { top: 371, height: 429, bottom: 800 } })

    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    const root = screen.getByTestId("design-scroll")
    await waitFor(() => expect(root.style.height).toBe("429px"))
    expect(root.style.marginBottom).toBe("-48px")
    // The positioning containing block clips the absolutely positioned sr-only
    // controls instead of letting them stretch the document (R2-D-02).
    expect(root).toHaveClass("relative", "overflow-hidden")
    expect(root.style.overflow).toBe("hidden")
  })

  it("keeps the approved 48px inset on a tall viewport", async () => {
    mockViewportMedia(false)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(900)
    mockViewportRects({ "design-scroll": { top: 400, height: 452, bottom: 852 } })

    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    const root = screen.getByTestId("design-scroll")
    await waitFor(() => expect(root.style.height).toBe("452px"))
    expect(root.style.marginBottom).toBe("")
    expect(root).toHaveClass("relative")
  })

  it("floors a fractional workspace height instead of rounding the document taller", async () => {
    // 1440x900: a fractional `top` (521.5) leaves 330.5px below the version
    // chrome once the 48px inset is reserved. Rounding up would set 331px and
    // make documentElement.scrollHeight 901 vs clientHeight 900; the inline
    // height must floor so the document never grows past the viewport.
    mockViewportMedia(false)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(900)
    mockViewportRects({ "design-scroll": { top: 521.5, height: 330.5, bottom: 852 } })

    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    const root = screen.getByTestId("design-scroll")
    await waitFor(() => expect(root.style.height).toBe("330px"))
    // Pins the direction: 330.5 must not become 331.
    expect(root.style.height).not.toBe("331px")
  })
})

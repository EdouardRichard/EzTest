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
 * Persisted split preferences (R-D2-2). Keys are literals here so the first red
 * run fails on the missing layout instead of a missing module export.
 */
const CHAT_SPLIT_KEY = "eztest.design.chat-split"
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
    graph: screen.getByTestId("design-graph"),
    detail: screen.getByTestId("design-detail"),
    mainSplit: screen.getByTestId("design-split-main"),
    mainTop: screen.getByTestId("design-split-main-top"),
    mainBottom: screen.getByTestId("design-split-main-bottom"),
    mainDivider: screen.getByTestId("design-split-main-divider"),
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

  it("stacks the chat over the graph and detail without scrolling the page", async () => {
    const { root, chat, graph, detail, mainSplit, workbenchSplit } = await renderWorkspace()

    // The outer shell is fixed to the viewport remainder and clips: only the
    // panels inside scroll.
    await waitFor(() => expect(root.style.height).toMatch(/^\d+px$/))
    expect(root).toHaveStyle({ overflow: "hidden" })
    expect(root).toHaveClass("overflow-hidden", "min-h-0", "min-w-0")

    // Chat above the workbench; inside it graph directly above detail.
    expect(chat.compareDocumentPosition(graph) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(graph.compareDocumentPosition(detail) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(
      mainSplit.compareDocumentPosition(workbenchSplit) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()

    // Each panel owns its scroll: graph and detail auto, chat is clipped and
    // scrolls internally (message list + composer).
    expect(graph).toHaveStyle({ overflow: "auto" })
    expect(detail).toHaveStyle({ overflow: "auto" })
    expect(screen.getByTestId("design-chat-pane")).toHaveStyle({ overflow: "hidden" })
  })

  it("defaults to a 40/60 chat split and a 65/35 graph/detail split", async () => {
    const { mainTop, mainBottom, graph, detail } = await renderWorkspace()

    expect(mainTop.style.flexBasis).toBe("40%")
    expect(mainBottom.style.flexBasis).toBe("60%")
    expect(graph.style.flexBasis).toBe("65%")
    expect(detail.style.flexBasis).toBe("35%")
  })

  it("drags the chat/workbench divider and persists the ratio", async () => {
    const { mainSplit, mainTop, mainBottom, mainDivider } = await renderWorkspace()
    mockHeight(mainSplit, 1000)

    dragDivider(mainDivider, 400, 500)

    expect(mainTop.style.flexBasis).toBe("50%")
    expect(mainBottom.style.flexBasis).toBe("50%")
    expect(window.localStorage.getItem(CHAT_SPLIT_KEY)).toBe("50")

    // The ratio survives a remount (refresh / navigation).
    cleanup()
    await renderWorkspace()
    expect(screen.getByTestId("design-split-main-top").style.flexBasis).toBe("50%")
    expect(screen.getByTestId("design-split-main-bottom").style.flexBasis).toBe("50%")
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

  it("clamps both splits, resets on double-click and resizes from the keyboard", async () => {
    const { mainSplit, mainTop, mainDivider, workbenchSplit, workbenchDivider, graph } =
      await renderWorkspace()
    mockHeight(mainSplit, 1000)
    mockHeight(workbenchSplit, 1000)

    // Overshoot both directions: the panes clamp exactly at their limits.
    dragDivider(mainDivider, 500, 1500)
    expect(Number.parseFloat(mainTop.style.flexBasis)).toBe(80)
    dragDivider(mainDivider, 500, -1500)
    expect(Number.parseFloat(mainTop.style.flexBasis)).toBe(20)

    dragDivider(workbenchDivider, 500, 1500)
    expect(Number.parseFloat(graph.style.flexBasis)).toBe(85)
    dragDivider(workbenchDivider, 500, -1500)
    expect(Number.parseFloat(graph.style.flexBasis)).toBe(25)

    // Double-click restores the approved defaults (40/60, 65/35).
    fireEvent.doubleClick(mainDivider)
    expect(mainTop.style.flexBasis).toBe("40%")
    fireEvent.doubleClick(workbenchDivider)
    expect(graph.style.flexBasis).toBe("65%")

    // Keyboard resize is the accessible equivalent of dragging: arrows step
    // ±5%, Home/End jump to the min/max split and the aria-label documents it.
    expect(mainDivider).toHaveAttribute("aria-label", expect.stringContaining("Home 最小"))
    expect(mainDivider).toHaveAttribute("aria-label", expect.stringContaining("End 最大"))
    fireEvent.keyDown(mainDivider, { key: "ArrowDown" })
    expect(mainTop.style.flexBasis).toBe("45%")
    fireEvent.keyDown(mainDivider, { key: "Home" })
    expect(mainTop.style.flexBasis).toBe("20%")
    fireEvent.keyDown(mainDivider, { key: "End" })
    expect(mainTop.style.flexBasis).toBe("80%")
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

  it("collapses the chat to a title bar on a short viewport and keeps the workbench usable", async () => {
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(560)

    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    const titleBar = await screen.findByTestId("design-chat-titlebar")
    expect(within(titleBar).getByRole("heading", { name: "设计对话" })).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat")).not.toBeInTheDocument()
    expect(screen.queryByTestId("message-list")).not.toBeInTheDocument()

    // Graph + detail are prioritised and the collapsed top does not consume a
    // percentage of the height.
    expect(screen.getByTestId("design-graph")).toBeInTheDocument()
    expect(screen.getByTestId("design-detail")).toBeInTheDocument()
    expect(screen.getByTestId("design-split-main-top").style.flex).toBe("0 0 auto")
    expect(screen.getByTestId("design-split-main-bottom").style.flex).toBe("1 1 0%")

    // No horizontal overflow, no Tabs: the column stays min-w-0 all the way.
    const root = screen.getByTestId("design-scroll")
    expect(root).toHaveClass("overflow-hidden", "min-w-0")
    expect(screen.getByTestId("design-split-main")).toHaveClass("min-w-0")
    expect(screen.getByTestId("design-split-main-bottom")).toHaveClass("min-w-0")
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument()

    // The title bar expands the chat again.
    fireEvent.click(within(titleBar).getByTestId("design-chat-collapse-toggle"))
    expect(await screen.findByTestId("design-chat")).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat-titlebar")).not.toBeInTheDocument()
  })

  it("collapses the chat on a narrow viewport with a normal height", async () => {
    // Width alone triggers the collapse: 800px < 960px while the available
    // height stays above the short-viewport band (900px viewport).
    vi.spyOn(window, "innerWidth", "get").mockReturnValue(800)
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(900)

    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-mindmap")

    const titleBar = await screen.findByTestId("design-chat-titlebar")
    expect(within(titleBar).getByRole("heading", { name: "设计对话" })).toBeInTheDocument()
    expect(screen.queryByTestId("design-chat")).not.toBeInTheDocument()

    // The graph and detail stay available and no tabbed fallback is introduced.
    expect(screen.getByTestId("design-graph")).toBeInTheDocument()
    expect(screen.getByTestId("design-detail")).toBeInTheDocument()
    expect(screen.queryByRole("tablist")).not.toBeInTheDocument()
  })

  it("collapses the chat when a resize enters the short-viewport band", async () => {
    await renderWorkspace()
    expect(screen.getByTestId("design-chat")).toBeInTheDocument()

    vi.spyOn(window, "innerHeight", "get").mockReturnValue(560)
    fireEvent(window, new Event("resize"))

    expect(await screen.findByTestId("design-chat-titlebar")).toBeInTheDocument()
    expect(screen.getByTestId("design-graph")).toBeInTheDocument()
    expect(screen.getByTestId("design-detail")).toBeInTheDocument()
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
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(800)
    mockViewportRects({
      "design-scroll": { top: 507, height: 245, bottom: 752 },
      "design-split-workbench": { top: 0, height: 100, bottom: 100 },
    })

    // The short viewport auto-folds the chat, so render the tab directly and
    // wait for the collapsed title bar instead of the full chat shell.
    render(<DesignTab versionId="2001" />)
    await screen.findByTestId("design-chat-titlebar")

    const root = screen.getByTestId("design-scroll")
    const workbenchSplit = screen.getByTestId("design-split-workbench")
    const workbenchDivider = screen.getByTestId("design-split-workbench-divider")

    await waitFor(() => expect(root.style.height).toBe("245px"))
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

  it("keeps a readable chat pane and a 200px workbench floor when expanded on a short viewport", async () => {
    // D-02: the expanded chat used to be 40% of a ~345px workspace, leaving
    // ~32px for the message list. DesignChat's own chrome (header + composer +
    // team room) measures ~222px, so the floor leaves ≥180px for the thread.
    vi.spyOn(window, "innerHeight", "get").mockReturnValue(800)
    mockViewportRects({ "design-scroll": { top: 507, height: 245, bottom: 752 } })

    render(<DesignTab versionId="2001" />)
    const titleBar = await screen.findByTestId("design-chat-titlebar")
    await waitFor(() => expect(screen.getByTestId("design-scroll").style.height).toBe("245px"))

    fireEvent.click(within(titleBar).getByTestId("design-chat-collapse-toggle"))

    const mainTop = await screen.findByTestId("design-split-main-top")
    expect(mainTop.style.minHeight).toBe("420px")
    expect(screen.getByTestId("design-split-main-bottom").style.minHeight).toBe("200px")
    expect(screen.getByTestId("design-split-main")).toHaveClass("overflow-y-auto")
  })
})

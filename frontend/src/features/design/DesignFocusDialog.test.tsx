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

const navigation = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
vi.mock("next/navigation", () => ({
  useRouter: () => navigation,
  usePathname: () => "/versions/2001",
}))

// The version channel runs through the real `useVersionEvents` hook so a
// re-subscription caused by the zoom toggle would be observable here.
const sse = vi.hoisted(() => ({ connectSSE: vi.fn(() => () => {}) }))
vi.mock("@/lib/sse-client", () => ({ connectSSE: sse.connectSSE }))

// Keep the real React Flow renderer (nodes must reach the DOM) but replace the
// imperative hook so the deferred re-fit can be observed and timed.
const reactFlow = vi.hoisted(() => ({
  fitView: vi.fn(),
  zoomIn: vi.fn(),
  zoomOut: vi.fn(),
}))
vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>()
  return { ...actual, useReactFlow: () => reactFlow }
})

import type { ConversationSummary } from "@/features/clarification/types"
import { versionFixture } from "@/features/versions/test-fixtures"

import { DesignTab } from "./DesignTab"
import { designCaseDetailFixture, designGraphFixture, designOverviewFixture } from "./test-fixtures"

const TIMESTAMP = "2026-10-31T12:00:00Z"
const FOCUS_TITLE = "脑图与用例详情"

const designConversation: ConversationSummary = {
  id: "4009",
  title: "用例设计 2026-10-31 12:00",
  kind: "design",
  status: "active",
  message_count: 2,
  last_message_at: TIMESTAMP,
  created_at: TIMESTAMP,
}

function mockWorkspace(): void {
  designApi.getDesignGraph.mockResolvedValue(designGraphFixture)
  designApi.getDesignOverview.mockResolvedValue(designOverviewFixture)
  designApi.listDesignConversations.mockResolvedValue([designConversation])
  designApi.getDesignCase.mockResolvedValue(designCaseDetailFixture)
  conversationApi.listPublicModels.mockResolvedValue([])
  clarificationApi.listMessages.mockResolvedValue([])
  clarificationApi.getTeamRoom.mockResolvedValue({ team: "design", digest: null, posts: [] })
  versionsApi.updateVersion.mockResolvedValue(versionFixture)
}

/** Every `data-testid` rendered twice would make the moved copy ambiguous. */
function duplicateTestIds(): string[] {
  const counts = new Map<string, number>()
  for (const element of document.querySelectorAll<HTMLElement>("[data-testid]")) {
    const testId = element.dataset.testid ?? ""
    counts.set(testId, (counts.get(testId) ?? 0) + 1)
  }
  return [...counts.entries()].filter(([, count]) => count > 1).map(([testId]) => testId)
}

async function renderWorkspace(): Promise<HTMLElement> {
  render(<DesignTab versionId="2001" />)
  return screen.findByTestId("design-mindmap")
}

function openWorkbenchZoom(): HTMLElement {
  fireEvent.click(screen.getByTestId("design-workbench-zoom"))
  return screen.getByRole("dialog", { name: FOCUS_TITLE })
}

describe("Design workbench whole-block zoom (R-D2-3)", () => {
  beforeEach(() => {
    for (const fn of Object.values(designApi)) fn.mockReset()
    for (const fn of Object.values(clarificationApi)) fn.mockReset()
    for (const fn of Object.values(conversationApi)) fn.mockReset()
    versionsApi.updateVersion.mockReset()
    navigation.replace.mockReset()
    reactFlow.fitView.mockReset()
    reactFlow.zoomIn.mockReset()
    reactFlow.zoomOut.mockReset()
    sse.connectSSE.mockReset()
    sse.connectSSE.mockImplementation(() => () => {})
    window.localStorage.clear()
    mockWorkspace()
  })

  afterEach(() => {
    cleanup()
    window.localStorage.clear()
  })

  it("opens the graph above detail at 65/35 and syncs the selected case into the detail", async () => {
    const mindmap = await renderWorkspace()
    const detail = screen.getByTestId("case-detail")
    const chat = screen.getByTestId("design-chat")

    const dialog = openWorkbenchZoom()

    // The very same nodes are moved into the dialog: not a second canvas or a
    // second detail panel (which would also duplicate their test ids).
    expect(dialog).toContainElement(screen.getByTestId("design-mindmap"))
    expect(dialog).toContainElement(screen.getByTestId("case-detail"))
    expect(screen.getByTestId("design-mindmap")).toBe(mindmap)
    expect(screen.getByTestId("case-detail")).toBe(detail)

    // The workbench keeps graph above detail, graph 65% / detail 35%.
    const graphPane = screen.getByTestId("design-graph")
    const detailPane = screen.getByTestId("design-detail")
    expect(dialog).toContainElement(graphPane)
    expect(dialog).toContainElement(detailPane)
    expect(graphPane.style.flexBasis).toBe("65%")
    expect(detailPane.style.flexBasis).toBe("35%")
    expect(
      graphPane.compareDocumentPosition(detailPane) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()

    // The graph keeps its own zoom/fit controls inside the enlarged workbench.
    expect(within(dialog).getByTestId("design-zoom-in")).toBeInTheDocument()
    expect(within(dialog).getByTestId("design-zoom-out")).toBeInTheDocument()
    expect(within(dialog).getByTestId("design-fit-view")).toBeInTheDocument()

    // The chat stays inline: only the workbench is enlarged.
    expect(dialog).not.toContainElement(chat)
    expect(chat).toBeInTheDocument()

    // A case picked on the focused canvas drives the focused detail panel.
    fireEvent.click(screen.getByTestId("case-select-7001"))

    await waitFor(() =>
      expect(screen.getByTestId("case-detail")).toHaveTextContent("退款 T+1 到账"),
    )
    expect(designApi.getDesignCase).toHaveBeenCalledWith("2001", "7001", undefined)
    expect(screen.getByTestId("case-card-7001")).toHaveAttribute("data-selected", "true")
  })

  it("closes back to the prior inline workbench, selection and scroll offsets", async () => {
    await renderWorkspace()
    fireEvent.click(screen.getByTestId("case-select-7001"))
    await waitFor(() =>
      expect(screen.getByTestId("case-detail")).toHaveTextContent("退款 T+1 到账"),
    )
    const detail = screen.getByTestId("case-detail")
    detail.scrollTop = 120

    expect(screen.getByTestId("design-graph").style.flexBasis).toBe("65%")
    expect(screen.getByTestId("design-detail").style.flexBasis).toBe("35%")

    openWorkbenchZoom()
    fireEvent.click(screen.getByTestId("design-focus-close"))

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    const restored = screen.getByTestId("case-detail")
    expect(restored).toBe(detail)
    expect(restored.scrollTop).toBe(120)
    expect(restored).toHaveTextContent("退款 T+1 到账")
    expect(screen.getByTestId("design-graph").style.flexBasis).toBe("65%")
    expect(screen.getByTestId("design-detail").style.flexBasis).toBe("35%")
    expect(screen.getByTestId("design-chat")).toBeInTheDocument()
    expect(screen.getByTestId("case-card-7001")).toHaveAttribute("data-selected", "true")
    expect(screen.getByTestId("design-workbench-zoom")).toHaveAttribute("aria-expanded", "false")
  })

  it("moves the workbench content instead of copying it, so no test id is duplicated", async () => {
    await renderWorkspace()
    const inlineDuplicates = duplicateTestIds()

    openWorkbenchZoom()

    expect(duplicateTestIds()).toEqual(inlineDuplicates)
    for (const testId of [
      "design-mindmap",
      "case-detail",
      "design-workbench",
      "design-graph",
      "design-detail",
      "design-chat",
    ]) {
      expect(screen.getAllByTestId(testId)).toHaveLength(1)
    }
    expect(screen.getByTestId("design-focus-dialog")).toHaveAttribute("aria-modal", "true")
    expect(screen.getByTestId("design-focus-title")).toHaveTextContent(FOCUS_TITLE)
  })

  it("keeps the version SSE subscription alive across open and close", async () => {
    await renderWorkspace()
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)

    openWorkbenchZoom()
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)

    fireEvent.click(screen.getByTestId("design-focus-close"))
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(sse.connectSSE).toHaveBeenCalledTimes(1)
  })

  it("re-fits the graph only after the enlarged layout has settled", async () => {
    await renderWorkspace()
    reactFlow.fitView.mockReset()
    const seenInsideDialog: boolean[] = []
    reactFlow.fitView.mockImplementation(() => {
      seenInsideDialog.push(document.querySelector('[data-testid="design-focus-dialog"]') !== null)
    })

    openWorkbenchZoom()

    // The size change is committed in a later frame: fitting against the very
    // tick that toggled the dialog would measure the stale inline canvas.
    expect(reactFlow.fitView).not.toHaveBeenCalled()

    await waitFor(() => expect(reactFlow.fitView).toHaveBeenCalledTimes(1))
    expect(seenInsideDialog[0]).toBe(true)
    expect(reactFlow.fitView.mock.calls[0]?.[0]).toMatchObject({ padding: 0.2 })

    fireEvent.click(screen.getByTestId("design-focus-close"))

    await waitFor(() => expect(reactFlow.fitView).toHaveBeenCalledTimes(2))
    expect(seenInsideDialog[1]).toBe(false)
  })
})

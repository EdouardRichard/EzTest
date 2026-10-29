import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { Version } from "@/features/versions/types"

import type { BaselineView, CaseNodeView, MindmapGraph } from "./types"

const mocks = vi.hoisted(() => ({
  useMindmap: vi.fn(),
  getVersion: vi.fn(),
  listRequirements: vi.fn(),
  createNode: vi.fn(),
  startRun: vi.fn(),
  listFindings: vi.fn(),
  getRun: vi.fn(),
  createExport: vi.fn(),
  downloadExport: vi.fn(),
  svgToPngBlob: vi.fn(),
  downloadBlob: vi.fn(),
}))

// MindmapPage renders next/link banners and the execution hook module through
// VersionHeader's status metadata; the shell's providers are not under test.
vi.mock("next/navigation", () => ({ usePathname: () => "/" }))
vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "tester", role: "admin", status: "active" },
    loading: false,
    logout: vi.fn(),
  }),
}))

// The embedded contract is a pure assembly of `useMindmap` + the existing
// panels, so the loader is mocked and each test drives the graph state.
vi.mock("./useMindmap", () => ({ useMindmap: mocks.useMindmap }))

vi.mock("./api", () => ({
  mindmapApi: {
    createNode: mocks.createNode,
    startRun: mocks.startRun,
    listFindings: mocks.listFindings,
    getRun: mocks.getRun,
    createExport: mocks.createExport,
    downloadExport: mocks.downloadExport,
  },
}))

vi.mock("./png-export", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./png-export")>()
  return { ...actual, svgToPngBlob: mocks.svgToPngBlob, downloadBlob: mocks.downloadBlob }
})

vi.mock("@/features/versions/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/versions/api")>()
  return { ...actual, getVersion: mocks.getVersion, listRequirements: mocks.listRequirements }
})

import { MindmapPage } from "./MindmapPage"

/** Snowflake IDs that a JS number cannot hold; they must stay strings verbatim. */
const VERSION_ID = "1844674407370955161"
const REQ_ID = "1844674407370955201"
const SCENARIO_ID = "1844674407370955101"
const CASE_ID = "1844674407370955103"
const UNKNOWN_CASE_ID = "1844674407370955999"
const TIMESTAMP = "2026-10-28T10:00:00Z"

const VERSION: Version = {
  id: VERSION_ID,
  name: "订单系统 2.0",
  label: "v2.0",
  description: "",
  status: "designing",
  systemId: 1,
  environmentId: 2,
  codeRepositoryId: 3,
  codeBranch: "main",
  codeBaselineCommit: null,
  bindings: {
    system: { id: 1, name: "订单系统", available: true },
    environment: { id: 2, name: "staging", available: true },
    codeRepository: { id: 3, name: "order-web", available: true },
  },
  requirementCount: 1,
  clarifiedCount: 1,
  openQuestionCount: 0,
  revision: 2,
  createdBy: 1,
  updatedBy: 1,
  createdAt: TIMESTAMP,
  updatedAt: TIMESTAMP,
}

function scenarioNode(): CaseNodeView {
  return {
    id: SCENARIO_ID,
    parentId: null,
    kind: "scenario",
    title: "订单场景",
    description: "",
    priority: "P2",
    preconditions: "",
    testData: null,
    source: "user_input",
    status: "draft",
    sortOrder: 0,
    revision: 1,
    steps: [],
    assertions: [],
    sources: [],
    anchors: [],
    openBlockerCount: 0,
  }
}

function caseNode(): CaseNodeView {
  return {
    id: CASE_ID,
    parentId: SCENARIO_ID,
    kind: "case",
    title: "取消订单",
    description: "",
    priority: "P1",
    preconditions: "",
    testData: null,
    source: "user_input",
    status: "draft",
    sortOrder: 1,
    revision: 3,
    steps: [],
    assertions: [],
    sources: [],
    anchors: [],
    openBlockerCount: 0,
  }
}

const BASELINE: BaselineView = {
  id: "1844674407370955601",
  graphHash: "a".repeat(64),
  caseCount: 1,
  nodeCount: 2,
  frozenAt: TIMESTAMP,
  codeBaselineCommit: null,
}

function defaultGraph(overrides: Partial<MindmapGraph> = {}): MindmapGraph {
  return {
    versionId: VERSION_ID,
    versionStatus: "designing",
    editable: true,
    nodes: [scenarioNode(), caseNode()],
    coverage: {
      requirementIds: [REQ_ID],
      uncoveredRequirementIds: [],
      staleRequirementIds: [],
      matrix: [{ requirementId: REQ_ID, caseIds: [CASE_ID] }],
    },
    baseline: null,
    latestRuns: {},
    impactCases: [],
    ...overrides,
  }
}

type MindmapState = {
  loading?: boolean
  error?: string
  streamStatus?: "connecting" | "live" | "reconnecting" | "closed"
}

function mockGraph(graph: MindmapGraph | null, state: MindmapState = {}) {
  const refresh = vi.fn().mockResolvedValue(undefined)
  mocks.useMindmap.mockReturnValue({
    graph,
    loading: state.loading ?? false,
    error: state.error ?? "",
    streamStatus: state.streamStatus ?? "live",
    refresh,
    applyGraph: vi.fn(),
  })
  return { refresh }
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getVersion.mockResolvedValue(VERSION)
  mocks.listRequirements.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
  mocks.listFindings.mockResolvedValue([])
})

afterEach(cleanup)

describe("MindmapPage embedded", () => {
  it("hides the full-screen header and back link but keeps canvas, review and export", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} embedded />)

    expect(await screen.findByTestId("mindmap-canvas")).toBeTruthy()
    expect(screen.queryByTestId("mindmap-toolbar")).toBeNull()
    expect(screen.queryByTestId("back-to-version")).toBeNull()
    expect(screen.getByTestId("mindmap-section-review")).toBeTruthy()
    expect(screen.getByTestId("mindmap-section-runs")).toBeTruthy()
    expect(screen.getByTestId("mindmap-section-export")).toBeTruthy()
  })

  it("shows a compact reconnect status and refreshes on demand in embedded mode", async () => {
    const { refresh } = mockGraph(defaultGraph(), { streamStatus: "reconnecting" })

    render(<MindmapPage versionId={VERSION_ID} embedded />)

    const bar = await screen.findByTestId("mindmap-embedded-status")
    const badge = within(bar).getByTestId("stream-status")
    expect(badge.getAttribute("data-status")).toBe("reconnecting")
    expect(badge.textContent).toContain("重连中")

    fireEvent.click(within(bar).getByTestId("btn-refresh-mindmap"))
    expect(refresh).toHaveBeenCalledTimes(1)

    // The full-screen header (title, back link, baseline badge) stays dropped.
    expect(screen.queryByTestId("mindmap-toolbar")).toBeNull()
    expect(screen.queryByTestId("mindmap-version-name")).toBeNull()
    expect(screen.queryByTestId("back-to-version")).toBeNull()
  })

  it("keeps the compact status bar and refresh action while the stream is live", async () => {
    mockGraph(defaultGraph(), { streamStatus: "live" })

    render(<MindmapPage versionId={VERSION_ID} embedded />)

    const bar = await screen.findByTestId("mindmap-embedded-status")
    expect(within(bar).getByTestId("stream-status").textContent).toContain("实时")
    expect(within(bar).getByTestId("btn-refresh-mindmap")).toBeTruthy()
  })

  it("shows the status bar while the graph is still loading", async () => {
    mockGraph(null, { loading: true, streamStatus: "connecting" })

    render(<MindmapPage versionId={VERSION_ID} embedded />)

    const bar = await screen.findByTestId("mindmap-embedded-status")
    expect(within(bar).getByTestId("stream-status").textContent).toContain("连接中")
    expect(within(bar).getByTestId("btn-refresh-mindmap")).toBeTruthy()
    expect(screen.getByTestId("mindmap-loading")).toBeTruthy()
  })

  it("gives the embedded canvas an explicit height and lets it fill the container", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} embedded />)

    const column = await screen.findByTestId("mindmap-canvas-column")
    expect(column.className).toMatch(/h-\[calc\(100vh-/)

    const canvas = screen.getByTestId("mindmap-canvas")
    expect(canvas.getAttribute("data-embedded")).toBe("true")
    expect(canvas.className).toContain("h-full")

    // The viewport follows the container instead of the standalone 70vh height.
    const viewport = screen.getByTestId("mindmap-viewport")
    expect(viewport.className).not.toContain("h-[70vh]")
    expect(viewport.className).toContain("flex-1")
  })

  it("keeps the standalone viewport height on the full-screen route", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} />)

    const column = await screen.findByTestId("mindmap-canvas-column")
    expect(column.className).not.toMatch(/h-\[calc\(100vh-/)
    const canvas = screen.getByTestId("mindmap-canvas")
    expect(canvas.getAttribute("data-embedded")).toBeNull()
    expect(canvas.className).not.toContain("h-full")
    expect(screen.getByTestId("mindmap-viewport").className).toContain("h-[70vh]")
  })

  it("keeps the embedded loading and error states inside the tab", async () => {
    mockGraph(null, { loading: true })

    const loading = render(<MindmapPage versionId={VERSION_ID} embedded />)
    expect(await screen.findByTestId("mindmap-loading")).toBeTruthy()
    expect(screen.queryByTestId("back-to-version")).toBeNull()
    loading.unmount()

    mockGraph(null, { error: "操作失败，请稍后重试。" })
    render(<MindmapPage versionId={VERSION_ID} embedded />)
    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载用例脑图失败")
    expect(screen.queryByTestId("back-to-version")).toBeNull()
  })

  it("selects the ?case= node that arrives through the deep link", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} embedded initialCaseId={CASE_ID} />)

    expect(await screen.findByTestId("case-editor")).toBeTruthy()
    expect((screen.getByLabelText("用例标题") as HTMLInputElement).value).toBe("取消订单")
    expect(screen.queryByTestId("mindmap-case-not-found")).toBeNull()
  })

  it("selects a scenario deep link as well", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} embedded initialCaseId={SCENARIO_ID} />)

    expect(await screen.findByTestId("scenario-editor")).toBeTruthy()
    expect(screen.queryByTestId("case-editor")).toBeNull()
  })

  it("shows a notice instead of crashing for an illegal case deep link", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} embedded initialCaseId={UNKNOWN_CASE_ID} />)

    const notice = await screen.findByTestId("mindmap-case-not-found")
    expect(notice.textContent).toContain("未找到")
    expect(screen.getByTestId("mindmap-canvas")).toBeTruthy()
    expect(screen.getByTestId("editor-placeholder")).toBeTruthy()
  })

  it("reports every canvas selection through onCaseChange", async () => {
    mockGraph(defaultGraph())
    const onCaseChange = vi.fn()

    render(<MindmapPage versionId={VERSION_ID} embedded onCaseChange={onCaseChange} />)

    fireEvent.click(await screen.findByTestId(`case-node-${CASE_ID}`))
    expect(onCaseChange).toHaveBeenLastCalledWith(CASE_ID)

    fireEvent.click(screen.getByTestId(`scenario-node-${SCENARIO_ID}`))
    expect(onCaseChange).toHaveBeenLastCalledWith(SCENARIO_ID)
  })

  it("reuses the container's version and stream instead of re-reading them", async () => {
    mockGraph(defaultGraph())
    const stream = { events: [], status: "live" as const, reconnect: vi.fn() }

    render(<MindmapPage versionId={VERSION_ID} embedded version={VERSION} stream={stream} />)

    expect(await screen.findByTestId("mindmap-canvas")).toBeTruthy()
    // The version detail already loaded the version and owns the SSE stream:
    // the embedded tab must not pay for a second read/connection.
    expect(mocks.getVersion).not.toHaveBeenCalled()
    expect(mocks.useMindmap).toHaveBeenCalledWith(VERSION_ID, stream)
  })

  it("keeps the full-screen resume link in non-embedded mode", async () => {
    // Regression guard for the shared page: only the embedded flag removes the
    // full-screen chrome, so the existing route keeps working unchanged.
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} />)

    expect(await screen.findByTestId("mindmap-toolbar")).toBeTruthy()
    expect(screen.getByTestId("back-to-version").getAttribute("href")).toBe(
      `/versions/${VERSION_ID}`,
    )
    // Without a provided stream the page owns one (second arg stays undefined).
    expect(mocks.useMindmap).toHaveBeenCalledWith(VERSION_ID, undefined)
    expect(await screen.findByTestId("mindmap-version-name")).toBeTruthy()
  })

  it("renders the same baseline read-only surface in embedded mode", async () => {
    mockGraph(defaultGraph({ versionStatus: "executing", editable: false, baseline: BASELINE }))

    render(<MindmapPage versionId={VERSION_ID} embedded />)

    const banner = await screen.findByTestId("readonly-banner")
    expect(banner.textContent).toContain("只读")
    // The embedded tab hides the full-screen baseline badge as well.
    expect(screen.queryByTestId("toolbar-baseline")).toBeNull()
    expect(screen.queryByTestId("mindmap-toolbar")).toBeNull()
    fireEvent.click(screen.getByTestId(`case-node-${CASE_ID}`))
    expect(screen.getByTestId("case-editor-readonly")).toBeTruthy()
  })
})

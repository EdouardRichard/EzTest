import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { Version } from "@/features/versions/types"

import type { BaselineView, CaseNodeView, MindmapGraph } from "./types"

const nav = vi.hoisted(() => ({ pathname: "/" }))

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

// The shell reads the route to decide whether the page is full-bleed; the test
// flips this between renders instead of navigating.
vi.mock("next/navigation", () => ({ usePathname: () => nav.pathname }))

// AppShell renders the user menu; the shell test only needs a signed-in user.
vi.mock("@/features/auth/AuthProvider", () => ({
  useAuth: () => ({
    user: { id: 1, username: "tester", role: "admin", status: "active" },
    loading: false,
    logout: vi.fn(),
  }),
}))

// The page is a pure assembly of `useMindmap` + the T20–T23 panels, so the
// loader is mocked and the graph state is driven by each test.
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

// PNG export is a browser boundary; the page test only proves the live canvas
// SVG reaches ExportMenu and that the version-named download is triggered.
vi.mock("./png-export", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./png-export")>()
  return { ...actual, svgToPngBlob: mocks.svgToPngBlob, downloadBlob: mocks.downloadBlob }
})

vi.mock("@/features/versions/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/features/versions/api")>()
  return { ...actual, getVersion: mocks.getVersion, listRequirements: mocks.listRequirements }
})

import MindmapRoutePage from "@/app/(app)/versions/[id]/mindmap/page"

import { AppShell } from "@/features/shell/AppShell"
import { OverviewTab } from "@/features/versions/OverviewTab"
import { VersionHeader } from "@/features/versions/VersionHeader"

import { MindmapPage } from "./MindmapPage"

/** Snowflake IDs that a JS number cannot hold; they must stay strings verbatim. */
const VERSION_ID = "1844674407370955161"
const REQ_ID = "1844674407370955201"
const SCENARIO_ID = "1844674407370955101"
const CASE_ID = "1844674407370955103"
const BASELINE_ID = "1844674407370955601"
const RUN_ID = "1844674407370955401"
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

function scenarioNode(overrides: Partial<CaseNodeView> = {}): CaseNodeView {
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
    ...overrides,
  }
}

function caseNode(overrides: Partial<CaseNodeView> = {}): CaseNodeView {
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
    ...overrides,
  }
}

const BASELINE: BaselineView = {
  id: BASELINE_ID,
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
  nav.pathname = "/"
  mocks.getVersion.mockResolvedValue(VERSION)
  mocks.listRequirements.mockResolvedValue({ items: [], nextCursor: "", hasMore: false })
  mocks.listFindings.mockResolvedValue([])
  mocks.startRun.mockResolvedValue({ runId: RUN_ID })
  mocks.createNode.mockResolvedValue(caseNode())
  mocks.svgToPngBlob.mockResolvedValue(new Blob(["png"], { type: "image/png" }))
  mocks.downloadBlob.mockImplementation(() => undefined)
})

afterEach(cleanup)

describe("MindmapPage", () => {
  it("renders the empty graph with the AI and manual dual entry", async () => {
    mockGraph(defaultGraph({ nodes: [] }))

    render(<MindmapPage versionId={VERSION_ID} />)

    expect(await screen.findByTestId("mindmap-empty")).toBeTruthy()
    expect(screen.getByTestId("btn-empty-generate")).toBeTruthy()
    expect(screen.getByTestId("btn-empty-manual")).toBeTruthy()
    expect(screen.queryByTestId("mindmap-canvas")).toBeNull()
  })

  it("guides to the P3 requirement workbench when no requirement is confirmed", async () => {
    mockGraph(
      defaultGraph({
        nodes: [],
        coverage: {
          requirementIds: [],
          uncoveredRequirementIds: [],
          staleRequirementIds: [],
          matrix: [],
        },
      }),
    )

    render(<MindmapPage versionId={VERSION_ID} />)

    const link = (await screen.findByTestId("goto-requirements")) as HTMLAnchorElement
    expect(link.getAttribute("href")).toBe(`/versions/${VERSION_ID}?tab=requirements`)
    expect(screen.queryByTestId("btn-empty-generate")).toBeNull()
  })

  it("creates a manual case node bound to the first confirmed requirement", async () => {
    const { refresh } = mockGraph(defaultGraph({ nodes: [] }))

    render(<MindmapPage versionId={VERSION_ID} />)

    fireEvent.click(await screen.findByTestId("btn-empty-manual"))
    fireEvent.change(screen.getByLabelText("节点类型"), { target: { value: "case" } })
    fireEvent.change(screen.getByLabelText("节点标题"), { target: { value: "手工新建用例" } })
    fireEvent.click(screen.getByTestId("btn-manual-create"))

    await waitFor(() =>
      expect(mocks.createNode).toHaveBeenCalledWith(VERSION_ID, {
        kind: "case",
        parentId: null,
        title: "手工新建用例",
        requirementIds: [REQ_ID],
      }),
    )
    await waitFor(() => expect(refresh).toHaveBeenCalled())
  })

  it("starts an append design run from the empty state", async () => {
    const { refresh } = mockGraph(defaultGraph({ nodes: [] }))

    render(<MindmapPage versionId={VERSION_ID} />)

    fireEvent.click(await screen.findByTestId("btn-empty-generate"))

    await waitFor(() =>
      expect(mocks.startRun).toHaveBeenCalledWith(VERSION_ID, { kind: "design", mode: "append" }),
    )
    await waitFor(() => expect(refresh).toHaveBeenCalled())
  })

  it("renders the canvas and switches the right panel with the selected node", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} />)

    expect(await screen.findByTestId("mindmap-canvas")).toBeTruthy()

    fireEvent.click(screen.getByTestId(`case-node-${CASE_ID}`))
    expect(screen.getByTestId("case-editor")).toBeTruthy()
    expect(screen.getByTestId("source-panel")).toBeTruthy()
    expect((screen.getByLabelText("用例标题") as HTMLInputElement).value).toBe("取消订单")

    fireEvent.click(screen.getByTestId(`scenario-node-${SCENARIO_ID}`))
    expect(screen.getByTestId("scenario-editor")).toBeTruthy()
    expect(screen.queryByTestId("case-editor")).toBeNull()
  })

  it("shows the read-only banner and disables editing once the baseline is frozen", async () => {
    mockGraph(
      defaultGraph({
        versionStatus: "executing",
        editable: false,
        baseline: BASELINE,
      }),
    )

    render(<MindmapPage versionId={VERSION_ID} />)

    const banner = await screen.findByTestId("readonly-banner")
    expect(banner.textContent).toContain("只读")
    expect(screen.getByTestId("toolbar-baseline").textContent).toContain("已绑定基线")

    const runs = screen.getByTestId("run-panel").closest("fieldset") as HTMLFieldSetElement | null
    expect(runs?.disabled).toBe(true)

    fireEvent.click(screen.getByTestId(`case-node-${CASE_ID}`))
    expect(screen.getByTestId("case-editor-readonly")).toBeTruthy()
    expect((screen.getByLabelText("用例标题") as HTMLInputElement).disabled).toBe(true)
  })

  it("exports the live canvas as a version-named PNG", async () => {
    mockGraph(defaultGraph())

    render(<MindmapPage versionId={VERSION_ID} />)

    // The version name arrives asynchronously; wait for the toolbar title.
    await waitFor(() =>
      expect(screen.getByTestId("mindmap-version-name").textContent).toBe("订单系统 2.0"),
    )

    fireEvent.click(screen.getByTestId("btn-export-png"))

    await waitFor(() => expect(mocks.svgToPngBlob).toHaveBeenCalled())
    expect(mocks.svgToPngBlob.mock.calls[0]?.[0]).toBe(screen.getByRole("tree"))
    expect(mocks.downloadBlob).toHaveBeenCalledWith(expect.any(Blob), "订单系统 2.0-mindmap.png")
    expect(mocks.createExport).not.toHaveBeenCalled()
  })

  it("maps the SSE status to 实时 / 重连中 / 断开", async () => {
    mockGraph(defaultGraph(), { streamStatus: "live" })

    const { rerender } = render(<MindmapPage versionId={VERSION_ID} />)

    const toolbar = await screen.findByTestId("mindmap-toolbar")
    expect(within(toolbar).getByTestId("stream-status").textContent).toContain("实时")

    mockGraph(defaultGraph(), { streamStatus: "reconnecting" })
    rerender(<MindmapPage versionId={VERSION_ID} />)
    expect(
      within(screen.getByTestId("mindmap-toolbar")).getByTestId("stream-status").textContent,
    ).toContain("重连中")

    mockGraph(defaultGraph(), { streamStatus: "closed" })
    rerender(<MindmapPage versionId={VERSION_ID} />)
    expect(
      within(screen.getByTestId("mindmap-toolbar")).getByTestId("stream-status").textContent,
    ).toContain("断开")
  })

  it("shows the loading state while the first graph read is in flight", async () => {
    mockGraph(null, { loading: true })

    render(<MindmapPage versionId={VERSION_ID} />)

    expect(await screen.findByTestId("mindmap-loading")).toBeTruthy()
    expect(screen.queryByRole("alert")).toBeNull()
  })

  it("reports a load failure and retries through the loader refresh", async () => {
    const { refresh } = mockGraph(null, {
      error: "操作失败，请稍后重试。",
    })

    render(<MindmapPage versionId={VERSION_ID} />)

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("加载用例脑图失败")
    expect(alert.textContent).toContain("操作失败，请稍后重试。")

    fireEvent.click(within(alert).getByRole("button", { name: "重试" }))
    await waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
  })

  it("renders the route page with the string id from the dynamic segment", async () => {
    mockGraph(defaultGraph())

    const route = await MindmapRoutePage({ params: Promise.resolve({ id: VERSION_ID }) })
    render(route)

    expect(await screen.findByTestId("mindmap-canvas")).toBeTruthy()
    expect(mocks.useMindmap).toHaveBeenCalledWith(VERSION_ID)
  })

  it("opens the mindmap from the version header and the overview card", async () => {
    const header = render(
      <VersionHeader
        version={VERSION}
        refreshing={false}
        onRefresh={vi.fn()}
        onUpdated={vi.fn()}
      />,
    )

    const headerLink = screen.getByTestId("open-mindmap")
    expect(headerLink.getAttribute("href")).toBe(`/versions/${VERSION_ID}/mindmap`)
    expect(headerLink.textContent).toContain("用例脑图")
    header.unmount()

    render(<OverviewTab version={VERSION} />)
    const overviewLink = screen.getByTestId("overview-open-mindmap")
    expect(overviewLink.getAttribute("href")).toBe(`/versions/${VERSION_ID}/mindmap`)
    expect(overviewLink.textContent).toContain("用例脑图")
  })
})

describe("AppShell", () => {
  it("uses the full-bleed content width only on the mindmap route", () => {
    nav.pathname = `/versions/${VERSION_ID}/mindmap`
    const first = render(
      <AppShell>
        <p>脑图内容</p>
      </AppShell>,
    )
    const fullBleed = screen.getByTestId("app-content")
    expect(fullBleed.className).toContain("max-w-none")
    expect(fullBleed.className).not.toContain("max-w-6xl")
    first.unmount()

    for (const pathname of [
      `/versions/${VERSION_ID}`,
      `/versions/${VERSION_ID}/mindmap/extra`,
      "/versions",
    ]) {
      nav.pathname = pathname
      const rendered = render(
        <AppShell>
          <p>其他页面</p>
        </AppShell>,
      )
      const content = screen.getByTestId("app-content")
      expect(content.className).toContain("max-w-6xl")
      expect(content.className).not.toContain("max-w-none")
      rendered.unmount()
    }
  })
})

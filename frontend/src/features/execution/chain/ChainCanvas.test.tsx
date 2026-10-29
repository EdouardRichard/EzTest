import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import dynamic from "next/dynamic"
import type { ComponentProps } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import type { ChainCanvasProps } from "./ChainCanvas"
import { layoutChain } from "./chain-layout"
import { CHAIN_COLOR_STYLES, EDGE_KIND_TOKENS } from "./chain-tokens"
import type { ChainSnapshot, ChainSnapshotEdge, ChainSnapshotNode } from "./chain-types"

/**
 * The canvas is always reached through the same `next/dynamic(..., { ssr:
 * false })` boundary Task 11 will use, so the tests exercise the real lazy
 * boundary of the product instead of importing the client module directly.
 *
 * Note: this proves lazy loading only. The suite runs in jsdom, where
 * `next/dynamic` resolves the loader immediately; it is no evidence that the
 * module is SSR-safe, which is exactly why Task 11 must keep `ssr: false`.
 */
const ChainCanvas = dynamic(() => import("./ChainCanvas"), { ssr: false })

/**
 * Prop inspection probe. `@xyflow/react` is a visual library that cannot report
 * its own configuration from jsdom, so the mock wraps the real `ReactFlow`
 * component (it still renders it) and records the props it received. Only
 * `useReactFlow` is replaced so the follow tests can observe `setCenter`.
 */
const flowProbe = vi.hoisted(() => ({
  props: null as Record<string, unknown> | null,
  setCenter: vi.fn(),
}))

vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>()
  const React = await import("react")

  function ReactFlowProbe(props: Record<string, unknown>) {
    flowProbe.props = props
    return React.createElement(actual.ReactFlow, props as ComponentProps<typeof actual.ReactFlow>)
  }
  ReactFlowProbe.displayName = "ReactFlowProbe"

  function useProbedReactFlow() {
    return { ...actual.useReactFlow(), setCenter: flowProbe.setCenter }
  }

  return {
    ...actual,
    ReactFlow: ReactFlowProbe as unknown as typeof actual.ReactFlow,
    useReactFlow: useProbedReactFlow as unknown as typeof actual.useReactFlow,
  }
})

function makeSnapshot(nodes: ChainSnapshotNode[], edges: ChainSnapshotEdge[]): ChainSnapshot {
  const count = (kind: ChainSnapshotNode["kind"]): number =>
    nodes.filter((node) => node.kind === kind).length
  return {
    versionId: "version:1",
    batch: { id: "batch:1", batchNo: 1, kind: "full", status: "completed" },
    nodes,
    edges,
    counts: {
      requirements: count("requirement"),
      cases: count("case"),
      results: count("result"),
      issues: count("issue"),
      byResultStatus: {},
      byIssueSeverity: {},
      uncoveredRequirements: 0,
      revalidationCases: 0,
      supplementCases: 0,
    },
    truncated: {
      requirements: false,
      cases: false,
      results: false,
      issues: false,
    },
    generatedAt: "2026-10-29T00:00:00Z",
  }
}

const SMALL_NODES: ChainSnapshotNode[] = [
  {
    id: "requirement:1",
    kind: "requirement",
    title: "登录需求",
    status: "clarified",
    priority: "must",
    caseCount: 2,
  },
  {
    id: "case:1",
    kind: "case",
    title: "登录成功",
    status: "reviewed",
    requirementIds: ["requirement:1"],
  },
  {
    id: "case:2",
    kind: "case",
    title: "登录失败",
    status: "needs_revalidation",
    requirementIds: ["requirement:1"],
  },
  {
    id: "result:1",
    kind: "result",
    title: "登录成功",
    status: "passed",
    caseNodeId: "case:1",
    attempt: 1,
  },
  {
    id: "result:2",
    kind: "result",
    title: "登录成功",
    status: "running",
    caseNodeId: "case:1",
    attempt: 2,
  },
  {
    id: "result:3",
    kind: "result",
    title: "登录失败",
    status: "failed",
    caseNodeId: "case:1",
    attempt: 3,
  },
  {
    id: "issue:1",
    kind: "issue",
    title: "会话超时",
    status: "open",
    severity: "major",
    caseNodeId: "case:1",
  },
  {
    id: "issue:2",
    kind: "issue",
    title: "文案错误",
    status: "open",
    severity: "minor",
    caseNodeId: "case:1",
  },
]

const SMALL_EDGES: ChainSnapshotEdge[] = [
  {
    id: "e-req-case",
    source: "requirement:1",
    target: "case:1",
    kind: "requirement_case",
  },
  { id: "e-impact", source: "requirement:1", target: "case:2", kind: "impact_case" },
  { id: "e-passed", source: "case:1", target: "result:1", kind: "case_result" },
  { id: "e-running", source: "case:1", target: "result:2", kind: "case_result" },
  { id: "e-failed", source: "case:1", target: "result:3", kind: "case_result" },
  {
    id: "e-result-issue",
    source: "result:2",
    target: "issue:1",
    kind: "result_issue",
  },
  { id: "e-case-issue", source: "case:1", target: "issue:2", kind: "case_issue" },
]

const SMALL_SNAPSHOT = makeSnapshot(SMALL_NODES, SMALL_EDGES)

/** A new snapshot object with the same graph, as an SSE refresh would deliver. */
function refreshed(snapshot: ChainSnapshot): ChainSnapshot {
  return makeSnapshot(
    snapshot.nodes.map((node) => ({ ...node })),
    snapshot.edges.map((edge) => ({ ...edge })),
  )
}

/** 200 nodes (50 of each layer) linked into a tall, mostly off-screen graph. */
function largeSnapshot(): ChainSnapshot {
  const nodes: ChainSnapshotNode[] = []
  const edges: ChainSnapshotEdge[] = []
  for (let index = 1; index <= 50; index += 1) {
    nodes.push({
      id: `requirement:${index}`,
      kind: "requirement",
      title: `需求 ${index}`,
      status: "clarified",
      caseCount: 1,
    })
    nodes.push({
      id: `case:${index}`,
      kind: "case",
      title: `用例 ${index}`,
      status: "reviewed",
    })
    nodes.push({
      id: `result:${index}`,
      kind: "result",
      title: `结果 ${index}`,
      status: index % 2 === 0 ? "passed" : "failed",
      caseNodeId: `case:${index}`,
      attempt: 1,
    })
    nodes.push({
      id: `issue:${index}`,
      kind: "issue",
      title: `问题 ${index}`,
      status: "open",
      severity: "major",
    })
    edges.push({
      id: `e-req-${index}`,
      source: `requirement:${index}`,
      target: `case:${index}`,
      kind: "requirement_case",
    })
    edges.push({
      id: `e-result-${index}`,
      source: `case:${index}`,
      target: `result:${index}`,
      kind: "case_result",
    })
    edges.push({
      id: `e-issue-${index}`,
      source: `result:${index}`,
      target: `issue:${index}`,
      kind: "result_issue",
    })
  }
  return makeSnapshot(nodes, edges)
}

function renderCanvas(overrides: Partial<ChainCanvasProps> = {}) {
  return render(<ChainCanvas snapshot={SMALL_SNAPSHOT} {...overrides} />)
}

/** Invokes the canvas' `onMoveStart` as React Flow would for one viewport move. */
function moveStart(event: Event | null): void {
  act(() => {
    const onMoveStart = flowProbe.props?.onMoveStart as
      | ((event: Event | null, viewport: unknown) => void)
      | undefined
    onMoveStart?.(event, { x: 0, y: 0, zoom: 1 })
  })
}

/** Centre of one laid-out node, matching what the follow controller targets. */
function nodeCentre(
  nodes: ChainSnapshotNode[],
  edges: ChainSnapshotEdge[],
  id: string,
): { x: number; y: number } {
  const node = layoutChain(nodes, { edges }).nodes.find((candidate) => candidate.id === id)
  expect(node).toBeDefined()
  return {
    x: (node?.x ?? 0) + (node?.width ?? 0) / 2,
    y: (node?.y ?? 0) + (node?.height ?? 0) / 2,
  }
}

function edgeGroup(container: HTMLElement, kind: string, edgeId: string): Element | null {
  return container.querySelector(`[data-testid="chain-edge-${kind}"][data-edge-id="${edgeId}"]`)
}

function edgePath(container: HTMLElement, kind: string, edgeId: string): SVGPathElement {
  const path = edgeGroup(container, kind, edgeId)?.querySelector("path.react-flow__edge-path")
  expect(path).not.toBeNull()
  return path as SVGPathElement
}

function edgeLabel(container: HTMLElement, kind: string, edgeId: string): string {
  const label = container.querySelector(
    `[data-testid="chain-edge-${kind}-label"][data-edge-id="${edgeId}"]`,
  )
  expect(label).not.toBeNull()
  return label?.textContent ?? ""
}

function nodeWrapper(container: HTMLElement, nodeId: string): HTMLElement | null {
  return container.querySelector<HTMLElement>(`.react-flow__node[data-id="${nodeId}"]`)
}

function mockReducedMotion(reduce: boolean): void {
  vi.spyOn(window, "matchMedia").mockImplementation(
    (query: string) =>
      ({
        matches: reduce && query.includes("prefers-reduced-motion"),
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(() => false),
      }) as unknown as MediaQueryList,
  )
}

beforeEach(() => {
  flowProbe.props = null
  flowProbe.setCenter.mockClear()
})

afterEach(() => {
  cleanup()
})

describe("ChainCanvas", () => {
  it("lazy-loads through next/dynamic ssr:false and renders the canvas shell", async () => {
    const { container } = renderCanvas()

    const canvas = await screen.findByTestId("chain-canvas")
    expect(canvas).not.toBeNull()
    expect(container.querySelector(".react-flow__background")).not.toBeNull()
    expect(container.querySelector(".react-flow__controls")).not.toBeNull()
    expect(screen.getByTestId("chain-minimap").querySelector(".react-flow__minimap")).not.toBeNull()
    expect(screen.getByTestId("chain-node-requirement")).not.toBeNull()
    expect(screen.getAllByTestId("chain-node-case")).toHaveLength(2)
    expect(screen.getAllByTestId("chain-node-result")).toHaveLength(3)
    expect(screen.getAllByTestId("chain-node-issue")).toHaveLength(2)
  })

  it("configures React Flow for the read-only, visible-only chain canvas", async () => {
    renderCanvas()
    await screen.findByTestId("chain-node-requirement")

    expect(flowProbe.props).not.toBeNull()
    expect(flowProbe.props?.onlyRenderVisibleElements).toBe(true)
    expect(flowProbe.props?.fitView).toBe(true)
    expect(flowProbe.props?.nodesDraggable).toBe(false)
    expect(flowProbe.props?.nodesConnectable).toBe(false)
    expect(flowProbe.props?.elementsSelectable).toBe(true)
    expect(flowProbe.props?.disableKeyboardA11y).toBe(true)
    expect(flowProbe.props?.nodesFocusable).toBe(false)
    expect(flowProbe.props?.edgesFocusable).toBe(false)
  })

  it("hides React Flow's interactive lock so the read-only canvas cannot be unlocked", async () => {
    const { container } = renderCanvas()
    await screen.findByTestId("chain-node-requirement")

    // Controls' interactive button toggles nodesDraggable/nodesConnectable/
    // elementsSelectable back on; the chain must never expose it.
    expect(container.querySelector(".react-flow__controls-interactive")).toBeNull()
    // Zoom and fit-to-view stay available.
    expect(container.querySelector(".react-flow__controls-zoomin")).not.toBeNull()
    expect(container.querySelector(".react-flow__controls-zoomout")).not.toBeNull()
    expect(container.querySelector(".react-flow__controls-fitview")).not.toBeNull()
  })

  it("fills the parent height and clamps zoom-out to the spec minimum", async () => {
    renderCanvas()
    const canvas = await screen.findByTestId("chain-canvas")

    // The parent owns the 72vh/60vh height; the canvas itself must not pin a
    // fixed 600px height that would fight it.
    expect(canvas.className).toContain("h-full")
    expect(canvas.className).toContain("w-full")
    expect(canvas.className).toContain("min-h-[420px]")
    expect(canvas.className).not.toContain("h-[600px]")
    expect(flowProbe.props?.minZoom).toBe(0.1)
  })

  it("renders 200 nodes and culls the ones outside the viewport", async () => {
    const { container } = render(<ChainCanvas snapshot={largeSnapshot()} />)

    await waitFor(() => expect(flowProbe.props).not.toBeNull())
    expect((flowProbe.props?.nodes as unknown[] | undefined)?.length).toBe(200)
    expect(flowProbe.props?.onlyRenderVisibleElements).toBe(true)

    // jsdom has no layout engine, so the test proves culling through the
    // rendered wrappers: the 800x600 fit viewport shows a slice of the graph,
    // which is 8,800 layout units tall (50 requirement groups x 176 units).
    // The mocked ReactFlow props above are the authoritative assertion for the
    // culling contract; the DOM assertions pin a known-visible and a
    // known-culled node instead of only asserting "fewer than all".
    const renderedNodes = (): Element[] => [...container.querySelectorAll(".react-flow__node")]
    await waitFor(
      () => {
        expect(renderedNodes().length).toBeGreaterThan(0)
        expect(renderedNodes().length).toBeLessThan(200)
      },
      { timeout: 5000 },
    )
    expect(nodeWrapper(container, "requirement:25")).not.toBeNull()
    expect(nodeWrapper(container, "requirement:50")).toBeNull()
  })

  it("draws semantic edges with labels, markers and status-driven colours", async () => {
    const { container } = renderCanvas()
    await screen.findAllByTestId("chain-node-result")

    // Inline style beats `.react-flow__edge-path { stroke: var(--xy-edge-stroke) }`
    // from `@xyflow/react/dist/style.css`; presentation attributes would lose.
    const requirementEdge = edgePath(container, "requirement_case", "e-req-case")
    expect(requirementEdge.style.stroke).toBe(CHAIN_COLOR_STYLES.indigo.stroke)
    expect(requirementEdge.style.strokeWidth).toBe("1.5")
    expect(requirementEdge.getAttribute("stroke")).toBeNull()
    expect(requirementEdge.getAttribute("marker-end")).toMatch(/^url\(/)
    expect(container.querySelector("marker.react-flow__arrowhead")).not.toBeNull()
    expect(edgeLabel(container, "requirement_case", "e-req-case")).toBe(
      EDGE_KIND_TOKENS.requirement_case.label,
    )

    expect(edgePath(container, "case_result", "e-passed").style.stroke).toBe(
      CHAIN_COLOR_STYLES.emerald.stroke,
    )
    expect(edgePath(container, "case_result", "e-running").style.stroke).toBe(
      CHAIN_COLOR_STYLES.blue.stroke,
    )
    expect(edgePath(container, "case_result", "e-failed").style.stroke).toBe(
      CHAIN_COLOR_STYLES.red.stroke,
    )
    expect(edgePath(container, "result_issue", "e-result-issue").style.stroke).toBe(
      EDGE_KIND_TOKENS.result_issue.stroke,
    )
    expect(edgePath(container, "case_issue", "e-case-issue").style.stroke).toBe(
      EDGE_KIND_TOKENS.case_issue.stroke,
    )
    // `case_issue` and `result_issue` deliberately use different tokens.
    expect(EDGE_KIND_TOKENS.case_issue.stroke).toBe(CHAIN_COLOR_STYLES.violet.stroke)
    expect(EDGE_KIND_TOKENS.result_issue.stroke).toBe(CHAIN_COLOR_STYLES.red.stroke)

    const impactEdge = edgePath(container, "impact_case", "e-impact")
    expect(impactEdge.style.stroke).toBe(CHAIN_COLOR_STYLES.amber.stroke)
    expect(impactEdge.style.strokeDasharray).toBe("6 6")
    expect(edgeLabel(container, "impact_case", "e-impact")).toBe(EDGE_KIND_TOKENS.impact_case.label)
  })

  it("falls back to the teal case_result token for unknown or empty result statuses", async () => {
    const nodes: ChainSnapshotNode[] = [
      ...SMALL_NODES,
      {
        id: "result:4",
        kind: "result",
        title: "未知状态",
        status: "mystery",
        caseNodeId: "case:1",
        attempt: 4,
      },
      {
        id: "result:5",
        kind: "result",
        title: "空状态",
        status: "",
        caseNodeId: "case:1",
        attempt: 5,
      },
    ]
    const edges: ChainSnapshotEdge[] = [
      ...SMALL_EDGES,
      { id: "e-unknown-status", source: "case:1", target: "result:4", kind: "case_result" },
      { id: "e-empty-status", source: "case:1", target: "result:5", kind: "case_result" },
    ]

    const { container } = render(<ChainCanvas snapshot={makeSnapshot(nodes, edges)} />)
    await screen.findAllByTestId("chain-node-result")

    expect(edgePath(container, "case_result", "e-unknown-status").style.stroke).toBe(
      EDGE_KIND_TOKENS.case_result.stroke,
    )
    expect(edgePath(container, "case_result", "e-empty-status").style.stroke).toBe(
      EDGE_KIND_TOKENS.case_result.stroke,
    )
  })

  it("colours MiniMap nodes by their chain kind token", async () => {
    renderCanvas()
    await screen.findAllByTestId("chain-node-result")

    const rects = [
      ...screen.getByTestId("chain-minimap").querySelectorAll("rect.react-flow__minimap-node"),
    ]
    const fills = new Set(rects.map((rect) => (rect as SVGElement).style.fill))
    expect(fills).toContain(CHAIN_COLOR_STYLES.blue.stroke)
    expect(fills).toContain(CHAIN_COLOR_STYLES.indigo.stroke)
    expect(fills).toContain(CHAIN_COLOR_STYLES.teal.stroke)
    expect(fills).toContain(CHAIN_COLOR_STYLES.red.stroke)
  })

  it("animates only edges that target a running result", async () => {
    renderCanvas()

    const runningEdge = await screen.findByTestId("rf__edge-e-running")
    expect(runningEdge.getAttribute("class")).toContain("animated")
    expect(screen.getByTestId("rf__edge-e-passed").getAttribute("class")).not.toContain("animated")
    expect(screen.getByTestId("rf__edge-e-impact").getAttribute("class")).not.toContain("animated")
  })

  it("calls onSelect with the backend node id when a node is clicked", async () => {
    const onSelect = vi.fn()
    renderCanvas({ onSelect })

    fireEvent.click(await screen.findByTestId("chain-node-requirement"))

    expect(onSelect).toHaveBeenCalledWith("requirement:1")
  })

  it("passes selectedId to the matching card only", async () => {
    renderCanvas({ selectedId: "case:1" })

    const cards = await screen.findAllByTestId("chain-node-case")
    const selected = cards.find((card) => card.getAttribute("data-node-id") === "case:1")
    const other = cards.find((card) => card.getAttribute("data-node-id") === "case:2")
    expect(selected?.getAttribute("data-selected")).toBe("true")
    expect(other?.getAttribute("data-selected")).toBeNull()
  })

  it("hides collapsed requirement groups and toggles on double click", async () => {
    const onToggleCollapse = vi.fn()
    renderCanvas({ collapsed: ["requirement:1"], onToggleCollapse })

    fireEvent.doubleClick(await screen.findByTestId("chain-node-requirement"))

    expect(screen.queryByTestId("chain-node-case")).toBeNull()
    expect(onToggleCollapse).toHaveBeenCalledWith("requirement:1")
  })

  it("pulses running result nodes", async () => {
    const { container } = renderCanvas()
    await screen.findAllByTestId("chain-node-result")

    const running = nodeWrapper(container, "result:2")
    expect(running).not.toBeNull()
    expect(running?.className).toContain("chain-node-running")
    expect(running?.className).toContain("animate-pulse")

    const passed = nodeWrapper(container, "result:1")
    expect(passed?.className).not.toContain("animate-pulse")
  })

  it("turns off node pulse and edge animation under prefers-reduced-motion", async () => {
    mockReducedMotion(true)
    const { container } = renderCanvas()
    await screen.findAllByTestId("chain-node-result")

    const running = nodeWrapper(container, "result:2")
    expect(running?.className).not.toContain("animate-pulse")
    expect(running?.className).not.toContain("animate-")

    const runningEdge = await screen.findByTestId("rf__edge-e-running")
    expect(runningEdge.getAttribute("class")).not.toContain("animated")
  })

  it("centres the running result once and pauses follow after a user pan", async () => {
    const centre = nodeCentre(SMALL_NODES, SMALL_EDGES, "result:2")

    const { rerender } = renderCanvas({ follow: true })

    await waitFor(() => expect(flowProbe.setCenter).toHaveBeenCalledTimes(1))
    expect(flowProbe.setCenter).toHaveBeenCalledWith(centre.x, centre.y, {
      zoom: 1.1,
      duration: 450,
    })
    expect(screen.getByTestId("chain-follow-status").getAttribute("data-follow-paused")).toBe(
      "false",
    )

    // Programmatic moves carry a null event and must not pause the follow.
    moveStart(null)
    expect(screen.getByTestId("chain-follow-status").getAttribute("data-follow-paused")).toBe(
      "false",
    )

    // A user pan pauses the follow; a later snapshot refresh must not re-fit.
    moveStart(new MouseEvent("mousedown"))
    await waitFor(() =>
      expect(screen.getByTestId("chain-follow-status").getAttribute("data-follow-paused")).toBe(
        "true",
      ),
    )

    const callsAfterPause = flowProbe.setCenter.mock.calls.length
    rerender(<ChainCanvas snapshot={refreshed(SMALL_SNAPSHOT)} follow />)
    await act(async () => {})
    expect(flowProbe.setCenter).toHaveBeenCalledTimes(callsAfterPause)
  })

  it("does not re-centre when an unpaused snapshot refreshes", async () => {
    const centre = nodeCentre(SMALL_NODES, SMALL_EDGES, "result:2")

    const { rerender } = renderCanvas({ follow: true })
    await waitFor(() => expect(flowProbe.setCenter).toHaveBeenCalledTimes(1))

    // The follow target keeps the same id across refreshes, so the controller
    // must remember it was already focused even while nobody panned.
    rerender(<ChainCanvas snapshot={refreshed(SMALL_SNAPSHOT)} follow />)
    await act(async () => {})

    expect(flowProbe.setCenter).toHaveBeenCalledTimes(1)
    expect(flowProbe.setCenter).toHaveBeenCalledWith(centre.x, centre.y, {
      zoom: 1.1,
      duration: 450,
    })
  })

  it("jumps instantly when reduced motion is preferred", async () => {
    mockReducedMotion(true)
    const centre = nodeCentre(SMALL_NODES, SMALL_EDGES, "result:2")

    renderCanvas({ follow: true })

    await waitFor(() => expect(flowProbe.setCenter).toHaveBeenCalledTimes(1))
    expect(flowProbe.setCenter).toHaveBeenCalledWith(centre.x, centre.y, {
      zoom: 1.1,
      duration: 0,
    })
  })

  it("resumes following and re-centres from the paused state", async () => {
    renderCanvas({ follow: true })
    await waitFor(() => expect(flowProbe.setCenter).toHaveBeenCalledTimes(1))

    moveStart(new MouseEvent("mousedown"))
    await waitFor(() =>
      expect(screen.getByTestId("chain-follow-status").getAttribute("data-follow-paused")).toBe(
        "true",
      ),
    )

    fireEvent.click(screen.getByTestId("chain-follow-resume"))

    await waitFor(() => expect(flowProbe.setCenter).toHaveBeenCalledTimes(2))
    expect(screen.getByTestId("chain-follow-status").getAttribute("data-follow-paused")).toBe(
      "false",
    )
  })

  it("follows the first running result in layout order", async () => {
    const nodes: ChainSnapshotNode[] = [
      {
        id: "requirement:1",
        kind: "requirement",
        title: "登录需求",
        status: "clarified",
        priority: "must",
        caseCount: 2,
      },
      {
        id: "case:1",
        kind: "case",
        title: "登录成功",
        status: "reviewed",
        requirementIds: ["requirement:1"],
      },
      {
        id: "result:1",
        kind: "result",
        title: "第一次执行",
        status: "running",
        caseNodeId: "case:1",
        attempt: 1,
      },
      {
        id: "result:2",
        kind: "result",
        title: "第二次执行",
        status: "running",
        caseNodeId: "case:1",
        attempt: 2,
      },
    ]
    const edges: ChainSnapshotEdge[] = [
      { id: "e-req", source: "requirement:1", target: "case:1", kind: "requirement_case" },
      { id: "e-r1", source: "case:1", target: "result:1", kind: "case_result" },
      { id: "e-r2", source: "case:1", target: "result:2", kind: "case_result" },
    ]
    const first = nodeCentre(nodes, edges, "result:1")

    render(<ChainCanvas snapshot={makeSnapshot(nodes, edges)} follow />)

    await waitFor(() => expect(flowProbe.setCenter).toHaveBeenCalledTimes(1))
    expect(flowProbe.setCenter).toHaveBeenCalledWith(first.x, first.y, {
      zoom: 1.1,
      duration: 450,
    })
  })

  it("exposes the chain through the visually hidden keyboard tree", async () => {
    const onSelect = vi.fn()
    const onToggleCollapse = vi.fn()
    const onEscape = vi.fn()
    renderCanvas({ onSelect, onToggleCollapse, onEscape })

    await screen.findByTestId("chain-node-requirement")
    expect(screen.getByRole("tree")).not.toBeNull()

    const requirement = screen.getByTestId("chain-treeitem-requirement:1")
    expect(requirement.getAttribute("role")).toBe("treeitem")
    expect(requirement.getAttribute("aria-level")).toBe("1")
    expect(requirement.getAttribute("aria-expanded")).toBe("true")
    expect(requirement.getAttribute("aria-setsize")).toBe("1")
    expect(requirement.getAttribute("tabindex")).toBe("0")

    requirement.focus()
    fireEvent.keyDown(requirement, { key: "ArrowDown" })
    const firstCase = screen.getByTestId("chain-treeitem-case:1")
    expect(document.activeElement).toBe(firstCase)
    expect(firstCase.getAttribute("tabindex")).toBe("0")
    expect(requirement.getAttribute("tabindex")).toBe("-1")

    fireEvent.keyDown(firstCase, { key: "Enter" })
    expect(onSelect).toHaveBeenCalledWith("case:1")

    fireEvent.keyDown(firstCase, { key: "Escape" })
    expect(onEscape).toHaveBeenCalledTimes(1)

    // Left goes back to the parent, then collapses the expanded requirement.
    fireEvent.keyDown(firstCase, { key: "ArrowLeft" })
    expect(document.activeElement).toBe(requirement)
    fireEvent.keyDown(requirement, { key: "ArrowLeft" })
    expect(onToggleCollapse).toHaveBeenCalledWith("requirement:1")
  })

  it("syncs aria-selected and the roving tab stop to selectedId", async () => {
    renderCanvas({ selectedId: "case:1" })

    await screen.findAllByTestId("chain-node-case")
    const selected = screen.getByTestId("chain-treeitem-case:1")
    expect(selected.getAttribute("aria-selected")).toBe("true")
    expect(selected.getAttribute("tabindex")).toBe("0")
    expect(screen.getByTestId("chain-treeitem-requirement:1").getAttribute("aria-selected")).toBe(
      "false",
    )
  })

  it("localises React Flow edge labels instead of leaving the English default", async () => {
    renderCanvas()
    await screen.findAllByTestId("chain-node-result")

    const edge = screen.getByTestId("rf__edge-e-req-case")
    const label = edge.getAttribute("aria-label") ?? ""
    expect(label).toContain(EDGE_KIND_TOKENS.requirement_case.label)
    expect(label).toContain("登录需求")
    expect(label).toContain("登录成功")
    expect(label).not.toContain("Edge from")
  })
})

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

  it("renders 200 nodes and culls the ones outside the viewport", async () => {
    const { container } = render(<ChainCanvas snapshot={largeSnapshot()} />)

    await waitFor(() => expect(flowProbe.props).not.toBeNull())
    expect((flowProbe.props?.nodes as unknown[] | undefined)?.length).toBe(200)
    expect(flowProbe.props?.onlyRenderVisibleElements).toBe(true)

    // jsdom has no layout engine, so the test proves culling through the
    // rendered wrappers: the fit viewport is 800x600 while the graph is tens of
    // thousands of layout units tall. The mocked ReactFlow props above are the
    // authoritative assertion for the culling contract.
    await waitFor(
      () => {
        expect(container.querySelectorAll(".react-flow__node").length).toBeLessThan(200)
      },
      { timeout: 5000 },
    )
  })

  it("draws semantic edges with labels, markers and status-driven colours", async () => {
    const { container } = renderCanvas()
    await screen.findAllByTestId("chain-node-result")

    const requirementEdge = edgePath(container, "requirement_case", "e-req-case")
    expect(requirementEdge.getAttribute("stroke")).toBe(CHAIN_COLOR_STYLES.indigo.stroke)
    expect(requirementEdge.getAttribute("marker-end")).toMatch(/^url\(/)
    expect(container.querySelector("marker.react-flow__arrowhead")).not.toBeNull()
    expect(edgeLabel(container, "requirement_case", "e-req-case")).toBe(
      EDGE_KIND_TOKENS.requirement_case.label,
    )

    expect(edgePath(container, "case_result", "e-passed").getAttribute("stroke")).toBe(
      CHAIN_COLOR_STYLES.emerald.stroke,
    )
    expect(edgePath(container, "case_result", "e-running").getAttribute("stroke")).toBe(
      CHAIN_COLOR_STYLES.blue.stroke,
    )
    expect(edgePath(container, "case_result", "e-failed").getAttribute("stroke")).toBe(
      CHAIN_COLOR_STYLES.red.stroke,
    )
    expect(edgePath(container, "result_issue", "e-result-issue").getAttribute("stroke")).toBe(
      CHAIN_COLOR_STYLES.red.stroke,
    )
    expect(edgePath(container, "case_issue", "e-case-issue").getAttribute("stroke")).toBe(
      CHAIN_COLOR_STYLES.red.stroke,
    )

    const impactEdge = edgePath(container, "impact_case", "e-impact")
    expect(impactEdge.getAttribute("stroke")).toBe(CHAIN_COLOR_STYLES.amber.stroke)
    expect(impactEdge.getAttribute("stroke-dasharray")).toBe("6 6")
    expect(edgeLabel(container, "impact_case", "e-impact")).toBe(EDGE_KIND_TOKENS.impact_case.label)
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
    const running = layoutChain(SMALL_SNAPSHOT.nodes, {
      edges: SMALL_SNAPSHOT.edges,
    }).nodes.find((node) => node.id === "result:2")
    expect(running).toBeDefined()

    const { rerender } = renderCanvas({ follow: true })

    await waitFor(() => expect(flowProbe.setCenter).toHaveBeenCalledTimes(1))
    expect(flowProbe.setCenter).toHaveBeenCalledWith(
      (running?.x ?? 0) + (running?.width ?? 0) / 2,
      (running?.y ?? 0) + (running?.height ?? 0) / 2,
      { zoom: 1.1, duration: 450 },
    )
    expect(screen.getByTestId("chain-follow-status").getAttribute("data-follow-paused")).toBe(
      "false",
    )

    // Programmatic moves carry a null event and must not pause the follow.
    act(() => {
      const onMoveStart = flowProbe.props?.onMoveStart as
        | ((event: Event | null, viewport: unknown) => void)
        | undefined
      onMoveStart?.(null, { x: 0, y: 0, zoom: 1 })
    })
    expect(screen.getByTestId("chain-follow-status").getAttribute("data-follow-paused")).toBe(
      "false",
    )

    // A user pan pauses the follow; a later snapshot refresh must not re-fit.
    act(() => {
      const onMoveStart = flowProbe.props?.onMoveStart as
        | ((event: Event | null, viewport: unknown) => void)
        | undefined
      onMoveStart?.(new MouseEvent("mousedown"), { x: 0, y: 0, zoom: 1 })
    })
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
})

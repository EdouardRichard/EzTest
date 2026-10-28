import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { createRef } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { MAX_ZOOM, MIN_ZOOM, MindmapCanvas } from "./MindmapCanvas"
import type { CaseNodeView, MindmapGraph } from "./types"

const VERSION_ID = "1844674407370955161"

const S1 = "1844674407370955101"
const S2 = "1844674407370955102"
const C1 = "1844674407370955103"
const C2 = "1844674407370955104"
const C3 = "1844674407370955105"

function caseNode(
  overrides: Partial<CaseNodeView> & Pick<CaseNodeView, "id" | "title">,
): CaseNodeView {
  return {
    parentId: null,
    kind: "case",
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

/**
 * s1 (scenario)
 * ├── s2 (scenario)
 * │   ├── c2 (case, AI generated, reviewed, no blocker)
 * │   └── c3 (case)
 * └── c1 (case, P1, 2 blockers, impacted by P4 code analysis)
 */
function fixtureNodes(): CaseNodeView[] {
  return [
    caseNode({ id: S1, title: "订单场景", kind: "scenario", sortOrder: 0 }),
    caseNode({
      id: S2,
      title: "创建订单",
      kind: "scenario",
      parentId: S1,
      sortOrder: 0,
    }),
    caseNode({
      id: C1,
      title: "取消订单",
      parentId: S1,
      sortOrder: 1,
      priority: "P1",
      source: "user_input",
      status: "draft",
      openBlockerCount: 2,
    }),
    caseNode({
      id: C2,
      title: "成功创建",
      parentId: S2,
      sortOrder: 0,
      source: "ai_generated",
      status: "reviewed",
    }),
    caseNode({ id: C3, title: "参数校验", parentId: S2, sortOrder: 1 }),
  ]
}

function fixtureGraph(nodes: CaseNodeView[] = fixtureNodes()): MindmapGraph {
  return {
    versionId: VERSION_ID,
    versionStatus: "designing",
    editable: true,
    nodes,
    coverage: {
      requirementIds: [],
      uncoveredRequirementIds: [],
      staleRequirementIds: [],
      matrix: [],
    },
    baseline: null,
    latestRuns: {},
    impactCases: [
      {
        caseNodeId: C1,
        reason: "取消逻辑被 code_intel 引用",
        confidence: "medium",
        source: "code_intel",
      },
    ],
  }
}

function renderCanvas(
  overrides: {
    graph?: MindmapGraph
    selectedId?: string | null
    collapsed?: Set<string>
    onSelect?: (id: string) => void
    onToggle?: (id: string) => void
  } = {},
) {
  const onSelect = overrides.onSelect ?? vi.fn()
  const onToggle = overrides.onToggle ?? vi.fn()
  const result = render(
    <MindmapCanvas
      graph={overrides.graph ?? fixtureGraph()}
      selectedId={overrides.selectedId ?? null}
      onSelect={onSelect}
      collapsed={overrides.collapsed ?? new Set<string>()}
      onToggle={onToggle}
    />,
  )
  return { ...result, onSelect, onToggle }
}

function zoomOf(): number {
  return Number(screen.getByTestId("mindmap-canvas").getAttribute("data-zoom"))
}

function activeTestId(): string | null {
  const active = screen
    .getAllByRole("treeitem")
    .find((item) => item.getAttribute("tabindex") === "0")
  return active?.getAttribute("data-testid") ?? null
}

describe("MindmapCanvas", () => {
  afterEach(cleanup)

  it("renders scenario and case cards with their metadata", () => {
    renderCanvas({ selectedId: C1 })

    expect(screen.getByTestId("mindmap-canvas")).toBeTruthy()
    expect(screen.getByRole("tree")).toBeTruthy()

    const root = screen.getByTestId(`scenario-node-${S1}`)
    expect(root.getAttribute("role")).toBe("treeitem")
    expect(root.getAttribute("aria-level")).toBe("1")
    expect(root.textContent).toContain("订单场景")

    const scenarioChild = screen.getByTestId(`scenario-node-${S2}`)
    expect(scenarioChild.getAttribute("aria-level")).toBe("2")
    expect(scenarioChild.textContent).toContain("场景")

    const impacted = screen.getByTestId(`case-node-${C1}`)
    expect(impacted.getAttribute("aria-level")).toBe("2")
    expect(impacted.getAttribute("aria-selected")).toBe("true")
    expect(impacted.textContent).toContain("取消订单")
    expect(impacted.textContent).toContain("P1")
    expect(impacted.textContent).toContain("草稿")
    expect(impacted.textContent).toContain("人工")
    expect(impacted.textContent).toContain("blocker ×2")
    expect(impacted.textContent).toContain("可能受影响")

    const unaffected = screen.getByTestId(`case-node-${C2}`)
    expect(unaffected.getAttribute("aria-selected")).toBe("false")
    expect(unaffected.textContent).toContain("AI")
    expect(unaffected.textContent).toContain("已评审")
    expect(unaffected.textContent).toContain("未受影响")
    expect(unaffected.textContent).not.toContain("blocker")

    expect(screen.getByTestId(`case-node-${C3}`)).toBeTruthy()
  })

  it("marks only nodes with children as expandable", () => {
    renderCanvas()

    expect(screen.getByTestId(`scenario-node-${S1}`).getAttribute("aria-expanded")).toBe("true")
    expect(screen.getByTestId(`scenario-node-${S2}`).getAttribute("aria-expanded")).toBe("true")
    expect(screen.getByTestId(`case-node-${C1}`).getAttribute("aria-expanded")).toBeNull()
  })

  it("hides the descendants of a collapsed node and shows the hidden count", () => {
    renderCanvas({ collapsed: new Set([S2]) })

    expect(screen.getByTestId(`scenario-node-${S2}`).getAttribute("aria-expanded")).toBe("false")
    expect(screen.getByTestId(`scenario-node-${S2}`).textContent).toContain("已折叠 2")
    expect(screen.queryByTestId(`case-node-${C2}`)).toBeNull()
    expect(screen.queryByTestId(`case-node-${C3}`)).toBeNull()
    // Sibling branches stay visible.
    expect(screen.getByTestId(`case-node-${C1}`)).toBeTruthy()
  })

  it("selects on click and toggles collapse from the expand button", () => {
    const { onSelect, onToggle } = renderCanvas()

    fireEvent.click(screen.getByTestId(`case-node-${C1}`))
    expect(onSelect).toHaveBeenLastCalledWith(C1)

    fireEvent.click(screen.getByTestId(`mindmap-toggle-${S2}`))
    expect(onToggle).toHaveBeenLastCalledWith(S2)
    expect(onSelect).toHaveBeenCalledTimes(1)

    expect(screen.queryByTestId(`mindmap-toggle-${C1}`)).toBeNull()
  })

  it("moves the active node with the arrow keys and selects it with Enter", () => {
    const { onSelect } = renderCanvas()

    expect(activeTestId()).toBe(`scenario-node-${S1}`)

    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S1}`), { key: "ArrowDown" })
    expect(activeTestId()).toBe(`scenario-node-${S2}`)

    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S2}`), { key: "ArrowUp" })
    expect(activeTestId()).toBe(`scenario-node-${S1}`)

    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S1}`), { key: "ArrowRight" })
    expect(activeTestId()).toBe(`scenario-node-${S2}`)

    // Left on a child card moves up to its parent (Left on an expanded parent collapses it).
    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S2}`), { key: "ArrowDown" })
    expect(activeTestId()).toBe(`case-node-${C2}`)
    fireEvent.keyDown(screen.getByTestId(`case-node-${C2}`), { key: "ArrowLeft" })
    expect(activeTestId()).toBe(`scenario-node-${S2}`)

    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S2}`), { key: "Enter" })
    expect(onSelect).toHaveBeenLastCalledWith(S2)
  })

  it("expands a collapsed node and collapses an expanded node from the keyboard", () => {
    const onToggle = vi.fn()
    const { rerender } = render(
      <MindmapCanvas
        graph={fixtureGraph()}
        selectedId={null}
        onSelect={vi.fn()}
        collapsed={new Set([S2])}
        onToggle={onToggle}
      />,
    )

    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S1}`), { key: "ArrowDown" })
    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S2}`), { key: "ArrowRight" })
    expect(onToggle).toHaveBeenLastCalledWith(S2)

    rerender(
      <MindmapCanvas
        graph={fixtureGraph()}
        selectedId={null}
        onSelect={vi.fn()}
        collapsed={new Set<string>()}
        onToggle={onToggle}
      />,
    )
    fireEvent.keyDown(screen.getByTestId(`scenario-node-${S2}`), { key: "ArrowLeft" })
    expect(onToggle).toHaveBeenLastCalledWith(S2)
  })

  it("clamps button zoom to the 0.5-2 range and resets on fit", () => {
    renderCanvas()

    expect(zoomOf()).toBe(1)
    const zoomIn = screen.getByTestId("mindmap-zoom-in")
    for (let index = 0; index < 8; index += 1) fireEvent.click(zoomIn)
    expect(zoomOf()).toBe(MAX_ZOOM)

    const zoomOut = screen.getByTestId("mindmap-zoom-out")
    for (let index = 0; index < 20; index += 1) fireEvent.click(zoomOut)
    expect(zoomOf()).toBe(MIN_ZOOM)

    fireEvent.click(screen.getByTestId("mindmap-fit"))
    expect(zoomOf()).toBe(1)
  })

  it("clamps wheel zoom to the same range", () => {
    renderCanvas()
    const viewport = screen.getByTestId("mindmap-viewport")

    for (let index = 0; index < 12; index += 1) {
      fireEvent.wheel(viewport, { deltaY: -120 })
    }
    expect(zoomOf()).toBe(MAX_ZOOM)

    for (let index = 0; index < 24; index += 1) {
      fireEvent.wheel(viewport, { deltaY: 120 })
    }
    expect(zoomOf()).toBe(MIN_ZOOM)
  })

  it("pans the canvas by pointer drag", () => {
    renderCanvas()
    const viewport = screen.getByTestId("mindmap-viewport")
    const group = screen.getByRole("tree").querySelector("g")
    expect(group).not.toBeNull()
    const before = group?.getAttribute("transform")

    fireEvent.pointerDown(viewport, { clientX: 100, clientY: 100, button: 0, pointerType: "mouse" })
    fireEvent.pointerMove(viewport, { clientX: 160, clientY: 140 })

    const after = screen.getByRole("tree").querySelector("g")?.getAttribute("transform")
    expect(after).not.toBe(before)
    expect(after).toContain("translate(60 40)")
    fireEvent.pointerUp(viewport)
  })

  it("forwards the canvas <svg> through svgRef for the PNG export", () => {
    const svgRef = createRef<SVGSVGElement>()
    render(
      <MindmapCanvas graph={fixtureGraph()} selectedId={null} onSelect={vi.fn()} svgRef={svgRef} />,
    )

    expect(svgRef.current).toBe(screen.getByRole("tree"))
  })

  it("shows an empty hint instead of an empty tree", () => {
    renderCanvas({ graph: fixtureGraph([]) })

    expect(screen.getByText("暂无脑图节点，先生成或新建用例。")).toBeTruthy()
    expect(screen.queryByRole("tree")).toBeNull()
  })
})

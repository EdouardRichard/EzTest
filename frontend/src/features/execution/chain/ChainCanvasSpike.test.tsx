import { cleanup, render, screen } from "@testing-library/react"
import type { Edge, Node } from "@xyflow/react"
import dynamic from "next/dynamic"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

/**
 * P8 Task 5 spike: prove `@xyflow/react` can be lazy-loaded with
 * `next/dynamic(..., { ssr: false })` and rendered under Vitest/jsdom.
 *
 * `next/dynamic` is deliberately declared at module scope, exactly the way a
 * product route would use it, so the test exercises the real lazy boundary.
 */
const ChainCanvasSpike = dynamic(() => import("./ChainCanvasSpike"), { ssr: false })

type SpikeNodeData = { label: string }
type SpikeFlowNode = Node<SpikeNodeData, "spike">

const TWO_NODES: SpikeFlowNode[] = [
  {
    id: "requirement-login",
    type: "spike",
    position: { x: 0, y: 0 },
    data: { label: "需求：登录" },
  },
  {
    id: "case-login-success",
    type: "spike",
    position: { x: 0, y: 140 },
    data: { label: "用例：登录成功" },
  },
]

const TWO_EDGES: Edge[] = [
  {
    id: "login-chain",
    source: "requirement-login",
    target: "case-login-success",
  },
]

function chainFixture(count: number): {
  nodes: SpikeFlowNode[]
  edges: Edge[]
} {
  const nodes = Array.from({ length: count }, (_, index) => ({
    id: `node-${index + 1}`,
    type: "spike" as const,
    position: { x: (index % 20) * 200, y: Math.floor(index / 20) * 100 },
    data: { label: `节点 ${index + 1}` },
  }))
  const edges = Array.from({ length: count - 1 }, (_, index) => ({
    id: `edge-${index + 1}`,
    source: `node-${index + 1}`,
    target: `node-${index + 2}`,
  }))
  return { nodes, edges }
}

/**
 * `@testing-library/jest-dom` is not wired into this project, so assert the
 * visibility React Flow itself controls on the node wrapper (it toggles
 * `visibility` between hidden and visible once a node has been measured).
 */
function expectNodeTitleVisible(label: string): void {
  const title = screen.getByText(label)
  expect(title.textContent).toBe(label)
  const wrapper = title.closest<HTMLElement>(".react-flow__node")
  expect(wrapper).not.toBeNull()
  if (wrapper !== null) {
    expect(window.getComputedStyle(wrapper).visibility).toBe("visible")
  }
}

describe("ChainCanvasSpike", () => {
  let consoleErrorSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    consoleErrorSpy = vi.spyOn(console, "error").mockImplementation(() => {})
  })

  afterEach(() => {
    consoleErrorSpy.mockRestore()
    cleanup()
  })

  it("lazy-loads and renders a two-node chain with visible titles", async () => {
    render(<ChainCanvasSpike nodes={TWO_NODES} edges={TWO_EDGES} />)

    await screen.findByText("需求：登录")
    await screen.findByText("用例：登录成功")

    expectNodeTitleVisible("需求：登录")
    expectNodeTitleVisible("用例：登录成功")
    expect(consoleErrorSpy).not.toHaveBeenCalled()
  })

  it("renders a 200-node chain within the 5s hang guard", async () => {
    const { nodes, edges } = chainFixture(200)

    const startedAt = performance.now()
    render(<ChainCanvasSpike nodes={nodes} edges={edges} />)
    await screen.findByText("节点 200", undefined, { timeout: 5000 })
    const elapsedMs = performance.now() - startedAt

    console.log(`[react-flow-spike] 200 nodes visible in ${elapsedMs.toFixed(1)} ms`)
    expectNodeTitleVisible("节点 200")
    expect(elapsedMs).toBeLessThan(5000)
    expect(consoleErrorSpy).not.toHaveBeenCalled()
  })
})

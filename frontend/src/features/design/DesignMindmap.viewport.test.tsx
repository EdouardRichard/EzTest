import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const reactFlow = vi.hoisted(() => ({
  fitView: vi.fn(),
  zoomIn: vi.fn(),
  zoomOut: vi.fn(),
  props: [] as Array<Record<string, unknown>>,
}))

// Keep the real provider/filters but capture the React Flow props and the
// imperative fitView calls: the viewport policy is what this file verifies.
vi.mock("@xyflow/react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@xyflow/react")>()
  const React = await import("react")
  return {
    ...actual,
    useReactFlow: () => reactFlow,
    ReactFlow: (props: Record<string, unknown>) => {
      reactFlow.props.push(props)
      return React.createElement("div", { "data-testid": "react-flow-stub" })
    },
  }
})

import { DesignMindmap } from "./DesignMindmap"
import { designGraphFixture } from "./test-fixtures"

function renderMindmap(
  overrides: Partial<Parameters<typeof DesignMindmap>[0]> = {},
): ReturnType<typeof render> {
  return render(
    <DesignMindmap
      graph={designGraphFixture}
      selectedCaseId={null}
      onSelectCase={vi.fn()}
      onConfirmCase={vi.fn()}
      onConfirmScenario={vi.fn()}
      onConfirmRequirement={vi.fn()}
      {...overrides}
    />,
  )
}

function lastCanvasProps(): Record<string, unknown> {
  const props = reactFlow.props.at(-1)
  if (props === undefined) throw new Error("React Flow was not rendered")
  return props
}

/**
 * A 10-case graph is ~2,500 world pixels tall. The inline canvas is a few
 * hundred pixels tall, so fitting the whole graph lands on React Flow's
 * 0.2 min zoom: the cards render 60 px wide and most of them sit outside the
 * canvas. The initial viewport therefore has to stay readable, and automatic
 * re-fits must not drop below a readable zoom either.
 */
describe("DesignMindmap readable viewport", () => {
  beforeEach(() => {
    reactFlow.fitView.mockReset()
    reactFlow.zoomIn.mockReset()
    reactFlow.zoomOut.mockReset()
    reactFlow.props.length = 0
  })

  afterEach(cleanup)

  it("opens at a readable zoom instead of fitting the tall graph to the minimum", () => {
    renderMindmap()

    const props = lastCanvasProps()
    const viewport = props.defaultViewport as { x: number; y: number; zoom: number } | undefined

    // No mount-time fitView (fitView=true would clamp to minZoom 0.2).
    expect(props.fitView).not.toBe(true)
    expect(viewport).toBeDefined()
    expect(viewport?.zoom).toBeGreaterThanOrEqual(0.6)
    expect(viewport?.x).toBeGreaterThanOrEqual(0)
    expect(viewport?.y).toBeGreaterThanOrEqual(0)
  })

  it("keeps automatic re-fits after the focus resize at a readable zoom", async () => {
    const view = renderMindmap()
    view.rerender(
      <DesignMindmap
        graph={designGraphFixture}
        selectedCaseId={null}
        onSelectCase={vi.fn()}
        onConfirmCase={vi.fn()}
        onConfirmScenario={vi.fn()}
        onConfirmRequirement={vi.fn()}
        fitViewToken={1}
      />,
    )

    await waitFor(() => expect(reactFlow.fitView).toHaveBeenCalled())
    const options = reactFlow.fitView.mock.calls.at(-1)?.[0] as { minZoom?: number }
    expect(options.minZoom).toBeGreaterThanOrEqual(0.6)
  })

  it("keeps search fits readable when the matches span a tall graph", async () => {
    renderMindmap()

    fireEvent.change(screen.getByTestId("design-search"), { target: { value: "退款" } })

    await waitFor(() => expect(reactFlow.fitView).toHaveBeenCalled())
    const options = reactFlow.fitView.mock.calls.at(-1)?.[0] as { minZoom?: number }
    expect(options.minZoom).toBeGreaterThanOrEqual(0.6)
  })

  it("still offers an explicit whole-graph fit", () => {
    renderMindmap()

    fireEvent.click(screen.getByTestId("design-fit-view"))

    expect(reactFlow.fitView).toHaveBeenCalledTimes(1)
    // The explicit control is the one place that may zoom out to the overview.
    expect(reactFlow.fitView.mock.calls[0]?.[0]).toMatchObject({ padding: 0.2 })
  })
})

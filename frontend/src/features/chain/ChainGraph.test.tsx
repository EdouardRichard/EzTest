import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import "@testing-library/jest-dom/vitest"

const reactFlow = vi.hoisted(() => ({
  setCenter: vi.fn(),
}))

vi.mock("@xyflow/react", async () => {
  const React = await import("react")
  return {
    ReactFlowProvider: ({ children }: { children: React.ReactNode }) =>
      React.createElement(React.Fragment, null, children),
    ReactFlow: ({
      children,
      onMoveStart,
    }: {
      children?: React.ReactNode
      onMoveStart?: (event: unknown, viewport: unknown) => void
    }) =>
      React.createElement(
        "div",
        { "data-testid": "mock-flow" },
        React.createElement(
          "button",
          { type: "button", onClick: () => onMoveStart?.({ type: "mousedown" }, {}) },
          "模拟用户平移",
        ),
        children,
      ),
    Background: () => null,
    BackgroundVariant: { Dots: "dots" },
    Controls: () => null,
    MiniMap: () => null,
    Handle: () => null,
    MarkerType: { ArrowClosed: "arrow" },
    Position: { Left: "left", Right: "right" },
    useReactFlow: () => ({ setCenter: reactFlow.setCenter }),
  }
})

import { ChainGraph } from "./ChainGraph"
import type { ChainNodeView } from "./types"

const issueNode: ChainNodeView = {
  id: "issue:9101",
  kind: "issue",
  ref_id: "9101",
  label: "退款金额不一致",
  status: "open",
  created_at: "2026-10-31T10:00:00Z",
  meta: { severity: "major", issue_status: "open", category: "api" },
}

describe("ChainGraph", () => {
  beforeEach(() => {
    reactFlow.setCenter.mockReset()
  })
  afterEach(() => {
    cleanup()
  })

  it("centers the followed node and exposes follow state", () => {
    render(
      <ChainGraph
        nodes={[issueNode]}
        edges={[]}
        selectedNodeId="issue:9101"
        onSelectNode={vi.fn()}
        follow
        followNodeId="issue:9101"
      />,
    )

    expect(screen.getByTestId("chain-graph")).toHaveAttribute("data-follow", "true")
    expect(reactFlow.setCenter).toHaveBeenCalledWith(1150, 48, expect.objectContaining({ zoom: 1 }))
  })

  it("does not center while follow is paused", () => {
    render(
      <ChainGraph
        nodes={[issueNode]}
        edges={[]}
        selectedNodeId="issue:9101"
        onSelectNode={vi.fn()}
        follow={false}
        followNodeId="issue:9101"
      />,
    )

    expect(reactFlow.setCenter).not.toHaveBeenCalled()
    expect(screen.getByTestId("chain-graph")).toHaveAttribute("data-follow", "false")
  })

  it("pauses follow on a real user pan/zoom event", () => {
    const onUserMove = vi.fn()
    render(
      <ChainGraph
        nodes={[issueNode]}
        edges={[]}
        selectedNodeId={null}
        onSelectNode={vi.fn()}
        follow
        followNodeId={null}
        onUserMove={onUserMove}
      />,
    )

    fireEvent.click(screen.getByRole("button", { name: "模拟用户平移" }))
    expect(onUserMove).toHaveBeenCalledTimes(1)
  })
})

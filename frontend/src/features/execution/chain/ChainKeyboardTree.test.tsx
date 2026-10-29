import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { type ChainKeyboardNode, ChainKeyboardTree } from "./ChainKeyboardTree"

const R1 = "1844674407370955101"
const R2 = "1844674407370955102"
const C1 = "1844674407370955201"
const C2 = "1844674407370955202"
const CR1 = "1844674407370955301"
const CR2 = "1844674407370955302"
const CR3 = "1844674407370955303"

function chainNode(
  domainId: string,
  kind: ChainKeyboardNode["kind"],
  overrides: Partial<ChainKeyboardNode> = {},
): ChainKeyboardNode {
  return {
    id: idOf(kind, domainId),
    kind,
    title: domainId,
    status: "open",
    requirementIds: [],
    caseNodeId: null,
    resultNodeId: null,
    hasChildren: false,
    collapsed: false,
    ...overrides,
  }
}

/** Pre-order: R1 → C1 → CR1 → CR2; R2 → C2 → CR3. */
const NODES: ChainKeyboardNode[] = [
  chainNode(R1, "requirement", {
    title: "登录需求",
    status: "clarified",
    requirementIds: [idOf("requirement", R1)],
    hasChildren: true,
  }),
  chainNode(C1, "case", {
    title: "登录成功",
    status: "executed",
    requirementIds: [idOf("requirement", R1)],
    caseNodeId: idOf("case", C1),
  }),
  chainNode(CR1, "result", {
    title: "登录成功",
    status: "passed",
    caseNodeId: idOf("case", C1),
    resultNodeId: idOf("result", CR1),
  }),
  chainNode(CR2, "result", {
    title: "登录失败",
    status: "failed",
    caseNodeId: idOf("case", C1),
    resultNodeId: idOf("result", CR2),
  }),
  chainNode(R2, "requirement", {
    title: "下单需求",
    status: "designed",
    requirementIds: [idOf("requirement", R2)],
    hasChildren: true,
  }),
  chainNode(C2, "case", {
    title: "提交订单",
    status: "reviewed",
    requirementIds: [idOf("requirement", R2)],
    caseNodeId: idOf("case", C2),
  }),
  chainNode(CR3, "result", {
    title: "提交订单",
    status: "passed",
    caseNodeId: idOf("case", C2),
    resultNodeId: idOf("result", CR3),
  }),
]

function idOf(kind: string, id: string): string {
  return `${kind}:${id}`
}

function renderTree(overrides: Partial<Parameters<typeof ChainKeyboardTree>[0]> = {}) {
  const onSelect = vi.fn()
  const onToggleCollapse = vi.fn()
  const onEscape = vi.fn()
  const utils = render(
    <ChainKeyboardTree
      nodes={NODES}
      onSelect={onSelect}
      onToggleCollapse={onToggleCollapse}
      onEscape={onEscape}
      {...overrides}
    />,
  )
  return { ...utils, onSelect, onToggleCollapse, onEscape }
}

function item(id: string): HTMLElement {
  return screen.getByTestId(`chain-treeitem-${id}`)
}

afterEach(() => {
  cleanup()
})

describe("ChainKeyboardTree", () => {
  it("renders a flat tree with level, set size and position per layer", () => {
    renderTree()

    const tree = screen.getByRole("tree")
    expect(tree).not.toBeNull()

    expect(item(idOf("requirement", R1)).getAttribute("aria-level")).toBe("1")
    expect(item(idOf("requirement", R1)).getAttribute("aria-setsize")).toBe("2")
    expect(item(idOf("requirement", R1)).getAttribute("aria-posinset")).toBe("1")
    expect(item(idOf("requirement", R2)).getAttribute("aria-posinset")).toBe("2")

    expect(item(idOf("case", C1)).getAttribute("aria-level")).toBe("2")
    expect(item(idOf("case", C1)).getAttribute("aria-setsize")).toBe("1")
    expect(item(idOf("result", CR1)).getAttribute("aria-level")).toBe("3")
    expect(item(idOf("result", CR1)).getAttribute("aria-setsize")).toBe("2")
    expect(item(idOf("result", CR2)).getAttribute("aria-posinset")).toBe("2")

    expect(item(idOf("requirement", R1)).getAttribute("aria-expanded")).toBe("true")
    expect(item(idOf("result", CR1)).getAttribute("aria-expanded")).toBeNull()
  })

  it("uses roving focus: exactly one treeitem is tabbable and arrows move it", () => {
    renderTree()

    const first = item(idOf("requirement", R1))
    expect(first.getAttribute("tabindex")).toBe("0")
    expect(item(idOf("case", C1)).getAttribute("tabindex")).toBe("-1")

    first.focus()
    fireEvent.keyDown(first, { key: "ArrowDown" })

    const caseItem = item(idOf("case", C1))
    expect(document.activeElement).toBe(caseItem)
    expect(caseItem.getAttribute("tabindex")).toBe("0")
    expect(first.getAttribute("tabindex")).toBe("-1")

    fireEvent.keyDown(caseItem, { key: "ArrowDown" })
    expect(document.activeElement).toBe(item(idOf("result", CR1)))
    fireEvent.keyDown(item(idOf("result", CR1)), { key: "ArrowUp" })
    expect(document.activeElement).toBe(caseItem)
    fireEvent.keyDown(caseItem, { key: "End" })
    expect(document.activeElement).toBe(item(idOf("result", CR3)))
    fireEvent.keyDown(item(idOf("result", CR3)), { key: "Home" })
    expect(document.activeElement).toBe(first)
  })

  it("expands or collapses requirements with left/right and walks the tree", () => {
    const { onToggleCollapse } = renderTree({ nodes: NODES })

    const requirement = item(idOf("requirement", R1))
    // Expanded: right walks to the first child, left collapses.
    fireEvent.keyDown(requirement, { key: "ArrowRight" })
    expect(document.activeElement).toBe(item(idOf("case", C1)))
    fireEvent.keyDown(item(idOf("case", C1)), { key: "ArrowLeft" })
    expect(document.activeElement).toBe(requirement)
    fireEvent.keyDown(requirement, { key: "ArrowLeft" })
    expect(onToggleCollapse).toHaveBeenCalledWith(idOf("requirement", R1))
  })

  it("expands a collapsed requirement when moving right", () => {
    const collapsedNodes = NODES.map((node) =>
      node.id === idOf("requirement", R1) ? { ...node, collapsed: true } : node,
    )
    const { onToggleCollapse } = renderTree({ nodes: collapsedNodes })

    const requirement = item(idOf("requirement", R1))
    expect(requirement.getAttribute("aria-expanded")).toBe("false")
    requirement.focus()
    fireEvent.keyDown(requirement, { key: "ArrowRight" })

    expect(onToggleCollapse).toHaveBeenCalledWith(idOf("requirement", R1))
    expect(document.activeElement).toBe(requirement)
  })

  it("opens a node on Enter/Space and closes on Escape", () => {
    const { onSelect, onEscape } = renderTree()

    const caseItem = item(idOf("case", C1))
    fireEvent.keyDown(caseItem, { key: "Enter" })
    expect(onSelect).toHaveBeenLastCalledWith(idOf("case", C1))

    fireEvent.keyDown(caseItem, { key: " " })
    expect(onSelect).toHaveBeenLastCalledWith(idOf("case", C1))

    fireEvent.keyDown(caseItem, { key: "Escape" })
    expect(onEscape).toHaveBeenCalledTimes(1)
  })

  it("syncs aria-selected and the initial roving tab stop to the selection", () => {
    renderTree({ selectedId: idOf("case", C2) })

    const selected = item(idOf("case", C2))
    expect(selected.getAttribute("aria-selected")).toBe("true")
    expect(selected.getAttribute("tabindex")).toBe("0")
    expect(item(idOf("result", CR3)).getAttribute("aria-selected")).toBe("false")
  })

  it("exposes a Chinese aria label with kind, title and status", () => {
    renderTree()

    const label = item(idOf("result", CR2)).getAttribute("aria-label") ?? ""
    expect(label).toContain("结果")
    expect(label).toContain("登录失败")
    expect(label).toContain("失败")
  })
})

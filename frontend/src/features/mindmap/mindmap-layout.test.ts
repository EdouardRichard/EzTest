import { describe, expect, it } from "vitest"
import {
  DEFAULT_HORIZONTAL_GAP,
  DEFAULT_NODE_HEIGHT,
  DEFAULT_NODE_WIDTH,
  DEFAULT_VERTICAL_GAP,
  layoutMindmap,
} from "./mindmap-layout"
import type { CaseNodeView } from "./types"

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

function scenario(id: string, title: string, overrides: Partial<CaseNodeView> = {}): CaseNodeView {
  return caseNode({ id, title, kind: "scenario", ...overrides })
}

function caseUnder(
  id: string,
  title: string,
  parentId: string,
  sortOrder: number,
  overrides: Partial<CaseNodeView> = {},
): CaseNodeView {
  return caseNode({ id, title, parentId, sortOrder, ...overrides })
}

/**
 * T21 fixture (also used by the canvas tests):
 *
 *   s1 (scenario, root)
 *   ├── s2 (scenario, sort 0)
 *   │   ├── c2 (case, sort 0)
 *   │   └── c3 (case, sort 1)
 *   └── c1 (case, sort 1)
 */
const S1 = "1844674407370955101"
const S2 = "1844674407370955102"
const C1 = "1844674407370955103"
const C2 = "1844674407370955104"
const C3 = "1844674407370955105"

function nestedNodes(): CaseNodeView[] {
  return [
    scenario(S1, "订单场景", { sortOrder: 0 }),
    scenario(S2, "创建订单", { parentId: S1, sortOrder: 0 }),
    caseUnder(C1, "取消订单", S1, 1),
    caseUnder(C2, "成功创建", S2, 0),
    caseUnder(C3, "参数校验", S2, 1),
  ]
}

const PER_LEVEL_Y = DEFAULT_NODE_HEIGHT + DEFAULT_VERTICAL_GAP
const PER_SIBLING_X = DEFAULT_NODE_WIDTH + DEFAULT_HORIZONTAL_GAP

describe("layoutMindmap", () => {
  it("returns an empty result for an empty graph", () => {
    const result = layoutMindmap([])

    expect(result.nodes).toEqual([])
    expect(result.edges).toEqual([])
    expect(result.visibleIds).toEqual([])
    expect(result.hiddenIds).toEqual([])
    expect(result.bounds).toEqual({ x: 0, y: 0, width: 0, height: 0 })
  })

  it("lays out a single root node at the origin", () => {
    const result = layoutMindmap([scenario(S1, "订单场景")])

    expect(result.nodes).toHaveLength(1)
    const only = result.nodes[0]
    expect(only.id).toBe(S1)
    expect(only.x).toBe(0)
    expect(only.y).toBe(0)
    expect(only.w).toBe(DEFAULT_NODE_WIDTH)
    expect(only.h).toBe(DEFAULT_NODE_HEIGHT)
    expect(only.depth).toBe(0)
    expect(only.hasChildren).toBe(false)
    expect(result.edges).toEqual([])
    expect(result.visibleIds).toEqual([S1])
    expect(result.bounds).toEqual({
      x: 0,
      y: 0,
      width: DEFAULT_NODE_WIDTH,
      height: DEFAULT_NODE_HEIGHT,
    })
  })

  it("orders siblings by sortOrder and places them left to right", () => {
    const nodes = [
      scenario(S1, "订单场景"),
      caseUnder(C1, "第三", S1, 2),
      caseUnder(C2, "第一", S1, 1),
      caseUnder(C3, "第二", S1, 0),
    ]

    const result = layoutMindmap(nodes)
    const byId = new Map(result.nodes.map((node) => [node.id, node]))

    expect(result.nodes).toHaveLength(4)
    expect(byId.get(C3)?.x).toBe(0)
    expect(byId.get(C2)?.x).toBe(PER_SIBLING_X)
    expect(byId.get(C1)?.x).toBe(PER_SIBLING_X * 2)
    expect(byId.get(S1)?.x).toBe(PER_SIBLING_X)
    for (const childId of [C1, C2, C3]) {
      expect(byId.get(childId)?.y).toBe(PER_LEVEL_Y)
      expect(byId.get(childId)?.depth).toBe(1)
    }
    expect(result.visibleIds).toEqual([S1, C3, C2, C1])
    expect(result.edges).toHaveLength(3)
    expect(result.edges.every((edge) => edge.parentId === S1)).toBe(true)
  })

  it("lays out a nested tree as a tidy tree with subtree widths", () => {
    const result = layoutMindmap(nestedNodes())
    const byId = new Map(result.nodes.map((node) => [node.id, node]))

    expect(result.nodes).toHaveLength(5)
    expect(byId.get(S1)).toMatchObject({ x: 248, y: 0, depth: 0 })
    expect(byId.get(S2)).toMatchObject({ x: 124, y: PER_LEVEL_Y, depth: 1 })
    expect(byId.get(C1)).toMatchObject({ x: 496, y: PER_LEVEL_Y, depth: 1 })
    expect(byId.get(C2)).toMatchObject({ x: 0, y: PER_LEVEL_Y * 2, depth: 2 })
    expect(byId.get(C3)).toMatchObject({ x: 248, y: PER_LEVEL_Y * 2, depth: 2 })

    // A parent is centred over the span of its whole child subtrees.
    expect((byId.get(S1)?.x ?? 0) + DEFAULT_NODE_WIDTH / 2).toBe(
      (DEFAULT_NODE_WIDTH * 3 + DEFAULT_HORIZONTAL_GAP * 2) / 2,
    )

    expect(result.bounds).toEqual({
      x: 0,
      y: 0,
      width: DEFAULT_NODE_WIDTH * 3 + DEFAULT_HORIZONTAL_GAP * 2,
      height: DEFAULT_NODE_HEIGHT + PER_LEVEL_Y * 2,
    })
    expect(result.edges.map((edge) => `${edge.parentId}->${edge.childId}`).sort()).toEqual(
      [`${S1}->${S2}`, `${S1}->${C1}`, `${S2}->${C2}`, `${S2}->${C3}`].sort(),
    )
  })

  it("keeps sibling cards from overlapping at every depth", () => {
    const result = layoutMindmap(nestedNodes())
    const childrenOf = new Map<string, { x: number; w: number }[]>()

    for (const node of result.nodes) {
      const parentId = node.node.parentId
      if (parentId === null || !result.visibleIds.includes(parentId)) continue
      const siblings = childrenOf.get(parentId) ?? []
      siblings.push({ x: node.x, w: node.w })
      childrenOf.set(parentId, siblings)
    }

    for (const siblings of childrenOf.values()) {
      const sorted = [...siblings].sort((a, b) => a.x - b.x)
      for (let index = 1; index < sorted.length; index += 1) {
        expect(sorted[index - 1].x + sorted[index - 1].w).toBeLessThanOrEqual(sorted[index].x)
      }
    }
  })

  it("hides every descendant of a collapsed scenario and reports the hidden set", () => {
    const result = layoutMindmap(nestedNodes(), { collapsed: new Set([S1]) })

    expect(result.visibleIds).toEqual([S1])
    expect(result.hiddenIds).toEqual([S2, C1, C2, C3].sort())
    expect(result.edges).toEqual([])
    expect(result.nodes).toHaveLength(1)
    const root = result.nodes[0]
    expect(root.id).toBe(S1)
    expect(root.hasChildren).toBe(true)
    expect(root.descendantCount).toBe(4)
    expect(root.x).toBe(0)
    expect(result.bounds.width).toBe(DEFAULT_NODE_WIDTH)
  })

  it("hides only the collapsed subtree and re-centres the surviving tree", () => {
    const result = layoutMindmap(nestedNodes(), { collapsed: new Set([S2]) })
    const byId = new Map(result.nodes.map((node) => [node.id, node]))

    expect(result.nodes.map((node) => node.id).sort()).toEqual([S1, S2, C1].sort())
    expect(result.visibleIds).toEqual([S1, S2, C1])
    expect(result.hiddenIds).toEqual([C2, C3].sort())
    expect(result.edges.map((edge) => `${edge.parentId}->${edge.childId}`).sort()).toEqual(
      [`${S1}->${S2}`, `${S1}->${C1}`].sort(),
    )
    expect(byId.get(S1)).toMatchObject({ x: 124, y: 0 })
    expect(byId.get(S2)).toMatchObject({ x: 0, y: PER_LEVEL_Y })
    expect(byId.get(C1)).toMatchObject({ x: 248, y: PER_LEVEL_Y })
    expect(byId.get(S2)?.hasChildren).toBe(true)
    expect(byId.get(S2)?.descendantCount).toBe(2)
  })

  it("ignores collapsed ids that are leaves or absent", () => {
    const withoutUnknown = layoutMindmap(nestedNodes(), {
      collapsed: new Set([C1, "1844674407370955999"]),
    })
    const plain = layoutMindmap(nestedNodes())

    expect(withoutUnknown.nodes).toEqual(plain.nodes)
    expect(withoutUnknown.hiddenIds).toEqual([])
    expect(withoutUnknown.visibleIds).toEqual(plain.visibleIds)
  })

  it("accepts a collapsed array as well as a set", () => {
    const withArray = layoutMindmap(nestedNodes(), { collapsed: [S2] })
    const withSet = layoutMindmap(nestedNodes(), { collapsed: new Set([S2]) })

    expect(withArray.hiddenIds).toEqual(withSet.hiddenIds)
    expect(withArray.nodes).toEqual(withSet.nodes)
  })

  it("honours explicit node size and gap overrides", () => {
    const result = layoutMindmap(
      [scenario(S1, "根"), caseUnder(C1, "子一", S1, 0), caseUnder(C2, "子二", S1, 1)],
      {
        nodeWidth: 100,
        nodeHeight: 40,
        horizontalGap: 20,
        verticalGap: 10,
      },
    )
    const byId = new Map(result.nodes.map((node) => [node.id, node]))

    expect(byId.get(S1)?.w).toBe(100)
    expect(byId.get(S1)?.h).toBe(40)
    expect(byId.get(S1)?.x).toBe(60)
    expect(byId.get(S1)?.y).toBe(0)
    expect(byId.get(C1)?.x).toBe(0)
    expect(byId.get(C2)?.x).toBe(120)
    expect(byId.get(C1)?.y).toBe(50)
    expect(result.bounds).toEqual({ x: 0, y: 0, width: 220, height: 90 })
  })

  it("is deterministic for a 200-node graph, independent of input order", () => {
    const root = scenario(S1, "根")
    const children = Array.from({ length: 199 }, (_, index) =>
      caseUnder(
        `1844674407370956${String(100 + index).padStart(3, "0")}`,
        `用例 ${index}`,
        S1,
        index,
      ),
    )
    const nodes = [root, ...children]

    const first = layoutMindmap(nodes)
    const second = layoutMindmap(nodes)
    const reversed = layoutMindmap([...nodes].reverse())

    expect(first).toEqual(second)
    expect(first).toEqual(reversed)
    expect(first.nodes).toHaveLength(200)
    expect(first.edges).toHaveLength(199)
    expect(new Set(first.nodes.map((node) => node.id)).size).toBe(200)

    for (const node of first.nodes) {
      expect(Number.isFinite(node.x)).toBe(true)
      expect(Number.isFinite(node.y)).toBe(true)
      expect(node.x).toBeGreaterThanOrEqual(first.bounds.x)
      expect(node.y).toBeGreaterThanOrEqual(first.bounds.y)
      expect(node.x + node.w).toBeLessThanOrEqual(first.bounds.x + first.bounds.width)
      expect(node.y + node.h).toBeLessThanOrEqual(first.bounds.y + first.bounds.height)
    }

    // Every pair of siblings is spaced by exactly one card plus one gap.
    expect(new Set(first.nodes.slice(1).map((node) => node.x)).size).toBe(199)
  })

  it("treats orphaned nodes as roots instead of dropping them", () => {
    const orphan = caseNode({ id: C1, title: "孤儿", parentId: "1844674407370955999" })
    const result = layoutMindmap([scenario(S1, "根"), orphan])
    const byId = new Map(result.nodes.map((node) => [node.id, node]))

    expect(result.nodes).toHaveLength(2)
    expect(result.edges).toEqual([])
    expect(byId.get(S1)?.x).toBe(0)
    expect(byId.get(C1)?.x).toBe(PER_SIBLING_X)
    expect(byId.get(C1)?.y).toBe(0)
  })

  it("terminates on a parent cycle and keeps each node exactly once", () => {
    const a = scenario(S1, "A", { parentId: S2 })
    const b = scenario(S2, "B", { parentId: S1 })

    const result = layoutMindmap([a, b])

    expect(result.nodes).toHaveLength(2)
    expect(new Set(result.nodes.map((node) => node.id)).size).toBe(2)
    expect(result.edges).toHaveLength(1)
    expect(result.visibleIds).toHaveLength(2)
    expect(result.hiddenIds).toEqual([])
  })
})

"use client"

import { type KeyboardEvent, useCallback, useEffect, useMemo, useRef, useState } from "react"

import { cn } from "@/lib/utils"

import {
  getCaseStatusToken,
  getIssueStatusToken,
  getRequirementStatusToken,
  getResultStatusToken,
  NODE_KIND_TOKENS,
} from "./chain-tokens"
import type { ChainSnapshotNode } from "./chain-types"

/** Minimal subset of a positioned layout node the keyboard tree needs. */
export type ChainKeyboardNode = {
  id: string
  kind: ChainSnapshotNode["kind"]
  title: string
  status: string
  requirementIds: readonly string[]
  caseNodeId: string | null
  resultNodeId: string | null
  hasChildren: boolean
  collapsed: boolean
}

export type ChainKeyboardTreeProps = {
  /** Visible nodes in pre-order (the layout order is the tree order). */
  nodes: readonly ChainKeyboardNode[]
  selectedId?: string | null
  /**
   * Controlled active (roving tab stop) id. When provided, the tree reports
   * every change through `onActiveChange` and never moves its own state; the
   * canvas owner lifts this so the visible card can follow the keyboard.
   */
  activeId?: string | null
  onActiveChange?: (nodeId: string) => void
  /** Called only for an actual direction-key move, so the canvas can reveal it. */
  onActiveNavigate?: (nodeId: string) => void
  onSelect: (nodeId: string) => void
  onToggleCollapse?: (requirementId: string) => void
  onEscape?: () => void
  label?: string
  className?: string
}

function statusLabelOf(node: ChainKeyboardNode): string {
  if (node.kind === "result") return getResultStatusToken(node.status).label
  if (node.kind === "case") return getCaseStatusToken(node.status).label
  if (node.kind === "issue") return getIssueStatusToken(node.status).label
  return getRequirementStatusToken(node.status).label
}

function ariaLabelOf(node: ChainKeyboardNode): string {
  const kind = NODE_KIND_TOKENS[node.kind].label
  const parts = [`${kind}：${node.title}`, statusLabelOf(node)]
  if (node.kind === "requirement" && node.hasChildren) {
    parts.push(node.collapsed ? "已折叠" : "已展开")
  }
  return parts.join("，")
}

type TreeIndex = {
  parentById: Map<string, string>
  childrenById: Map<string, string[]>
  levelById: Map<string, number>
  setsizeById: Map<string, number>
  posinsetById: Map<string, number>
  orderIndexById: Map<string, number>
}

/**
 * Builds the parent/child hierarchy from the flat pre-order layout.
 *
 * The chain layout renders a requirement group as requirement → case →
 * results/issues, so the visual order already is a valid tree order; parentage
 * is resolved from the snapshot link fields (`requirementIds`, `caseNodeId`,
 * `resultNodeId`) and only visible nodes may be parents.
 */
function buildTreeIndex(nodes: readonly ChainKeyboardNode[]): TreeIndex {
  const visible = new Set(nodes.map((node) => node.id))
  const parentById = new Map<string, string>()
  const childrenById = new Map<string, string[]>()
  const rootIds: string[] = []

  for (const node of nodes) {
    let parent: string | null = null
    if (node.kind === "case") {
      parent = node.requirementIds.find((id) => visible.has(id)) ?? null
    } else if (node.kind === "result") {
      parent = node.caseNodeId !== null && visible.has(node.caseNodeId) ? node.caseNodeId : null
    } else if (node.kind === "issue") {
      const candidate = node.resultNodeId ?? node.caseNodeId
      parent = candidate !== null && visible.has(candidate) ? candidate : null
    }
    if (parent === null) {
      rootIds.push(node.id)
      continue
    }
    parentById.set(node.id, parent)
    const children = childrenById.get(parent)
    if (children === undefined) childrenById.set(parent, [node.id])
    else children.push(node.id)
  }

  const levelById = new Map<string, number>()
  const setsizeById = new Map<string, number>()
  const posinsetById = new Map<string, number>()
  const orderIndexById = new Map(nodes.map((node, index) => [node.id, index]))

  function assignLevel(id: string, level: number): void {
    levelById.set(id, level)
    for (const child of childrenById.get(id) ?? []) assignLevel(child, level + 1)
  }
  for (const rootId of rootIds) assignLevel(rootId, 1)

  const groups: string[][] = [rootIds, ...childrenById.values()]
  for (const group of groups) {
    for (const [index, id] of group.entries()) {
      setsizeById.set(id, group.length)
      posinsetById.set(id, index + 1)
    }
  }

  return { parentById, childrenById, levelById, setsizeById, posinsetById, orderIndexById }
}

/**
 * Visually hidden WAI-ARIA tree of the exploration chain (spec §5.3/§5.7).
 *
 * React Flow's built-in keyboard navigation is disabled on the canvas
 * (`disableKeyboardA11y`, `nodesFocusable/edgesFocusable=false`); this tree is
 * the keyboard/screen-reader contract instead: one treeitem per visible node,
 * roving tabindex, ↑↓/Home/End within the flat pre-order, ←→ parent/child plus
 * requirement collapse/expand, Enter/Space opens the Sheet and Esc closes it.
 */
export function ChainKeyboardTree({
  nodes,
  selectedId = null,
  activeId,
  onActiveChange,
  onActiveNavigate,
  onSelect,
  onToggleCollapse,
  onEscape,
  label = "探索链路节点（键盘导航）",
  className,
}: ChainKeyboardTreeProps) {
  const tree = useMemo(() => buildTreeIndex(nodes), [nodes])
  const itemRefs = useRef(new Map<string, HTMLDivElement>())
  const pendingFocusRef = useRef<string | null>(null)

  const [internalActiveId, setInternalActiveId] = useState<string | null>(() => {
    if (selectedId !== null && nodes.some((node) => node.id === selectedId)) return selectedId
    return nodes[0]?.id ?? null
  })
  // `activeId !== undefined` makes the tree controlled: the canvas owner owns
  // the roving tab stop so both the hidden tree and the visible cards can
  // render the same active node.
  const isControlled = activeId !== undefined
  const currentActiveId = isControlled ? (activeId ?? null) : internalActiveId

  const changeActive = useCallback(
    (nextId: string): void => {
      if (nextId === currentActiveId) return
      if (!isControlled) setInternalActiveId(nextId)
      onActiveChange?.(nextId)
    },
    [currentActiveId, isControlled, onActiveChange],
  )

  // A selected node (canvas click or deep link) becomes the roving tab stop.
  // Only actual selection changes may move it: snapshot refreshes deliver fresh
  // node arrays and must not reset where the keyboard user currently is.
  const lastSelectedIdRef = useRef<string | null>(selectedId)
  useEffect(() => {
    const nextSelectedId = selectedId ?? null
    if (nextSelectedId === lastSelectedIdRef.current) return
    lastSelectedIdRef.current = nextSelectedId
    if (
      nextSelectedId !== null &&
      nextSelectedId !== "" &&
      nodes.some((node) => node.id === nextSelectedId)
    ) {
      changeActive(nextSelectedId)
    }
  }, [selectedId, nodes, changeActive])

  // Collapsing a group can hide the active node; fall back to the selection or
  // the first visible node so Tab never leads into a detached treeitem.
  useEffect(() => {
    if (currentActiveId !== null && nodes.some((node) => node.id === currentActiveId)) return
    if (selectedId !== null && selectedId !== "" && nodes.some((node) => node.id === selectedId)) {
      changeActive(selectedId)
      return
    }
    const firstId = nodes[0]?.id
    if (firstId !== undefined) changeActive(firstId)
  }, [nodes, selectedId, currentActiveId, changeActive])

  useEffect(() => {
    if (currentActiveId === null) return
    if (pendingFocusRef.current !== currentActiveId) return
    pendingFocusRef.current = null
    itemRefs.current.get(currentActiveId)?.focus()
  }, [currentActiveId])

  function moveTo(nextId: string | null): void {
    if (nextId === null || nextId === currentActiveId) return
    pendingFocusRef.current = nextId
    changeActive(nextId)
    onActiveNavigate?.(nextId)
  }

  function firstChildOf(nodeId: string): string | null {
    return tree.childrenById.get(nodeId)?.[0] ?? null
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>, node: ChainKeyboardNode): void {
    const index = tree.orderIndexById.get(node.id) ?? -1
    switch (event.key) {
      case "ArrowDown": {
        event.preventDefault()
        moveTo(nodes[Math.min(index + 1, nodes.length - 1)]?.id ?? null)
        return
      }
      case "ArrowUp": {
        event.preventDefault()
        moveTo(nodes[Math.max(index - 1, 0)]?.id ?? null)
        return
      }
      case "Home": {
        event.preventDefault()
        moveTo(nodes[0]?.id ?? null)
        return
      }
      case "End": {
        event.preventDefault()
        moveTo(nodes[nodes.length - 1]?.id ?? null)
        return
      }
      case "ArrowRight": {
        event.preventDefault()
        if (node.kind === "requirement" && node.hasChildren && node.collapsed) {
          onToggleCollapse?.(node.id)
          return
        }
        moveTo(firstChildOf(node.id))
        return
      }
      case "ArrowLeft": {
        event.preventDefault()
        if (node.kind === "requirement" && node.hasChildren && !node.collapsed) {
          onToggleCollapse?.(node.id)
          return
        }
        moveTo(tree.parentById.get(node.id) ?? null)
        return
      }
      case "Enter":
      case " ": {
        event.preventDefault()
        onSelect(node.id)
        return
      }
      case "Escape": {
        event.preventDefault()
        event.stopPropagation()
        onEscape?.()
        return
      }
      default:
        return
    }
  }

  return (
    <div
      role="tree"
      aria-label={label}
      data-testid="chain-keyboard-tree"
      className={cn("sr-only", className)}
    >
      {nodes.map((node) => {
        const level = tree.levelById.get(node.id) ?? 1
        // Every node with visible children is an expandable treeitem: only the
        // requirement can collapse, case/result groups are always expanded.
        const expanded = node.hasChildren
          ? node.kind === "requirement"
            ? !node.collapsed
            : true
          : undefined
        return (
          <div
            key={node.id}
            ref={(element) => {
              if (element === null) itemRefs.current.delete(node.id)
              else itemRefs.current.set(node.id, element)
            }}
            role="treeitem"
            data-testid={`chain-treeitem-${node.id}`}
            data-node-id={node.id}
            data-kind={node.kind}
            data-active={currentActiveId === node.id ? "true" : "false"}
            aria-level={level}
            aria-setsize={tree.setsizeById.get(node.id) ?? 1}
            aria-posinset={tree.posinsetById.get(node.id) ?? 1}
            aria-selected={selectedId === node.id}
            aria-expanded={expanded}
            aria-label={ariaLabelOf(node)}
            tabIndex={currentActiveId === node.id ? 0 : -1}
            className="focus-visible:outline-none"
            onFocus={() => changeActive(node.id)}
            onKeyDown={(event) => handleKeyDown(event, node)}
            onClick={() => onSelect(node.id)}
          >
            {ariaLabelOf(node)}
          </div>
        )
      })}
    </div>
  )
}

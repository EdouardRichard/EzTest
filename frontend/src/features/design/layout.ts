import type { CaseRunVerdict } from "@/features/execution/types"

import type { DesignCaseSummary, DesignGraph, DesignRequirement, DesignScenario } from "./types"

/** Column x-origins for the deterministic three-level mindmap layout. */
export const LAYOUT_COLUMNS = {
  requirement: 0,
  scenario: 360,
  case: 720,
} as const

/** Vertical distance between two stacked nodes; cards are ~110px tall. */
export const LAYOUT_ROW_GAP = 150

/** Extra space between two requirement subtrees. */
export const LAYOUT_SECTION_GAP = 60

export type DesignNodeData =
  | { kind: "requirement"; requirement: DesignRequirement }
  | {
      kind: "scenario"
      requirement: DesignRequirement
      scenario: DesignScenario
      draftCaseCount: number
    }
  | {
      kind: "case"
      requirement: DesignRequirement
      scenario: DesignScenario
      case: DesignCaseSummary
    }

export type DesignLayoutNode = {
  id: string
  type: DesignNodeData["kind"]
  position: { x: number; y: number }
  data: DesignNodeData
}

export type DesignLayoutEdge = {
  id: string
  source: string
  target: string
}

export type DesignLayout = {
  nodes: DesignLayoutNode[]
  edges: DesignLayoutEdge[]
}

function collectDraftCount(requirement: DesignRequirement): number {
  return requirement.scenarios.reduce(
    (total, scenario) =>
      total + scenario.cases.filter((designCase) => designCase.status === "draft").length,
    0,
  )
}

/**
 * Pure deterministic column layout: requirement → scenario → case. Node
 * positions are local UI state only and are never persisted (spec §6.3); the
 * same graph always produces the same coordinates, so a refresh restores the
 * default view without a DB round-trip.
 */
export function layoutDesignGraph(graph: DesignGraph): DesignLayout {
  const nodes: DesignLayoutNode[] = []
  const edges: DesignLayoutEdge[] = []
  let cursor = 0

  for (const requirement of graph.requirements) {
    const requirementId = `requirement-${requirement.requirement_id}`
    const requirementY = cursor
    cursor += LAYOUT_ROW_GAP

    for (const scenario of requirement.scenarios) {
      const scenarioId = `scenario-${scenario.id}`
      const scenarioY = cursor
      cursor += LAYOUT_ROW_GAP
      edges.push({
        id: `${requirementId}-${scenarioId}`,
        source: requirementId,
        target: scenarioId,
      })

      for (const designCase of scenario.cases) {
        const caseId = `case-${designCase.case_id}`
        nodes.push({
          id: caseId,
          type: "case",
          position: { x: LAYOUT_COLUMNS.case, y: cursor },
          data: { kind: "case", requirement, scenario, case: designCase },
        })
        cursor += LAYOUT_ROW_GAP
        edges.push({ id: `${scenarioId}-${caseId}`, source: scenarioId, target: caseId })
      }

      nodes.push({
        id: scenarioId,
        type: "scenario",
        position: { x: LAYOUT_COLUMNS.scenario, y: scenarioY },
        data: {
          kind: "scenario",
          requirement,
          scenario,
          draftCaseCount: scenario.cases.filter((designCase) => designCase.status === "draft")
            .length,
        },
      })
    }

    nodes.push({
      id: requirementId,
      type: "requirement",
      position: { x: LAYOUT_COLUMNS.requirement, y: requirementY },
      data: { kind: "requirement", requirement },
    })
    cursor += LAYOUT_SECTION_GAP
  }

  return { nodes, edges }
}

/** Number of draft cases below a requirement; drives the confirm-all button. */
export function requirementDraftCount(requirement: DesignRequirement): number {
  return collectDraftCount(requirement)
}

/* ------------------------------- status / search ---------------------------- */

/** Sentinel for "no filter" in every dropdown. */
export const ALL_FILTER_VALUE = "all"

/** The four mindmap filters plus the search box; search never hides nodes. */
export type CaseFilterValues = {
  requirementId: string
  scenarioId: string
  status: string
  result: string
  query: string
}

export const DEFAULT_CASE_FILTERS: CaseFilterValues = {
  requirementId: ALL_FILTER_VALUE,
  scenarioId: ALL_FILTER_VALUE,
  status: ALL_FILTER_VALUE,
  result: ALL_FILTER_VALUE,
  query: "",
}

/** Latest terminal execution verdict per logical case id (spec §9.3). */
export type DesignExecutionResults = Record<string, CaseRunVerdict>

type CaseNodeData = Extract<DesignNodeData, { kind: "case" }>

function caseMatches(
  data: CaseNodeData,
  filters: CaseFilterValues,
  results: DesignExecutionResults,
): boolean {
  const { requirement, scenario, case: designCase } = data
  if (
    filters.requirementId !== ALL_FILTER_VALUE &&
    requirement.requirement_id !== filters.requirementId
  ) {
    return false
  }
  if (filters.scenarioId !== ALL_FILTER_VALUE && scenario.id !== filters.scenarioId) {
    return false
  }
  if (filters.status !== ALL_FILTER_VALUE && designCase.status !== filters.status) {
    return false
  }
  if (filters.result !== ALL_FILTER_VALUE && results[designCase.case_id] !== filters.result) {
    return false
  }
  return true
}

/**
 * Prunes the layout to the nodes that match the filters. Case status/result
 * filters keep the ancestor chain only for scenarios with a matching case, so
 * an empty branch disappears instead of leaving an orphan canvas.
 */
export function filterDesignLayout(
  layout: DesignLayout,
  filters: CaseFilterValues,
  results: DesignExecutionResults = {},
): DesignLayout {
  const caseFiltersActive =
    filters.status !== ALL_FILTER_VALUE || filters.result !== ALL_FILTER_VALUE

  const visibleCaseIds = new Set<string>()
  for (const node of layout.nodes) {
    if (node.data.kind === "case" && caseMatches(node.data, filters, results)) {
      visibleCaseIds.add(node.id)
    }
  }

  const visibleScenarioIds = new Set<string>()
  const visibleRequirementIds = new Set<string>()

  for (const node of layout.nodes) {
    if (node.data.kind !== "scenario") continue
    const { requirement, scenario } = node.data
    if (
      filters.requirementId !== ALL_FILTER_VALUE &&
      requirement.requirement_id !== filters.requirementId
    ) {
      continue
    }
    if (filters.scenarioId !== ALL_FILTER_VALUE && scenario.id !== filters.scenarioId) continue
    if (
      caseFiltersActive &&
      !scenario.cases.some((designCase) => visibleCaseIds.has(`case-${designCase.case_id}`))
    ) {
      continue
    }
    visibleScenarioIds.add(scenario.id)
    visibleRequirementIds.add(requirement.requirement_id)
  }

  // Requirements without scenarios stay visible while no scenario/case filter
  // prunes the tree; they are a valid "pending design" node.
  for (const node of layout.nodes) {
    if (node.data.kind !== "requirement") continue
    const { requirement } = node.data
    if (
      filters.requirementId !== ALL_FILTER_VALUE &&
      requirement.requirement_id !== filters.requirementId
    ) {
      continue
    }
    if (requirement.scenarios.length === 0 && filters.scenarioId === ALL_FILTER_VALUE) {
      if (!caseFiltersActive) visibleRequirementIds.add(requirement.requirement_id)
    }
  }

  const nodes = layout.nodes.filter((node) => {
    if (node.data.kind === "case") return visibleCaseIds.has(node.id)
    if (node.data.kind === "scenario") return visibleScenarioIds.has(node.data.scenario.id)
    return visibleRequirementIds.has(node.data.requirement.requirement_id)
  })
  const visibleNodeIds = new Set(nodes.map((node) => node.id))
  const edges = layout.edges.filter(
    (edge) => visibleNodeIds.has(edge.source) && visibleNodeIds.has(edge.target),
  )

  return { nodes, edges }
}

function searchableText(node: DesignLayoutNode): string {
  switch (node.data.kind) {
    case "requirement":
      return node.data.requirement.title
    case "scenario":
      return `${node.data.scenario.name} ${node.data.scenario.description}`
    case "case":
      return node.data.case.title
  }
}

/** Case-insensitive match over requirement title, scenario name/description and case title. */
export function matchDesignNodes(layout: DesignLayout, query: string): DesignLayoutNode[] {
  const needle = query.trim().toLowerCase()
  if (needle.length === 0) return []
  return layout.nodes.filter((node) => searchableText(node).toLowerCase().includes(needle))
}

const LAYOUT_CACHE_LIMIT = 8
const layoutCache = new Map<string, DesignLayout>()

/**
 * Lightweight memoization keyed by `version_id:graph_hash`: an SSE refresh that
 * rebuilds the graph object with unchanged content reuses the same layout
 * instead of recomputing every position. The cache is bounded because the list
 * view can hop between versions.
 */
export function cachedLayoutDesignGraph(graph: DesignGraph): DesignLayout {
  const key = `${graph.version_id}:${graph.graph_hash}`
  const cached = layoutCache.get(key)
  if (cached !== undefined) return cached

  const computed = layoutDesignGraph(graph)
  layoutCache.set(key, computed)
  if (layoutCache.size > LAYOUT_CACHE_LIMIT) {
    const oldest = layoutCache.keys().next().value
    if (oldest !== undefined) layoutCache.delete(oldest)
  }
  return computed
}

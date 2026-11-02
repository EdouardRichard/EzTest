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

"use client"

import {
  BaseEdge,
  type Edge,
  EdgeLabelRenderer,
  type EdgeProps,
  getSmoothStepPath,
} from "@xyflow/react"
import { memo } from "react"

import {
  CHAIN_COLOR_STYLES,
  type ChainColorName,
  EDGE_KIND_TOKENS,
  getResultStatusToken,
  isResultStatus,
} from "../chain-tokens"
import type { ChainEdgeKindDTO } from "../chain-types"

/** Payload every chain edge carries; `targetStatus` is set for `case_result`. */
export type ChainEdgeData = {
  kind: ChainEdgeKindDTO | string
  /** Status of the edge's target node when that node is a result. */
  targetStatus?: string
}

/** The only custom edge type of the exploration chain. */
export type ChainFlowEdge = Edge<ChainEdgeData, "chain">

/**
 * Semantic colour of one edge kind.
 *
 * `EDGE_KIND_TOKENS` is the single source of truth (spec §5.4): indigo for
 * `requirement_case`, red for `result_issue`, violet for `case_issue` and amber
 * for `impact_case`. `case_result` borrows the target result's status token so a
 * passed edge is green and a failed one is red without opening the card; an
 * absent, empty or unknown status falls back to the neutral `case_result` token
 * (teal) instead of the generic unknown status colour, so the edge still reads
 * as "execution" rather than as an unknown result.
 */
export function getChainEdgeColorName(
  kind: ChainEdgeKindDTO | string,
  targetStatus?: string,
): ChainColorName {
  switch (kind) {
    case "requirement_case":
      return EDGE_KIND_TOKENS.requirement_case.color
    case "case_result": {
      if (targetStatus === undefined || targetStatus.trim() === "") {
        return EDGE_KIND_TOKENS.case_result.color
      }
      return isResultStatus(targetStatus)
        ? getResultStatusToken(targetStatus).color
        : EDGE_KIND_TOKENS.case_result.color
    }
    case "result_issue":
      return EDGE_KIND_TOKENS.result_issue.color
    case "case_issue":
      return EDGE_KIND_TOKENS.case_issue.color
    case "impact_case":
      return EDGE_KIND_TOKENS.impact_case.color
    default:
      return "slate"
  }
}

/** CSS stroke expression of one chain edge. */
export function getChainEdgeStroke(kind: ChainEdgeKindDTO | string, targetStatus?: string): string {
  return CHAIN_COLOR_STYLES[getChainEdgeColorName(kind, targetStatus)].stroke
}

/**
 * Hex colour for the SVG arrow marker.
 *
 * Marker ids are derived from their colour by React Flow, so the CSS custom
 * property expression of the palette cannot be used verbatim (its comma and
 * space would end up inside the marker id). The palette's hex fallback is
 * extracted instead of being duplicated here.
 */
export function getChainEdgeMarkerColor(
  kind: ChainEdgeKindDTO | string,
  targetStatus?: string,
): string {
  const stroke = getChainEdgeStroke(kind, targetStatus)
  return /#[0-9a-f]{3,8}/i.exec(stroke)?.[0] ?? stroke
}

/** Chinese label of one edge kind; unknown kinds render no label. */
export function getChainEdgeLabel(kind: ChainEdgeKindDTO | string): string {
  return EDGE_KIND_TOKENS[kind as ChainEdgeKindDTO]?.label ?? ""
}

/**
 * Custom edge of the exploration chain.
 *
 * Draws the semantic colour, the Chinese kind label and React Flow's arrow
 * marker. The semantic colour and width are passed through inline `style`
 * rather than SVG presentation attributes: `@xyflow/react/dist/style.css` sets
 * `.react-flow__edge-path { stroke: var(--xy-edge-stroke); stroke-width: … }`,
 * and CSS always beats presentation attributes — only inline style wins. The
 * running animation itself is owned by React Flow (`animated` on the edge) so
 * `prefers-reduced-motion` only has to decide whether to set it. `impact_case`
 * is always dashed amber.
 */
export const ChainEdge = memo(function ChainEdge({
  id,
  data,
  sourceX,
  sourceY,
  sourcePosition,
  targetX,
  targetY,
  targetPosition,
  markerEnd,
}: EdgeProps<ChainFlowEdge>) {
  const kind = data?.kind ?? ""
  const targetStatus = data?.targetStatus
  const stroke = getChainEdgeStroke(kind, targetStatus)
  const label = getChainEdgeLabel(kind)
  const testId = `chain-edge-${kind}`
  const [path, labelX, labelY] = getSmoothStepPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  return (
    <>
      <g data-testid={testId} data-edge-id={id} data-edge-kind={kind}>
        <BaseEdge
          id={id}
          path={path}
          markerEnd={markerEnd}
          style={{
            stroke,
            strokeWidth: 1.5,
            ...(kind === "impact_case" ? { strokeDasharray: "6 6" } : null),
          }}
        />
      </g>
      {label === "" ? null : (
        <EdgeLabelRenderer>
          <div
            data-testid={`${testId}-label`}
            data-edge-id={id}
            className="pointer-events-none rounded-full border border-border bg-card/95 px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground shadow-sm"
            style={{
              position: "absolute",
              transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            }}
          >
            {label}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
})

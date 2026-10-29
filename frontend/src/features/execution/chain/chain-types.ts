/**
 * DTOs of the P8 exploration-chain read model (spec §5.1,
 * `GET /api/versions/{versionId}/exploration-chain`).
 *
 * The read model is one snapshot of four layers — requirement → case → result →
 * issue. Node ids are the backend's `<kind>:<decimal id>` form; every decimal
 * id stays a string end-to-end (D29) and is never passed through `Number()`.
 * Fields are kind-specific: a node only carries the attributes of its layer, so
 * the optional members below are narrowed by the consumer's `kind` check.
 */

import type { CaseRunStatus, FailureClass } from "../types"

/** The four node layers of the exploration chain. */
export type ChainNodeKindDTO = "requirement" | "case" | "result" | "issue"

/** The semantic edge kinds the read model emits (spec §5.1). */
export type ChainEdgeKindDTO =
  | "requirement_case"
  | "case_result"
  | "result_issue"
  | "case_issue"
  | "impact_case"

/** One node of the read model. `id` is `<kind>:<decimal id>`. */
export type ChainSnapshotNode = {
  id: string
  kind: ChainNodeKindDTO
  title: string
  status: string

  // Requirement layer.
  priority?: string
  uncovered?: boolean
  caseCount?: number
  executedCount?: number
  failedCount?: number
  openQuestionCount?: number

  // Case layer.
  source?: string
  revalidation?: boolean
  /** Requirement ids reported by the backend; display only, never an edge. */
  requirementIds?: string[]
  stepCount?: number
  assertionCount?: number
  impacted?: boolean
  impactReason?: string

  // Result layer.
  /** The case-run id (same value as the result node's decimal id). */
  caseRunId?: string
  caseNodeId?: string
  attempt?: number
  durationMs?: number
  failureClass?: FailureClass | string
  evidenceCount?: number
  batchId?: string
  summary?: string

  // Issue layer.
  severity?: string
  category?: string
  confidence?: string
  codeRefCount?: number
}

/** One edge; `source`/`target` carry the node ids, `id` the domain edge id. */
export type ChainSnapshotEdge = {
  id: string
  source: string
  target: string
  kind: ChainEdgeKindDTO | string
}

/**
 * Real (pre-truncation) totals of the filtered graph. `byResultStatus` is a
 * string map because the backend returns all eight statuses, including
 * `pending` and `running` (not just the six terminal ones of the spec example).
 */
export type ChainSnapshotCounts = {
  requirements: number
  cases: number
  results: number
  issues: number
  byResultStatus: Record<string, number>
  byIssueSeverity: Record<string, number>
  uncoveredRequirements: number
  revalidationCases: number
  supplementCases: number
}

/** Which layers were cut by the node budget (spec §5.2). */
export type ChainSnapshotTruncated = {
  requirements: boolean
  cases: boolean
  results: boolean
  issues: boolean
}

/** The resolved batch context of the response (empty id when no batch exists). */
export type ChainSnapshotBatch = {
  id: string
  batchNo: number
  kind: string
  status: string
}

/** One exploration-chain read-model snapshot. */
export type ChainSnapshot = {
  versionId: string
  batch: ChainSnapshotBatch
  nodes: ChainSnapshotNode[]
  edges: ChainSnapshotEdge[]
  counts: ChainSnapshotCounts
  truncated: ChainSnapshotTruncated
  generatedAt: string
}

/** Result statuses accepted by the server-side `resultStatus` filter. */
export const CHAIN_RESULT_STATUS_FILTERS: readonly Exclude<CaseRunStatus, "pending" | "running">[] =
  ["passed", "failed", "blocked", "inconclusive", "skipped", "interrupted"]

/** Total node count described by the pre-truncation counts. */
export function chainSnapshotTotal(counts: ChainSnapshotCounts): number {
  return counts.requirements + counts.cases + counts.results + counts.issues
}

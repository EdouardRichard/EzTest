/**
 * Wire types for the P4 exploration-chain tab (spec §8, §9.4).
 *
 * Field names and enums are pinned by the frozen P4 OpenAPI contract
 * (`ChainView`, `ChainNodeView`, `ChainEdgeView`, `ChainReplayView`). IDs are
 * decimal strings, timestamps are RFC3339 UTC and the graph is read-only.
 */

export type ChainNodeKind =
  | "requirement"
  | "scenario"
  | "case_version"
  | "wave"
  | "case_run"
  | "issue"
  | "report"
  | "closure"

export type ChainEdgeKind =
  | "has_scenario"
  | "has_case"
  | "executed_by"
  | "contains"
  | "raised"
  | "reports"
  | "closed_by"
  | "included_in"
  | "merged_into"

export type ChainNodeView = {
  /** Stable `{kind}:{ref_id}` id (spec §8.2). */
  id: string
  kind: ChainNodeKind
  ref_id: string
  label: string
  status?: string | null
  version_sut_id?: string | null
  requirement_id?: string | null
  case_version_id?: string | null
  wave_id?: string | null
  case_run_id?: string | null
  issue_id?: string | null
  report_id?: string | null
  created_at: string
  meta?: Record<string, unknown>
}

export type ChainEdgeView = {
  from: string
  to: string
  kind: ChainEdgeKind
}

export type ChainFiltersView = {
  sut_id?: string | null
  page_id?: string | null
  wave_id?: string | null
  status?: string | null
  result?: string | null
  event_from?: number | null
  event_to?: number | null
}

export type ChainCounts = {
  requirements: number
  scenarios: number
  cases: number
  waves: number
  case_runs: number
  issues: number
  reports: number
  evidence: number
}

export type ChainTruncationReason = "" | "node_budget" | "edge_budget" | "requirement_budget"

export type ChainTruncation = {
  max_nodes: number
  max_edges: number
  max_requirements: number
  reason: ChainTruncationReason
}

export type ChainReplayMode = "live" | "wave" | "range"

export type ChainReplay = {
  mode: ChainReplayMode
  wave_id?: string | null
  event_from?: number | null
  event_to?: number | null
  /** Historical replay is approximate; P4 never claims an as-of snapshot. */
  approximate: boolean
}

/** `ChainView`: one request-level read model (spec §8.3). */
export type ChainView = {
  version_id: string
  generated_at: string
  snapshot_cursor: number
  filters: ChainFiltersView
  counts: ChainCounts
  nodes: ChainNodeView[]
  edges: ChainEdgeView[]
  truncated: boolean
  truncation: ChainTruncation
  replay: ChainReplay
}

/* --------------------------------- requests -------------------------------- */

export type ChainQuery = {
  /** `version_sut_id` (decimal string). */
  sutId?: string
  pageId?: string
  waveId?: string
  status?: string
  result?: string
  /** Forced node retention for `?node=` deep links. */
  focusNode?: string
  eventFrom?: number
  eventTo?: number
  limit?: number
}

export type ChainEventListOptions = {
  limit?: number
}

/* ------------------------------- UI metadata ------------------------------- */

export const CHAIN_NODE_KIND_LABELS: Record<ChainNodeKind, string> = {
  requirement: "需求",
  scenario: "场景",
  case_version: "用例",
  wave: "波次",
  case_run: "运行",
  issue: "问题",
  report: "报告",
  closure: "闭环",
}

export const CHAIN_EDGE_KIND_LABELS: Record<ChainEdgeKind, string> = {
  has_scenario: "场景",
  has_case: "用例",
  executed_by: "执行",
  contains: "包含",
  raised: "产生",
  reports: "报告",
  closed_by: "闭环",
  included_in: "纳入",
  merged_into: "合并至",
}

export const CHAIN_TRUNCATION_REASON_LABELS: Record<Exclude<ChainTruncationReason, "">, string> = {
  node_budget: "节点预算",
  edge_budget: "边预算",
  requirement_budget: "需求预算",
}

/** Semantic border/dot classes; values are design tokens, not mock hex colors. */
export const CHAIN_NODE_TONE_CLASSES: Record<ChainNodeKind, string> = {
  requirement: "border-primary/40 bg-card",
  scenario: "border-info/40 bg-card",
  case_version: "border-info/40 bg-card",
  wave: "border-warning/40 bg-card",
  case_run: "border-success/40 bg-card",
  issue: "border-destructive/40 bg-card",
  report: "border-border bg-muted/40",
  closure: "border-border bg-card",
}

export const CHAIN_NODE_DOT_CLASSES: Record<ChainNodeKind, string> = {
  requirement: "bg-primary",
  scenario: "bg-info",
  case_version: "bg-info",
  wave: "bg-warning",
  case_run: "bg-success",
  issue: "bg-destructive",
  report: "bg-muted-foreground",
  closure: "bg-foreground",
}

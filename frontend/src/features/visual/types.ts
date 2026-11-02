import type { BadgeTone } from "@/components/ui/Badge"

/**
 * Wire types for the P5 visual assertion feature.
 *
 * Field names and enum values are pinned by the frozen P5 OpenAPI contract
 * (`0.2.0-p5`): IDs are decimal strings, timestamps RFC3339 UTC, visual list
 * endpoints use `{data, next_cursor, has_more}` and detail endpoints use
 * `{data: ...}`. Image bytes never appear here; only evidence IDs do.
 */

/* ---------------------------------- enums ---------------------------------- */

/** Four-state visual classification (spec §7.3). */
export type VisualClassification = "passed" | "failed" | "inconclusive" | "blocked"

export type VisualAssertionStatus = "active" | "archived"

export type VisualBaselineStatus = "approved" | "superseded"

/* ---------------------------------- config --------------------------------- */

/** Normalized crop region `{x,y,width,height}` in 0–1 image coordinates. */
export type VisualRegion = {
  x: number
  y: number
  width: number
  height: number
}

export type VisualViewport = {
  width: number
  height: number
}

export type VisualWaitPolicy = Record<string, unknown>

/** `VisualAssertionUpsertRequest` (contract `PUT .../assertions/{caseAssertionId}`). */
export type VisualConfigInput = {
  versionSutId?: string
  selector?: string
  region?: VisualRegion | null
  threshold?: number
  viewport?: VisualViewport
  deviceScaleFactor?: number
  waitPolicy?: VisualWaitPolicy | null
}

/** Editable configuration snapshot shared by the editor and its callers. */
export type VisualConfigValues = {
  selector: string
  region: VisualRegion | null
  threshold: number
  viewportWidth: number
  viewportHeight: number
  deviceScaleFactor: number
  waitPolicy: VisualWaitPolicy | null
}

/* --------------------------------- baseline -------------------------------- */

/** `VisualBaselineSummary` embedded in `VisualAssertionView.baseline`. */
export type VisualBaselineSummary = {
  id: string
  sha256: string
  width: number
  height: number
  config_revision: number
  environment_fingerprint: string
  approved_at: string
}

/** `VisualBaselineView`. */
export type VisualBaseline = {
  id: string
  visual_assertion_id: string
  case_version_id: string
  version_sut_id: string
  evidence_id: string
  sha256: string
  size_bytes: number
  width: number
  height: number
  viewport_width: number
  viewport_height: number
  device_scale_factor: number
  config_revision: number
  environment_fingerprint: string
  status: VisualBaselineStatus
  approved_by: string
  approved_at: string
  superseded_at: string | null
  created_at: string
}

/* -------------------------------- assertion -------------------------------- */

/**
 * `VisualAssertionView`. `case_assertion_id` is the P2 design assertion id used
 * by the PUT path; `baseline` is the active (`approved`) baseline summary.
 */
export type VisualAssertion = {
  id: string
  version_id: string
  case_version_id: string
  case_assertion_id: string
  step_seq: number
  assertion_seq: number
  version_sut_id: string
  selector: string
  region: VisualRegion | Record<string, unknown> | null
  threshold: number
  viewport_width: number
  viewport_height: number
  device_scale_factor: number
  wait_policy: VisualWaitPolicy | Record<string, unknown>
  config_revision: number
  status: VisualAssertionStatus
  baseline?: VisualBaselineSummary | null
  created_at: string
  updated_at: string
}

/* ----------------------------------- diff ---------------------------------- */

/** `VisualMismatchBox` (32×32 grid, ≤50 per diff). */
export type VisualMismatchBox = {
  x: number
  y: number
  width: number
  height: number
}

export type VisualEnvironmentRef = {
  viewport_width: number
  viewport_height: number
  device_scale_factor: number
  browser?: string | null
  version?: string | null
}

/** `VisualDiffView`; no image bytes, only evidence ids. */
export type VisualDiff = {
  id: string
  visual_assertion_id: string
  case_run_id: string
  assertion_result_id: string
  baseline_id?: string | null
  actual_evidence_id: string
  diff_evidence_id?: string | null
  diff_ratio: number
  threshold: number
  classification: VisualClassification
  reason: string
  environment_match: boolean
  width: number
  height: number
  created_at: string
}

/** `VisualDiffDetailView`. */
export type VisualDiffDetail = VisualDiff & {
  mismatch_boxes: VisualMismatchBox[]
  environment?: VisualEnvironmentRef | null
}

/* --------------------------------- overview -------------------------------- */

export type VisualInconclusiveReason = {
  reason: string
  count: number
}

export type VisualDiffLatency = {
  p50_ms: number
  p95_ms: number
}

/** `VisualOverviewView` (per version). */
export type VisualOverview = {
  version_id: string
  assertion_total: number
  baseline_covered: number
  baseline_coverage_ratio: number | null
  passed: number
  failed: number
  inconclusive: number
  blocked: number
  pass_ratio: number | null
  inconclusive_reasons: VisualInconclusiveReason[]
  diff_latency_ms: VisualDiffLatency | null
  recent_failures: VisualDiff[]
}

/* --------------------------------- metrics --------------------------------- */

export type VisualMetricTotals = {
  assertions: number
  baselines: number
  diffs: number
  passed: number
  failed: number
  inconclusive: number
  blocked: number
}

export type VisualMetricRatios = {
  baseline_coverage?: number | null
  pass_rate?: number | null
  inconclusive_rate?: number | null
}

/** `VisualMetricsView` (`GET /api/quality/visual-metrics`). */
export type VisualMetrics = {
  scope: { version_id: string | null }
  totals: VisualMetricTotals
  ratios: VisualMetricRatios
  diff_latency_ms: VisualDiffLatency | null
  top_inconclusive_reasons: VisualInconclusiveReason[]
  generated_at: string
}

/* --------------------------------- requests -------------------------------- */

export type VisualAssertionListOptions = {
  caseVersionId?: string
  versionSutId?: string
}

export type VisualDiffListOptions = {
  caseRunId?: string
  caseVersionId?: string
  classification?: VisualClassification
  cursor?: string
  limit?: number
}

/* ------------------------------- UI metadata ------------------------------- */

export const VISUAL_CLASSIFICATION_LABEL: Record<VisualClassification, string> = {
  passed: "通过",
  failed: "失败",
  inconclusive: "未定",
  blocked: "阻塞",
}

export const VISUAL_CLASSIFICATION_TONE: Record<VisualClassification, BadgeTone> = {
  passed: "success",
  failed: "danger",
  inconclusive: "neutral",
  blocked: "warning",
}

/** Reason codes from spec §7.3 rendered as Chinese hints; unknown codes stay raw. */
export const VISUAL_REASON_LABEL: Record<string, string> = {
  visual_match: "像素匹配",
  visual_mismatch: "像素差异超过阈值",
  missing_visual_baseline: "缺失视觉基线",
  visual_environment_mismatch: "环境指纹不一致",
  visual_config_changed: "视觉配置已变更",
  visual_baseline_invalid: "基线证据无效",
  visual_actual_invalid: "实际截图无效",
  visual_assertion_not_run: "视觉断言未执行",
}

export function visualReasonLabel(reason: string): string {
  if (reason.length === 0) return "—"
  return VISUAL_REASON_LABEL[reason] ?? reason
}

/** Percent formatting for ratios in 0–1; `null` renders as a dash. */
export function formatRatio(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—"
  return `${(value * 100).toFixed(2).replace(/\.?0+$/, "")}%`
}

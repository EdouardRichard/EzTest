import type { BadgeTone } from "@/components/ui/Badge"

/**
 * Wire types for the P6 quality centre.
 *
 * Names/fields are pinned by the frozen P6 OpenAPI contract
 * (`docs/version-V0.2/contracts/openapi.yaml`): IDs are decimal strings,
 * timestamps RFC3339 UTC, and every metric value is derived by the server. The
 * page never invents a default metric value: `available=false` / `value=null`
 * means "unavailable", never 0.
 */

/* ---------------------------------- enums ---------------------------------- */

export type EvalMode = "real_llm"

export type EvalStatus = "running" | "passed" | "failed" | "error"

export type EvalMetricReason = "ok" | "below_threshold" | "above_threshold" | "baseline_regression"

export type OperationalMetricName =
  | "requirement_closure_rate"
  | "case_confirmation_rate"
  | "execution_pass_rate"
  | "issue_validity_rate"
  | "evidence_completeness_rate"
  | "context_compaction_count"
  | "task_failure_retry_rate"

/* ------------------------------- operational ------------------------------- */

/** `OperationalMetric` of GET /api/quality/overview. */
export type OperationalMetric = {
  name: OperationalMetricName | string
  /** null when the denominator is zero; the UI shows "不可用". */
  value: number | null
  numerator: number
  denominator: number
  available: boolean
  /** Server-provided definition rendered as the tooltip. */
  definition: string
}

/** `EvalSummary` embedded in the overview. */
export type EvalSummary = {
  id: string
  mode: EvalMode
  status: EvalStatus
  created_at: string
  metrics_count: number
  metrics_passed: number
}

/** `QualityOverviewView` of GET /api/quality/overview. */
export type QualityOverview = {
  scope: { version_id: string | null }
  generated_at: string
  operational: OperationalMetric[]
  latest_eval: EvalSummary | null
}

/* -------------------------------- evaluation ------------------------------- */

export type EvalMetricDetail = {
  numerator: number
  denominator: number
  threshold_passed: boolean
  baseline?: number | null
  regressed: boolean
  reason: EvalMetricReason
  zero_denominator?: boolean
}

/** `EvalMetricView` of one eval run. */
export type EvalMetric = {
  name: string
  value: number
  threshold: number
  passed: boolean
  detail: EvalMetricDetail
}

/** `EvalRunView` (admin-only list/detail). */
export type EvalRun = {
  id: string
  mode: EvalMode
  corpus_version: string
  model: string
  status: EvalStatus
  summary: Record<string, unknown>
  metrics: EvalMetric[]
  started_at: string | null
  finished_at: string | null
  created_at: string
}

/* ------------------------------- UI metadata ------------------------------- */

export const OPERATIONAL_METRIC_LABEL: Record<string, string> = {
  requirement_closure_rate: "需求闭环率",
  case_confirmation_rate: "用例确认率",
  execution_pass_rate: "执行通过率",
  issue_validity_rate: "问题有效率",
  evidence_completeness_rate: "证据完整率",
  context_compaction_count: "上下文压缩次数",
  task_failure_retry_rate: "任务失败/重试率",
}

export const EVAL_METRIC_LABEL: Record<string, string> = {
  design_coverage: "设计覆盖率",
  impact_recall: "影响召回率",
  impact_precision: "影响精确率",
  classification_accuracy: "分类准确率",
  issue_false_positive_rate: "问题误报率",
  supplement_hit_rate: "补充命中率",
  visual_classification_accuracy: "视觉分类准确率",
}

export const EVAL_REASON_LABEL: Record<string, string> = {
  ok: "通过",
  below_threshold: "低于阈值",
  above_threshold: "高于阈值",
  baseline_regression: "基线回归",
}

export const EVAL_STATUS_LABEL: Record<EvalStatus, string> = {
  running: "运行中",
  passed: "通过",
  failed: "失败",
  error: "错误",
}

export const EVAL_STATUS_TONE: Record<EvalStatus, BadgeTone> = {
  running: "info",
  passed: "success",
  failed: "danger",
  error: "warning",
}

/** Percent formatting for 0–1 ratios; compact but with two decimals when needed. */
export function formatPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return "—"
  return `${(value * 100).toFixed(2).replace(/\.?0+$/, "")}%`
}

/** A metric is unavailable when the server could not compute a denominator. */
export function metricUnavailable(metric: OperationalMetric): boolean {
  return !metric.available || metric.value === null
}

/**
 * Renders one operational metric. Counts stay integers, ratios use percent and
 * an unavailable denominator renders "不可用" (never a fabricated 0%).
 */
export function formatOperationalValue(metric: OperationalMetric): string {
  if (metricUnavailable(metric)) return "不可用"
  const value = metric.value ?? 0
  if (metric.name === "context_compaction_count") return String(Math.round(value))
  return formatPercent(value)
}

/** Badge tone of one derived eval metric. */
export function evalMetricTone(metric: EvalMetric): BadgeTone {
  return metric.passed ? "success" : "danger"
}

/** Human reason of one derived eval metric. */
export function evalMetricReason(metric: EvalMetric): string {
  return EVAL_REASON_LABEL[metric.detail.reason] ?? metric.detail.reason
}

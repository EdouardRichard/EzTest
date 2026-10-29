/** Wire types of the P8 evaluation endpoints (design spec §9.1). */

/** One evaluated KPI; ids stay decimal strings end-to-end (D29). */
export type EvalMetric = {
  name: string
  value: number
  threshold: number
  passed: boolean
  detail: Record<string, unknown>
}

/** One persisted evaluation run returned by GET /api/quality/eval-runs. */
export type EvalRun = {
  id: string
  mode: "deterministic" | "real_llm" | string
  corpusVersion: string
  model: string
  status: "running" | "passed" | "failed" | "error" | string
  summary: Record<string, unknown>
  metrics: EvalMetric[]
  startedAt: string
  finishedAt: string | null
}

/** Human labels of the five spec §9.1 KPI groups (impact has two values). */
export const METRIC_LABELS: Record<string, string> = {
  design_coverage: "设计覆盖率",
  impact_recall: "影响命中率",
  impact_precision: "影响精确率",
  classification_accuracy: "失败分类准确率",
  issue_false_positive_rate: "问题误报率",
  supplement_hit_rate: "补充命中率",
}

/** Whether a metric is a "stay below" KPI (the issue false-positive rate). */
export function metricIsUpperBound(name: string): boolean {
  return name === "issue_false_positive_rate"
}

/** Reads the stable numerator/denominator pair of a metric detail document. */
export function metricCounts(detail: Record<string, unknown>): { numerator: number; denominator: number } | null {
  const numerator = detail.numerator
  const denominator = detail.denominator
  if (typeof numerator !== "number" || typeof denominator !== "number") return null
  return { numerator, denominator }
}

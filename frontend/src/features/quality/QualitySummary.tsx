"use client"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import {
  type EvalRun,
  METRIC_LABELS,
  metricBaseline,
  metricCounts,
  metricIsUpperBound,
} from "./types"

/** Maps a run status to a badge variant and its Chinese label. */
export function runStatusBadge(status: string): { variant: StatusBadgeVariant; label: string } {
  switch (status) {
    case "passed":
      return { variant: "success", label: "通过" }
    case "failed":
      return { variant: "danger", label: "未通过" }
    case "error":
      return { variant: "warning", label: "执行错误" }
    case "running":
      return { variant: "info", label: "运行中" }
    default:
      return { variant: "neutral", label: status }
  }
}

/** Maps the evaluation track to its display label. */
export function modeLabel(mode: string): string {
  if (mode === "deterministic") return "确定性门禁"
  if (mode === "real_llm") return "真实 LLM 报告"
  return mode
}

/** Formats one ratio as a one-decimal percentage. */
export function formatMetricPercent(value: number): string {
  if (!Number.isFinite(value)) return "—"
  return `${(value * 100).toFixed(1)}%`
}

/** Formats one RFC 3339 timestamp for the local dashboard. */
export function formatRunTime(value: string | null): string {
  if (value === null || value === "") return "—"
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  })
}

/**
 * The header verdict. "全部指标达标" is only true for a passed run that actually
 * produced metrics; an error run with no metrics must never look like a pass.
 */
export function runVerdictText(run: EvalRun): string {
  if (run.metrics.length === 0) return "无指标"
  const failedCount = run.metrics.filter((metric) => !metric.passed).length
  if (run.status === "passed" && failedCount === 0) return "全部指标达标"
  if (failedCount === 0) return "未通过"
  return `${failedCount} 项指标未达标`
}

export type QualitySummaryProps = {
  /** The run to render; the dashboard renders the newest run here. */
  run: EvalRun
}

/**
 * Latest-run card of the quality dashboard (spec §9.1): mode, corpus, model,
 * five KPI groups with their value/threshold/verdict and the history detail
 * counts. Values are percentages so a threshold is readable at a glance; the
 * verdict text never depends on colour alone.
 */
export function QualitySummary({ run }: QualitySummaryProps) {
  const status = runStatusBadge(run.status)
  return (
    <section
      data-testid="quality-summary"
      aria-labelledby="quality-summary-title"
      className="space-y-4 rounded-2xl border border-border bg-card p-4"
    >
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="quality-summary-title" className="text-lg font-semibold tracking-tight">
            最近评测 <span data-testid="quality-run-id">#{run.id}</span>
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            {modeLabel(run.mode)} · 语料 {run.corpusVersion} · 模型{" "}
            {run.model === "" ? "—" : run.model}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            完成于 {formatRunTime(run.finishedAt ?? run.startedAt)}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge variant={status.variant}>{status.label}</StatusBadge>
          <span data-testid="quality-verdict" className="text-xs text-muted-foreground">
            {runVerdictText(run)}
          </span>
        </div>
      </header>

      {run.metrics.length === 0 ? (
        <p
          data-testid="quality-no-metrics"
          className="rounded-xl border border-dashed px-3 py-4 text-sm text-muted-foreground"
        >
          本次运行没有指标数据（错误运行不伪造指标）。
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[36rem] border-collapse text-sm">
            <caption className="sr-only">五项评测指标：数值、阈值、基线、通过状态与计数</caption>
            <thead>
              <tr className="border-b border-border text-left text-xs uppercase tracking-wide text-muted-foreground">
                <th scope="col" className="px-2 py-2 font-medium">
                  指标
                </th>
                <th scope="col" className="px-2 py-2 font-medium">
                  数值
                </th>
                <th scope="col" className="px-2 py-2 font-medium">
                  阈值
                </th>
                <th scope="col" className="px-2 py-2 font-medium">
                  基线
                </th>
                <th scope="col" className="px-2 py-2 font-medium">
                  判定
                </th>
                <th scope="col" className="px-2 py-2 font-medium">
                  计数
                </th>
              </tr>
            </thead>
            <tbody>
              {run.metrics.map((metric) => {
                const counts = metricCounts(metric.detail)
                const baseline = metricBaseline(metric.detail)
                const regressed = metric.detail.reason === "baseline_regression"
                const verdict = runStatusBadge(metric.passed ? "passed" : "failed")
                return (
                  <tr
                    key={metric.name}
                    data-testid={`quality-metric-${metric.name}`}
                    className="border-b border-border/60 last:border-0"
                  >
                    <th scope="row" className="px-2 py-2 text-left font-medium">
                      {METRIC_LABELS[metric.name] ?? metric.name}
                    </th>
                    <td
                      data-testid={`quality-metric-value-${metric.name}`}
                      className="px-2 py-2 tabular-nums"
                    >
                      {formatMetricPercent(metric.value)}
                    </td>
                    <td className="px-2 py-2 tabular-nums text-muted-foreground">
                      <span aria-hidden="true">{metricIsUpperBound(metric.name) ? "≤" : "≥"} </span>
                      {formatMetricPercent(metric.threshold)}
                    </td>
                    <td
                      data-testid={`quality-metric-baseline-${metric.name}`}
                      className="px-2 py-2 tabular-nums text-muted-foreground"
                    >
                      {baseline === null ? "—" : formatMetricPercent(baseline)}
                    </td>
                    <td className="px-2 py-2">
                      <StatusBadge variant={verdict.variant}>{verdict.label}</StatusBadge>
                      {regressed ? (
                        <span
                          data-testid={`quality-metric-regression-${metric.name}`}
                          className="ml-2 text-[11px] font-medium text-amber-700 dark:text-amber-300"
                        >
                          低于基线
                        </span>
                      ) : null}
                    </td>
                    <td className="px-2 py-2 tabular-nums text-muted-foreground">
                      {counts === null ? "—" : `${counts.numerator}/${counts.denominator}`}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

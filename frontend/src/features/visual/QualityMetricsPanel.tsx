"use client"

import { useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { getVisualMetrics } from "./api"
import {
  formatRatio,
  VISUAL_CLASSIFICATION_LABEL,
  type VisualMetrics,
  visualReasonLabel,
} from "./types"

export type QualityMetricsPanelProps = {
  /** Version scope; omitted means the global quality aggregate. */
  versionId?: string
}

function MetricCard({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-lg border border-border bg-card px-3 py-2.5">
      <p className="text-[11px] text-muted-foreground">{label}</p>
      <p className="mt-0.5 text-sm font-semibold text-foreground">{value}</p>
      {hint !== undefined ? (
        <p className="mt-0.5 text-[10px] text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  )
}

/**
 * Minimal visual quality panel (spec §7.5/§8.2): baseline coverage, four-state
 * distribution, pass/inconclusive rates, diff latency and the top inconclusive
 * reasons. It shows only metrics the P5 API returns; P6 metrics are absent.
 */
export function QualityMetricsPanel({ versionId }: QualityMetricsPanelProps) {
  const [metrics, setMetrics] = useState<VisualMetrics | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken re-runs the explicit refetch
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void getVisualMetrics(versionId)
      .then((next) => {
        if (!cancelled) setMetrics(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setMetrics(null)
          setError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, reloadToken])

  const retry = useCallback(() => setReloadToken((token) => token + 1), [])

  if (loading && metrics === null) return <LoadingCard label="正在加载视觉指标…" />
  if (error !== null) {
    return <ErrorState error={error} title="视觉指标加载失败" onRetry={retry} />
  }
  if (metrics === null || metrics.totals.assertions === 0) {
    return (
      <EmptyState
        title="暂无视觉数据"
        description="确认带视觉断言的用例并执行后，这里会展示基线覆盖率、四态分布与差异延迟。"
      />
    )
  }

  const totals = metrics.totals
  const ratios = metrics.ratios
  const latency = metrics.diff_latency_ms

  return (
    <div data-testid="quality-metrics-panel" className="space-y-3">
      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
        <MetricCard label="断言总数" value={String(totals.assertions)} />
        <MetricCard label="已批准基线" value={String(totals.baselines)} />
        <MetricCard label="基线覆盖率" value={formatRatio(ratios.baseline_coverage)} />
        <MetricCard label="通过率" value={formatRatio(ratios.pass_rate)} />
        <MetricCard label="未定率" value={formatRatio(ratios.inconclusive_rate)} />
        <MetricCard label="视觉比对" value={String(totals.diffs)} />
      </dl>

      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        <MetricCard label={VISUAL_CLASSIFICATION_LABEL.passed} value={String(totals.passed)} />
        <MetricCard label={VISUAL_CLASSIFICATION_LABEL.failed} value={String(totals.failed)} />
        <MetricCard
          label={VISUAL_CLASSIFICATION_LABEL.inconclusive}
          value={String(totals.inconclusive)}
        />
        <MetricCard label={VISUAL_CLASSIFICATION_LABEL.blocked} value={String(totals.blocked)} />
      </dl>

      <div className="flex flex-wrap items-center gap-3 text-[11px] text-muted-foreground">
        <span>
          {latency !== null ? `p50 ${latency.p50_ms} ms · p95 ${latency.p95_ms} ms` : "延迟 —"}
        </span>
        <span>生成于 {formatDateTime(metrics.generated_at)}</span>
      </div>

      {metrics.top_inconclusive_reasons.length > 0 ? (
        <div>
          <h3 className="text-xs font-semibold text-foreground">未定原因</h3>
          <ul className="mt-1 space-y-1">
            {metrics.top_inconclusive_reasons.map((item) => (
              <li
                key={item.reason}
                className="flex items-center justify-between rounded-md border border-border px-2.5 py-1.5 text-[11px]"
              >
                <span className="text-foreground">{visualReasonLabel(item.reason)}</span>
                <span className="text-muted-foreground">{item.count}</span>
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  )
}

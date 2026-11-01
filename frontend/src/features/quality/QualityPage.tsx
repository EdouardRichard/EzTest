"use client"

import { useRouter } from "next/navigation"
import { useCallback, useEffect, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { useIsAdmin } from "@/features/auth/useIsAdmin"
import { listVersions } from "@/features/versions/api"
import type { VersionSummary } from "@/features/versions/types"
import { QualityMetricsPanel } from "@/features/visual/QualityMetricsPanel"
import { formatDateTime } from "@/lib/format"

import { getQualityOverview, listEvalRuns } from "./api"
import {
  EVAL_METRIC_LABEL,
  EVAL_STATUS_LABEL,
  EVAL_STATUS_TONE,
  type EvalMetric,
  type EvalRun,
  evalMetricReason,
  evalMetricTone,
  formatOperationalValue,
  formatPercent,
  metricUnavailable,
  OPERATIONAL_METRIC_LABEL,
  type OperationalMetric,
  type QualityOverview,
} from "./types"

export type QualityPageProps = {
  /** Deep link `?version=` from the quality route. */
  initialVersionId?: string
}

const EVAL_LIST_LIMIT = 20

/**
 * P6 quality centre (spec §5.5/P6-D-02).
 *
 * Every logged-in user sees the seven server-derived operational metrics and
 * the P5 visual quality panel. The evaluation board (runs, thresholds,
 * baselines, history) is admin-only: normal users never issue the admin
 * requests. Unavailable denominators render "不可用", never a fabricated zero.
 */
export function QualityPage({ initialVersionId }: QualityPageProps) {
  const router = useRouter()
  const isAdmin = useIsAdmin()

  const [versions, setVersions] = useState<VersionSummary[]>([])
  const [versionsError, setVersionsError] = useState<unknown>(null)
  const [versionId, setVersionId] = useState(initialVersionId ?? "")

  const [overview, setOverview] = useState<QualityOverview | null>(null)
  const [overviewLoading, setOverviewLoading] = useState(true)
  const [overviewError, setOverviewError] = useState<unknown>(null)

  const [evalRuns, setEvalRuns] = useState<EvalRun[] | null>(null)
  const [evalLoading, setEvalLoading] = useState(false)
  const [evalError, setEvalError] = useState<unknown>(null)

  const [overviewReloadToken, setOverviewReloadToken] = useState(0)
  const [evalReloadToken, setEvalReloadToken] = useState(0)

  useEffect(() => {
    let cancelled = false
    setVersionsError(null)
    void listVersions()
      .then((items) => {
        if (!cancelled) setVersions(items)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setVersionsError(cause)
      })
    return () => {
      cancelled = true
    }
  }, [])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload token re-runs the explicit refetch
  useEffect(() => {
    let cancelled = false
    setOverviewLoading(true)
    setOverviewError(null)
    void getQualityOverview(versionId.length > 0 ? versionId : undefined)
      .then((next) => {
        if (!cancelled) setOverview(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setOverview(null)
          setOverviewError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setOverviewLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [versionId, overviewReloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload token re-runs the explicit refetch
  useEffect(() => {
    if (!isAdmin) {
      setEvalRuns(null)
      setEvalError(null)
      setEvalLoading(false)
      return
    }
    let cancelled = false
    setEvalLoading(true)
    setEvalError(null)
    void listEvalRuns(EVAL_LIST_LIMIT)
      .then((runs) => {
        if (!cancelled) setEvalRuns(runs)
      })
      .catch((cause: unknown) => {
        if (!cancelled) {
          setEvalRuns(null)
          setEvalError(cause)
        }
      })
      .finally(() => {
        if (!cancelled) setEvalLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [isAdmin, evalReloadToken])

  const refresh = useCallback(() => {
    setOverviewReloadToken((token) => token + 1)
    if (isAdmin) setEvalReloadToken((token) => token + 1)
  }, [isAdmin])

  function changeVersion(next: string): void {
    setVersionId(next)
    router.replace(next.length > 0 ? `/quality?version=${encodeURIComponent(next)}` : "/quality")
  }

  return (
    <div data-testid="quality-page" className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            全局工作区
          </p>
          <div className="mt-1 flex items-center gap-2">
            <h1 className="text-xl font-semibold text-foreground">质量</h1>
            {isAdmin ? <Badge tone="info">管理员</Badge> : null}
          </div>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            运营指标由服务端按只读口径派生；视觉质量沿用 P5。真实 LLM
            评测看板仅管理员可见，阈值与基线是报告轨，不阻断阶段验收。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <span>版本</span>
            <Select
              aria-label="版本"
              value={versionId}
              onChange={(event) => changeVersion(event.target.value)}
              className="h-8 w-52 text-xs"
            >
              <option value="">全部版本</option>
              {versions.map((version) => (
                <option key={version.id} value={version.id}>
                  #{version.id} · {version.title}
                </option>
              ))}
            </Select>
          </div>
          <Button variant="secondary" size="sm" onClick={refresh}>
            刷新
          </Button>
        </div>
      </header>

      {versionsError !== null ? <InlineError error={versionsError} /> : null}

      <section aria-label="运营指标" className="surface-card space-y-3 p-4">
        <header>
          <h2 className="text-sm font-semibold text-foreground">运营指标</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            7 项指标由服务端聚合；分母为 0 时显示“不可用”，不展示 0 假数据。
          </p>
        </header>
        {overviewLoading && overview === null ? (
          <LoadingCard label="正在加载运营指标…" />
        ) : overviewError !== null ? (
          <ErrorState error={overviewError} title="质量概览加载失败" onRetry={refresh} />
        ) : overview !== null ? (
          <>
            <dl
              data-testid="quality-operational-metrics"
              className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4"
            >
              {overview.operational.map((metric) => (
                <OperationalMetricCard key={metric.name} metric={metric} />
              ))}
            </dl>
            <p className="text-[11px] text-muted-foreground">
              生成于 {formatDateTime(overview.generated_at)}
            </p>
          </>
        ) : null}
      </section>

      <section aria-label="视觉质量指标" className="surface-card space-y-3 p-4">
        <header>
          <h2 className="text-sm font-semibold text-foreground">视觉断言质量</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            P5 视觉指标语义不变；未执行或未配置视觉断言时保持空态。
          </p>
        </header>
        <QualityMetricsPanel versionId={versionId.length > 0 ? versionId : undefined} />
      </section>

      <section
        data-testid="quality-eval-board"
        aria-label="评测看板"
        className="surface-card space-y-3 p-4"
      >
        <header className="flex flex-wrap items-start justify-between gap-2">
          <div>
            <h2 className="text-sm font-semibold text-foreground">评测看板</h2>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              真实 LLM 评测 run、6+1 指标、阈值与基线；服务端派生，客户端 verdict 不生效。
            </p>
          </div>
          {isAdmin ? <Badge tone="neutral">admin-only</Badge> : null}
        </header>

        {!isAdmin ? (
          <div
            data-testid="quality-eval-admin-only"
            className="rounded-lg border border-dashed border-border bg-muted/30 px-3 py-4"
          >
            <p className="text-sm font-medium text-foreground">评测看板仅管理员可见</p>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              普通用户可查看运营与视觉质量；评测 run 列表与样本不返回。
            </p>
          </div>
        ) : evalLoading && evalRuns === null ? (
          <LoadingCard label="正在加载评测 run…" />
        ) : evalError !== null ? (
          <ErrorState error={evalError} title="评测看板加载失败" onRetry={refresh} />
        ) : evalRuns === null || evalRuns.length === 0 ? (
          <div data-testid="quality-eval-empty">
            <EmptyState
              title="尚未运行评测"
              description="管理员执行真实 LLM 评测 runner 后，这里会展示指标、阈值、基线与历史。"
            />
          </div>
        ) : (
          <EvalBoardContent
            runs={evalRuns}
            latestSummary={overview?.latest_eval ?? null}
            onRefresh={refresh}
          />
        )}
      </section>
    </div>
  )
}

function OperationalMetricCard({ metric }: { metric: OperationalMetric }) {
  const label = OPERATIONAL_METRIC_LABEL[metric.name] ?? metric.name
  const unavailable = metricUnavailable(metric)
  return (
    <div
      data-testid={`quality-operational-${metric.name}`}
      title={metric.definition}
      className="rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <dt className="text-[11px] text-muted-foreground">{label}</dt>
      <dd className="mt-0.5 text-sm font-semibold text-foreground">
        {formatOperationalValue(metric)}
      </dd>
      {!unavailable && metric.name !== "context_compaction_count" && metric.denominator > 0 ? (
        <p className="mt-0.5 text-[10px] text-muted-foreground">
          {metric.numerator}/{metric.denominator}
        </p>
      ) : null}
    </div>
  )
}

function EvalBoardContent({
  runs,
  latestSummary,
  onRefresh,
}: {
  runs: EvalRun[]
  latestSummary: QualityOverview["latest_eval"]
  onRefresh: () => void
}) {
  const latest = runs[0]
  const baselineMetrics = latest.metrics.filter(
    (metric) => metric.detail.baseline !== null && metric.detail.baseline !== undefined,
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          {latestSummary !== null ? (
            <span data-testid="quality-eval-latest" className="flex items-center gap-2">
              <span className="font-semibold text-foreground">最新评测 #{latestSummary.id}</span>
              <Badge tone={EVAL_STATUS_TONE[latestSummary.status]}>
                {EVAL_STATUS_LABEL[latestSummary.status]}
              </Badge>
              <span>
                {latestSummary.metrics_passed}/{latestSummary.metrics_count} 指标通过 ·{" "}
                {formatDateTime(latestSummary.created_at)}
              </span>
            </span>
          ) : null}
        </div>
        <Button variant="ghost" size="sm" onClick={onRefresh}>
          只刷新评测
        </Button>
      </div>

      <div data-testid="quality-eval-runs" className="overflow-x-auto">
        <table className="w-full min-w-[760px] border-separate border-spacing-0 text-left text-xs">
          <thead>
            <tr className="text-[11px] uppercase tracking-wide text-muted-foreground">
              <th className="border-b border-border px-2 py-1.5 font-medium">Run</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">状态</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">语料</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">模型</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">指标</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">时间</th>
            </tr>
          </thead>
          <tbody>
            {runs.map((run) => {
              const passed = run.metrics.filter((metric) => metric.passed).length
              return (
                <tr key={run.id} data-testid={`quality-eval-run-${run.id}`}>
                  <td className="border-b border-border px-2 py-1.5 font-mono text-foreground">
                    #{run.id}
                  </td>
                  <td className="border-b border-border px-2 py-1.5">
                    <Badge tone={EVAL_STATUS_TONE[run.status]}>
                      {EVAL_STATUS_LABEL[run.status]}
                    </Badge>
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {run.corpus_version}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {run.model.length > 0 ? run.model : "—"}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {passed}/{run.metrics.length}
                  </td>
                  <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
                    {formatDateTime(run.created_at)}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      <div data-testid="quality-eval-metrics" className="overflow-x-auto">
        <h3 className="text-xs font-semibold text-foreground">
          最新 run 指标 <span className="font-mono text-muted-foreground">#{latest.id}</span>
        </h3>
        <table className="mt-1 w-full min-w-[680px] border-separate border-spacing-0 text-left text-xs">
          <thead>
            <tr className="text-[11px] uppercase tracking-wide text-muted-foreground">
              <th className="border-b border-border px-2 py-1.5 font-medium">指标</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">值</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">阈值</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">基线</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">状态</th>
              <th className="border-b border-border px-2 py-1.5 font-medium">分子/分母</th>
            </tr>
          </thead>
          <tbody>
            {latest.metrics.map((metric) => (
              <EvalMetricRow key={metric.name} metric={metric} />
            ))}
          </tbody>
        </table>
      </div>

      <div
        data-testid="quality-eval-baseline"
        className="rounded-lg border border-border bg-muted/20 px-3 py-2.5"
      >
        <h3 className="text-xs font-semibold text-foreground">基线</h3>
        {baselineMetrics.length === 0 ? (
          <p className="mt-1 text-[11px] text-muted-foreground">本次 run 未提供基线。</p>
        ) : (
          <dl className="mt-1 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4">
            {baselineMetrics.map((metric) => (
              <div key={metric.name} className="text-[11px]">
                <dt className="text-muted-foreground">
                  {EVAL_METRIC_LABEL[metric.name] ?? metric.name}
                </dt>
                <dd className="font-medium text-foreground">
                  {formatPercent(metric.detail.baseline)}
                </dd>
              </div>
            ))}
          </dl>
        )}
        <p className="mt-2 text-[10px] text-muted-foreground">
          基线回归由服务端标记（报告轨，不阻断阶段验收）。
        </p>
      </div>
    </div>
  )
}

function EvalMetricRow({ metric }: { metric: EvalMetric }) {
  const baseline = metric.detail.baseline
  return (
    <tr data-testid={`quality-eval-metric-${metric.name}`}>
      <td className="border-b border-border px-2 py-1.5 text-foreground">
        {EVAL_METRIC_LABEL[metric.name] ?? metric.name}
      </td>
      <td className="border-b border-border px-2 py-1.5 font-medium text-foreground">
        {formatPercent(metric.value)}
      </td>
      <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
        {formatPercent(metric.threshold)}
      </td>
      <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
        {baseline !== null && baseline !== undefined ? formatPercent(baseline) : "—"}
      </td>
      <td className="border-b border-border px-2 py-1.5">
        <Badge tone={evalMetricTone(metric)}>{evalMetricReason(metric)}</Badge>
      </td>
      <td className="border-b border-border px-2 py-1.5 text-muted-foreground">
        {metric.detail.numerator}/{metric.detail.denominator}
      </td>
    </tr>
  )
}

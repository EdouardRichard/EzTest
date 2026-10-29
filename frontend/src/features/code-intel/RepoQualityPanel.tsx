"use client"

import { Loader2 } from "lucide-react"
import { useCallback, useEffect, useId, useRef, useState } from "react"

import { getRepoQuality } from "./api"
import { describeCodeError } from "./empty"
import type { RepoQuality } from "./types"

type QualityLoad =
  | { kind: "loading" }
  | { kind: "ready"; quality: RepoQuality }
  | { kind: "error"; message: string }

/** Human labels of the closed degraded sources of the impact metrics. */
const IMPACT_REASON_LABELS: Record<string, string> = {
  no_eval_run: "尚未运行评测",
  no_impact_metric: "评测缺少影响指标",
  eval_store_unavailable: "评测数据暂不可用",
}

/** Renders an optional rate as one-decimal percent; null means "no data". */
function formatRate(value: number | null): string {
  if (typeof value !== "number") return "无数据"
  return `${(value * 100).toFixed(1)}%`
}

/** True when at least one persisted quality source exists. */
function hasQualityData(quality: RepoQuality): boolean {
  return (
    quality.analysis !== null ||
    quality.impact.available ||
    quality.reviews.total > 0 ||
    quality.confidence.total > 0 ||
    quality.evidence.checks > 0 ||
    quality.invalidFacts > 0
  )
}

/** The impact line: real precision/recall, or an explicit unavailable marker. */
function impactText(impact: RepoQuality["impact"]): string {
  if (impact.available && impact.precision !== null && impact.recall !== null) {
    return `precision ${formatRate(impact.precision)} · recall ${formatRate(impact.recall)}`
  }
  const reason =
    impact.reason === undefined || impact.reason === ""
      ? "评测不可用"
      : (IMPACT_REASON_LABELS[impact.reason] ?? impact.reason)
  return `评测不可用（${reason}）`
}

export type RepoQualityPanelProps = {
  /** P2 code repository id (a number in the URL contract, spec §7.1). */
  repoId: number
}

/**
 * Repository quality block of the「代码与变更」tab (spec §9.2): evidence pass
 * rate, invalid facts, confidence distribution, human review counters, the
 * newest evaluation's impact precision/recall and the cache hit rate. The panel
 * renders only persisted aggregate data: missing sources stay `无数据`/unavailable
 * and an all-empty aggregate renders the empty state instead of fabricated zeros.
 */
export function RepoQualityPanel({ repoId }: RepoQualityPanelProps) {
  const headingId = useId()
  const requestRef = useRef(0)
  const [load, setLoad] = useState<QualityLoad>({ kind: "loading" })

  const loadQuality = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoad({ kind: "loading" })
    try {
      const quality = await getRepoQuality(repoId)
      if (requestRef.current !== requestId) return
      setLoad({ kind: "ready", quality })
    } catch (caught) {
      if (requestRef.current !== requestId) return
      setLoad({ kind: "error", message: describeCodeError(caught) })
    }
  }, [repoId])

  useEffect(() => {
    void loadQuality()
    return () => {
      requestRef.current += 1
    }
  }, [loadQuality])

  return (
    <section
      data-testid="repo-quality-panel"
      aria-labelledby={headingId}
      className="space-y-2 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id={headingId} className="text-base font-semibold">
          代码质量
        </h2>
        {load.kind === "ready" && load.quality.analysis !== null ? (
          <p className="text-[11px] text-muted-foreground">
            分析 {load.quality.analysis.headCommit.slice(0, 8)}
          </p>
        ) : null}
      </div>

      {load.kind === "loading" ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载质量指标…
        </p>
      ) : load.kind === "error" ? (
        <div
          role="alert"
          className="space-y-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2.5 text-sm text-destructive"
        >
          <p>加载质量指标失败：{load.message}</p>
          <button
            type="button"
            onClick={() => void loadQuality()}
            className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            重试质量指标
          </button>
        </div>
      ) : !hasQualityData(load.quality) ? (
        <div className="space-y-1">
          <p className="text-sm text-muted-foreground">暂无质量数据</p>
          <p className="text-xs text-muted-foreground">
            完成一次代码分析或人工修正后，依次展示证据通过率、无效事实、置信度分布与影响评测指标。
          </p>
        </div>
      ) : (
        <QualityMetrics quality={load.quality} />
      )}
    </section>
  )
}

/** The metric grid of one non-empty aggregate. */
function QualityMetrics({ quality }: { quality: RepoQuality }) {
  const evidence =
    quality.evidence.passRate === null
      ? "无数据"
      : `${formatRate(quality.evidence.passRate)}（${quality.evidence.checks} 次校验，${quality.evidence.failures} 次失败）`
  return (
    <dl className="grid gap-x-4 gap-y-2 text-xs sm:grid-cols-2">
      <div>
        <dt className="text-muted-foreground">证据通过率</dt>
        <dd data-testid="quality-evidence-pass-rate" className="font-medium text-foreground">
          {evidence}
        </dd>
      </div>
      <div>
        <dt className="text-muted-foreground">无效事实</dt>
        <dd data-testid="quality-invalid-facts" className="font-medium text-foreground">
          {quality.invalidFacts} 条
        </dd>
      </div>
      <div>
        <dt className="text-muted-foreground">置信度分布</dt>
        <dd data-testid="quality-confidence" className="font-medium text-foreground">
          高 {quality.confidence.high} · 中 {quality.confidence.medium} · 低{" "}
          {quality.confidence.low}（共 {quality.confidence.total} 条）
        </dd>
      </div>
      <div>
        <dt className="text-muted-foreground">人工修正</dt>
        <dd data-testid="quality-reviews" className="font-medium text-foreground">
          共 {quality.reviews.total} 条（正确 {quality.reviews.correct} · 错误{" "}
          {quality.reviews.incorrect}）
        </dd>
      </div>
      <div>
        <dt className="text-muted-foreground">最近影响评测</dt>
        <dd data-testid="quality-impact" className="font-medium text-foreground">
          {impactText(quality.impact)}
        </dd>
      </div>
      <div>
        <dt className="text-muted-foreground">缓存命中率</dt>
        <dd data-testid="quality-cache-hit-rate" className="font-medium text-foreground">
          {formatRate(quality.cacheHitRate)}
        </dd>
      </div>
    </dl>
  )
}

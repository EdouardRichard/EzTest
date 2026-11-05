"use client"

import { useCallback, useEffect, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"

import { getDiscoveryCoverage } from "./api"
import type { DiscoveryCoverage, DiscoveryCoverageBucket } from "./types"

export type CoveragePanelProps = {
  versionId: string
  /** SSE-driven refresh nonce (`resource.delta` / scope snapshot). */
  refreshKey?: number
}

const BUCKETS: { key: keyof DiscoveryCoverageBucket & string; label: string }[] = [
  { key: "proposed", label: "待处理" },
  { key: "approved", label: "已批准" },
  { key: "rejected", label: "已拒绝" },
  { key: "out_of_scope", label: "范围外" },
]

function isEmptyBucket(bucket: DiscoveryCoverageBucket): boolean {
  return (
    bucket.observed === 0 &&
    bucket.proposed === 0 &&
    bucket.approved === 0 &&
    bucket.rejected === 0 &&
    bucket.out_of_scope === 0 &&
    bucket.uncovered.length === 0
  )
}

function CoverageCard({
  kind,
  title,
  bucket,
}: {
  kind: "pages" | "apis"
  title: string
  bucket: DiscoveryCoverageBucket
}) {
  return (
    <article
      data-testid={`coverage-${kind}`}
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-xs font-semibold text-foreground">{title}</h3>
        <span className="text-[11px] text-muted-foreground">
          已观测 {bucket.observed} 个资源模板
        </span>
      </header>
      <dl className="grid grid-cols-2 gap-1.5 text-[11px] sm:grid-cols-4">
        {BUCKETS.map((entry) => (
          <div key={entry.key} className="rounded-md border border-border px-2 py-1">
            <dt className="text-muted-foreground">{entry.label}</dt>
            <dd className="text-sm font-semibold text-foreground">{bucket[entry.key]}</dd>
          </div>
        ))}
      </dl>
      <div data-testid={`coverage-${kind}-uncovered`} className="text-[11px]">
        <p className="text-muted-foreground">未覆盖（仍待处理）</p>
        {bucket.uncovered.length === 0 ? (
          <p className="text-muted-foreground">无未覆盖资源</p>
        ) : (
          <ul className="mt-1 space-y-0.5">
            {bucket.uncovered.map((identity) => (
              <li
                key={identity}
                className="truncate rounded border border-dashed border-border px-1.5 py-0.5 font-mono text-foreground"
                title={identity}
              >
                {identity}
              </li>
            ))}
          </ul>
        )}
      </div>
    </article>
  )
}

/**
 * W1 discovery-side coverage matrix (`GET /api/versions/{id}/coverage`): per
 * kind the observed template count, the four decision buckets and the still
 * uncovered resource identities. API identities keep the METHOD because GET and
 * POST on one path are different resources (R-EX-2). Case coverage is W2/T14
 * and deliberately not faked here.
 */
export function CoveragePanel({ versionId, refreshKey = 0 }: CoveragePanelProps) {
  const [coverage, setCoverage] = useState<DiscoveryCoverage | null>(null)
  const [error, setError] = useState<unknown>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      setCoverage(await getDiscoveryCoverage(versionId))
    } catch (cause) {
      setCoverage(null)
      setError(cause)
    }
  }, [versionId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE nonce.
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  const empty = coverage !== null && isEmptyBucket(coverage.pages) && isEmptyBucket(coverage.apis)

  return (
    <section
      data-testid="coverage-panel"
      aria-label="探索覆盖"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-end justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">探索覆盖</h2>
          <p className="text-[11px] text-muted-foreground">
            发现侧覆盖：已观测模板 · 五类决策 · 未覆盖清单（用例覆盖属 W2）
          </p>
        </div>
      </header>

      {error !== null ? (
        <ErrorState error={error} title="探索覆盖加载失败" onRetry={() => void load()} />
      ) : coverage === null ? (
        <LoadingCard label="正在加载探索覆盖…" />
      ) : empty ? (
        <EmptyState
          title="该版本暂无探索覆盖"
          description="执行 wave 结束后，发现的页面/API 模板会在这里形成覆盖矩阵。"
        />
      ) : (
        <div className="grid gap-2 lg:grid-cols-2">
          <CoverageCard kind="pages" title="页面" bucket={coverage.pages} />
          <CoverageCard kind="apis" title="API" bucket={coverage.apis} />
        </div>
      )}
    </section>
  )
}

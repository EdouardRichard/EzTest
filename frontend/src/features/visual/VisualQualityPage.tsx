"use client"

import { useRouter } from "next/navigation"
import { useEffect, useState } from "react"

import { InlineError } from "@/components/ui/ErrorState"
import { Select } from "@/components/ui/Select"
import { listVersions } from "@/features/versions/api"
import type { VersionSummary } from "@/features/versions/types"

import { QualityMetricsPanel } from "./QualityMetricsPanel"

export type VisualQualityPageProps = {
  /** Deep link `?version=` from the quality route. */
  initialVersionId?: string
}

/**
 * Minimal quality page (spec §8.2): the P5 visual metrics only, with a version
 * scope switch. Eval/metrics from P6 are intentionally absent; the page keeps
 * the existing `/quality` global entry and adds no new navigation.
 */
export function VisualQualityPage({ initialVersionId }: VisualQualityPageProps) {
  const router = useRouter()
  const [versions, setVersions] = useState<VersionSummary[]>([])
  const [versionsError, setVersionsError] = useState<unknown>(null)
  const [versionId, setVersionId] = useState(initialVersionId ?? "")

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

  function changeVersion(next: string): void {
    setVersionId(next)
    router.replace(next.length > 0 ? `/quality?version=${encodeURIComponent(next)}` : "/quality")
  }

  return (
    <div data-testid="visual-quality-page" className="space-y-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">
            全局工作区
          </p>
          <h1 className="mt-1 text-xl font-semibold text-foreground">质量</h1>
          <p className="mt-1 max-w-2xl text-xs leading-relaxed text-muted-foreground">
            视觉断言的基线覆盖率、四态分布、未定原因与差异延迟。其余质量/评测能力在后续阶段提供。
          </p>
        </div>
      </header>

      <section aria-label="视觉质量指标" className="surface-card space-y-3 p-4">
        <header className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h2 className="text-sm font-semibold text-foreground">视觉断言质量</h2>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              指标为真实聚合值；未执行或未配置视觉断言时保持空态，不展示占位数字。
            </p>
          </div>
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
        </header>

        {versionsError !== null ? <InlineError error={versionsError} /> : null}

        <QualityMetricsPanel versionId={versionId.length > 0 ? versionId : undefined} />
      </section>
    </div>
  )
}

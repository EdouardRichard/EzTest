"use client"

import Link from "next/link"
import { useCallback, useEffect, useMemo, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDateTime } from "@/lib/format"

import { getVersionFacts, type VersionFact } from "./api"
import { FactArbitrationCard } from "./FactArbitrationCard"

export type FactsPanelProps = {
  versionId: string
  /** Deep link `?panel=facts&fact=` highlights one fact. */
  initialFactId?: string
  /** SSE-driven refresh nonce (`fact.recorded` / `fact.arbitrated`). */
  refreshKey?: number
}

function conflictKey(fact: VersionFact): string {
  return `${fact.kind}\u0000${fact.key}`
}

function factHref(versionId: string, factId: string): string {
  const params = new URLSearchParams({ tab: "overview", panel: "facts", fact: factId })
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

/** Groups current facts that share `kind` + `key`; a group > 1 is a conflict. */
function factConflicts(facts: VersionFact[]): Map<string, VersionFact[]> {
  const groups = new Map<string, VersionFact[]>()
  for (const fact of facts) {
    if (fact.superseded_by !== null) continue
    const key = conflictKey(fact)
    const group = groups.get(key)
    if (group === undefined) groups.set(key, [fact])
    else group.push(fact)
  }
  for (const [key, group] of groups) if (group.length < 2) groups.delete(key)
  return groups
}

/**
 * Version facts panel (API-12/API-13). Every row is an append-only fact of the
 * version; contradictory current rows of the same `kind` + `key` get a
 * {@link FactArbitrationCard} so the host can keep or drop one explicitly.
 */
export function FactsPanel({ versionId, initialFactId, refreshKey = 0 }: FactsPanelProps) {
  const [facts, setFacts] = useState<VersionFact[] | null>(null)
  const [error, setError] = useState<unknown>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      setFacts(await getVersionFacts(versionId))
    } catch (cause) {
      setFacts(null)
      setError(cause)
    }
  }, [versionId])

  // biome-ignore lint/correctness/useExhaustiveDependencies: refreshKey is the SSE nonce.
  useEffect(() => {
    void load()
  }, [load, refreshKey])

  const conflicts = useMemo(
    () => (facts === null ? [] : [...factConflicts(facts).values()]),
    [facts],
  )

  return (
    <section
      data-testid="version-facts"
      aria-label="版本事实"
      className="space-y-2 rounded-lg border border-border bg-card px-3 py-2.5"
    >
      <header className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">版本事实</h2>
          <p className="text-[11px] text-muted-foreground">
            共 {facts?.length ?? 0} 条 · append-only，冲突并存时需人工仲裁
          </p>
        </div>
        <Button size="sm" variant="secondary" onClick={() => void load()}>
          刷新
        </Button>
      </header>

      {error !== null ? (
        <ErrorState error={error} title="版本事实加载失败" onRetry={() => void load()} />
      ) : facts === null ? (
        <LoadingCard label="正在加载版本事实…" />
      ) : facts.length === 0 ? (
        <EmptyState
          title="该版本暂无事实"
          description="探索/代码扫描产生的事实会写入这里，并参与计划与设计上下文。"
        />
      ) : (
        <ul className="space-y-1.5">
          {facts.map((fact) => {
            const superseded = fact.superseded_by !== null
            const isSelected = fact.id === initialFactId
            return (
              <li
                key={fact.id}
                data-testid={`version-fact-${fact.id}`}
                className={
                  isSelected
                    ? "rounded-md border border-primary/40 bg-primary/5 px-2 py-1.5"
                    : "rounded-md border border-border px-2 py-1.5"
                }
              >
                <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                  <Badge tone="info">{fact.kind}</Badge>
                  <Link
                    href={factHref(versionId, fact.id)}
                    className="min-w-0 flex-1 truncate font-medium text-foreground underline underline-offset-2"
                  >
                    {fact.key}
                  </Link>
                  {superseded ? (
                    <Badge tone="neutral">已被 #{fact.superseded_by} 替代</Badge>
                  ) : null}
                  <span className="text-muted-foreground">来源 {fact.source}</span>
                  {fact.confidence !== undefined && fact.confidence !== null ? (
                    <span className="text-muted-foreground">
                      置信度 {Math.round(fact.confidence * 100)}%
                    </span>
                  ) : (
                    <span className="text-muted-foreground">置信度未知</span>
                  )}
                  <span className="text-muted-foreground">{formatDateTime(fact.created_at)}</span>
                </div>
              </li>
            )
          })}
        </ul>
      )}

      {conflicts.map((group) => {
        const selected = group.find((fact) => fact.id === initialFactId) ?? group[0]
        return (
          <FactArbitrationCard
            key={conflictKey(selected)}
            versionId={versionId}
            fact={selected}
            conflictCount={group.length}
            onArbitrated={() => void load()}
          />
        )
      })}
    </section>
  )
}

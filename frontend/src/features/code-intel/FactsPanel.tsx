"use client"

import { Loader2 } from "lucide-react"
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react"

import { getFacts } from "./api"
import { ConfidenceBadge } from "./ConfidenceBadge"
import { EvidenceLink } from "./EvidenceLink"
import { describeCodeError } from "./empty"
import type { Analysis, CodeEntity } from "./types"

/** One bounded fact page requested per kind (keeps T0 `file` facts out). */
export const FACTS_PANEL_LIMIT = 50

/** The seven AI fact kinds, in the display order of spec §9.2. */
export const FACT_KIND_ORDER = [
  "route",
  "page",
  "api",
  "client_call",
  "component",
  "module",
  "symbol",
] as const

export type FactKind = (typeof FACT_KIND_ORDER)[number]

const FACT_KIND_LABELS: Record<FactKind, string> = {
  route: "路由",
  page: "页面",
  api: "接口",
  client_call: "调用点",
  component: "组件",
  module: "模块",
  symbol: "符号",
}

/** `path:start-end` for the fact's own location (not its evidence). */
function formatFactPath(fact: CodeEntity): string {
  if (typeof fact.startLine !== "number") return fact.path
  if (typeof fact.endLine !== "number" || fact.endLine === fact.startLine) {
    return `${fact.path}:${fact.startLine}`
  }
  return `${fact.path}:${fact.startLine}-${fact.endLine}`
}

type FactsLoad =
  | { kind: "loading" }
  | { kind: "ready"; facts: CodeEntity[]; hasMore: boolean }
  | { kind: "error"; message: string }

export type FactsPanelProps = {
  analysis: Analysis
}

/**
 * AI/T0 code facts grouped by kind (spec §9.2). Facts come from the real
 * `GET /api/code/repos/{repoId}/facts` read at the analysis head commit; the
 * panel never fabricates rows. `low` facts stay visible but greyed as
 * 「低置信线索」, and their `file:line` evidence opens the snippet drawer.
 */
export function FactsPanel({ analysis }: FactsPanelProps) {
  const headingId = useId()
  const requestRef = useRef(0)
  const ready = analysis.status === "done"

  const [load, setLoad] = useState<FactsLoad>({ kind: "loading" })

  const loadFacts = useCallback(async (): Promise<void> => {
    const requestId = ++requestRef.current
    setLoad({ kind: "loading" })
    try {
      // Query once per kind: a commit-wide page also contains one T0 `file` entity
      // per blob, which would otherwise crowd the AI facts out of the page limit.
      const pages = await Promise.all(
        FACT_KIND_ORDER.map((kind) =>
          getFacts(analysis.repoId, {
            commit: analysis.headCommit,
            kind,
            limit: FACTS_PANEL_LIMIT,
          }),
        ),
      )
      if (requestRef.current !== requestId) return
      const facts = pages.flatMap((page, index) => {
        const kind = FACT_KIND_ORDER[index]
        const items = Array.isArray(page.items) ? page.items : []
        return items.filter((fact) => fact.kind === kind)
      })
      setLoad({
        kind: "ready",
        facts,
        hasMore: pages.some((page) => page.hasMore === true),
      })
    } catch (caught) {
      if (requestRef.current !== requestId) return
      setLoad({ kind: "error", message: describeCodeError(caught) })
    }
  }, [analysis.repoId, analysis.headCommit])

  useEffect(() => {
    if (!ready) return
    void loadFacts()
    return () => {
      requestRef.current += 1
    }
  }, [ready, loadFacts])

  const groups = useMemo(() => {
    if (load.kind !== "ready") return []
    return FACT_KIND_ORDER.map((kind) => ({
      kind,
      label: FACT_KIND_LABELS[kind],
      facts: load.facts.filter((fact) => fact.kind === kind),
    })).filter((group) => group.facts.length > 0)
  }, [load])

  const totalFacts = groups.reduce((total, group) => total + group.facts.length, 0)

  return (
    <section
      aria-labelledby={headingId}
      className="space-y-3 rounded-2xl border border-border bg-card p-4"
    >
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id={headingId} className="text-base font-semibold">
          代码事实
        </h2>
        <p className="text-xs text-muted-foreground">按 kind 分组 · {totalFacts} 条</p>
      </div>

      {!ready ? (
        <p role="status" className="text-sm text-muted-foreground">
          {analysis.status === "queued" || analysis.status === "running"
            ? "分析运行中，代码事实完成后展示。"
            : "分析未完成，暂无代码事实。"}
        </p>
      ) : load.kind === "loading" ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载代码事实…
        </p>
      ) : load.kind === "error" ? (
        <div
          role="alert"
          className="space-y-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-4 text-sm text-destructive"
        >
          <p>加载代码事实失败：{load.message}</p>
          <button
            type="button"
            onClick={() => void loadFacts()}
            className="rounded-lg border border-border bg-card px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            重试加载事实
          </button>
        </div>
      ) : groups.length === 0 ? (
        <p className="text-sm text-muted-foreground">暂无代码事实</p>
      ) : (
        <div className="space-y-4">
          {load.hasMore ? (
            <p
              role="status"
              className="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
            >
              代码事实较多，仅展示前 {totalFacts} 条。
            </p>
          ) : null}
          {groups.map((group) => (
            <div key={group.kind} className="space-y-2">
              <div className="flex items-baseline justify-between gap-2">
                <h3 className="text-sm font-semibold">{group.label}</h3>
                <span className="text-xs text-muted-foreground">{group.facts.length} 条</span>
              </div>
              <ul className="divide-y divide-border">
                {group.facts.map((fact) => {
                  const low = fact.confidence === "low"
                  return (
                    <li
                      key={fact.id}
                      data-fact-kind={fact.kind}
                      data-confidence={fact.confidence}
                      className={`space-y-1.5 py-2.5 ${low ? "opacity-60" : ""}`}
                    >
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="min-w-0 break-all font-mono text-sm text-foreground">
                          {fact.name}
                        </span>
                        <ConfidenceBadge confidence={fact.confidence} />
                        {low ? (
                          <span className="text-[11px] font-medium text-muted-foreground">
                            低置信线索
                          </span>
                        ) : null}
                      </div>
                      <p className="break-all font-mono text-[11px] text-muted-foreground">
                        {formatFactPath(fact)}
                      </p>
                      {fact.signature === "" ? null : (
                        <p className="break-all text-xs text-muted-foreground">{fact.signature}</p>
                      )}
                      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                        <span>证据</span>
                        {fact.evidence.length === 0 ? (
                          <span>无有效证据</span>
                        ) : (
                          fact.evidence.map((evidence) => (
                            <EvidenceLink
                              key={`${evidence.file}:${evidence.startLine ?? ""}:${
                                evidence.endLine ?? ""
                              }:${evidence.excerptHash ?? ""}`}
                              repoId={analysis.repoId}
                              commit={analysis.headCommit}
                              evidence={evidence}
                            />
                          ))
                        )}
                      </div>
                    </li>
                  )
                })}
              </ul>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

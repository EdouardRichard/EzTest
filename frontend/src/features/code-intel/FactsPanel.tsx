"use client"

import { Loader2 } from "lucide-react"
import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react"

import { createFactReview, getFacts } from "./api"
import { ConfidenceBadge } from "./ConfidenceBadge"
import { EvidenceLink } from "./EvidenceLink"
import { describeCodeError } from "./empty"
import type { Analysis, CodeEntity, FactReview, FactReviewVerdict } from "./types"

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
  /** Persisted human verdicts, seeded from the facts page and updated on write. */
  const [reviews, setReviews] = useState<Record<string, FactReview>>({})
  const [editingFactId, setEditingFactId] = useState<string | null>(null)
  const [reasonDraft, setReasonDraft] = useState("")
  const [savingReview, setSavingReview] = useState(false)
  const [reviewError, setReviewError] = useState("")

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
      const persisted: Record<string, FactReview> = {}
      for (const fact of facts) {
        if (fact.review) persisted[fact.id] = fact.review
      }
      setReviews(persisted)
      setEditingFactId(null)
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

  function openReviewEditor(fact: CodeEntity): void {
    setEditingFactId(fact.id)
    setReasonDraft(reviews[fact.id]?.reason ?? "")
    setReviewError("")
  }

  function closeReviewEditor(): void {
    setEditingFactId(null)
    setReasonDraft("")
    setReviewError("")
  }

  async function submitReview(fact: CodeEntity, verdict: FactReviewVerdict): Promise<void> {
    if (savingReview) return
    const reason = reasonDraft.trim()
    setSavingReview(true)
    setReviewError("")
    try {
      // The same fact/commit is unique: a new verdict overwrites the previous
      // one server-side, so the local marker simply follows the response.
      const review = await createFactReview(
        analysis.repoId,
        fact.id,
        reason === "" ? { verdict } : { verdict, reason },
      )
      setReviews((current) => ({ ...current, [fact.id]: review }))
      setEditingFactId(null)
      setReasonDraft("")
    } catch (caught) {
      setReviewError(describeCodeError(caught))
    } finally {
      setSavingReview(false)
    }
  }

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
                  const review = reviews[fact.id]
                  const editing = editingFactId === fact.id
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

                      {/* Additive P8 human review: persisted marker + reason. */}
                      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs">
                        {review === undefined ? null : (
                          <span
                            data-testid="fact-review-badge"
                            data-review-verdict={review.verdict}
                            className={
                              review.verdict === "correct"
                                ? "rounded-full border border-emerald-500/30 bg-emerald-500/10 px-2 py-0.5 font-medium text-emerald-700 dark:text-emerald-300"
                                : "rounded-full border border-destructive/30 bg-destructive/10 px-2 py-0.5 font-medium text-destructive"
                            }
                          >
                            {review.verdict === "correct" ? "已人工修正" : "错误"}
                          </span>
                        )}
                        {review === undefined || review.reason === "" ? null : (
                          <p
                            data-testid="fact-review-reason"
                            className="break-all text-muted-foreground"
                          >
                            原因：{review.reason}
                          </p>
                        )}
                        <button
                          type="button"
                          onClick={() => openReviewEditor(fact)}
                          aria-label={`${review === undefined ? "人工修正" : "重新修正"} ${fact.name}`}
                          className="rounded-lg border border-border bg-card px-2 py-0.5 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          {review === undefined ? "人工修正" : "重新修正"}
                        </button>
                      </div>

                      {!editing ? null : (
                        <div className="space-y-2 rounded-xl border border-border bg-muted/30 p-2.5">
                          <textarea
                            aria-label="修正原因"
                            value={reasonDraft}
                            onChange={(event) => setReasonDraft(event.target.value)}
                            maxLength={2000}
                            rows={2}
                            placeholder="可选：说明修正原因"
                            className="w-full rounded-lg border border-border bg-card px-2.5 py-1.5 text-xs text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          />
                          <div className="flex flex-wrap gap-2">
                            <button
                              type="button"
                              disabled={savingReview}
                              onClick={() => void submitReview(fact, "correct")}
                              className="rounded-lg border border-border bg-card px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              标记为正确
                            </button>
                            <button
                              type="button"
                              disabled={savingReview}
                              onClick={() => void submitReview(fact, "incorrect")}
                              className="rounded-lg border border-destructive/40 bg-destructive/10 px-2.5 py-1 text-[11px] font-medium text-destructive transition-colors hover:bg-destructive/20 disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              标记为错误
                            </button>
                            <button
                              type="button"
                              disabled={savingReview}
                              onClick={closeReviewEditor}
                              className="rounded-lg px-2.5 py-1 text-[11px] font-medium text-muted-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            >
                              取消
                            </button>
                          </div>
                          {reviewError === "" ? null : (
                            <p
                              role="alert"
                              className="rounded-lg border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-[11px] text-destructive"
                            >
                              保存人工修正失败：{reviewError}
                            </p>
                          )}
                        </div>
                      )}
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

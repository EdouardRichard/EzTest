"use client"

import { BookOpen, Filter, Loader2, RefreshCw, ShieldCheck } from "lucide-react"
import Link from "next/link"
import { useCallback, useEffect, useMemo, useState } from "react"

import { useAuth } from "@/features/auth/AuthProvider"
import { ApiError } from "@/lib/api-client"
import { listGlobalKnowledge } from "./api"
import { ReviewDrawer } from "./ReviewDrawer"
import {
  KNOWLEDGE_KINDS,
  KNOWLEDGE_STATUSES,
  type KnowledgeEntry,
  type KnowledgeKind,
  type KnowledgeStatus,
  knowledgeConfidenceLabel,
  knowledgeKindLabel,
  knowledgeStatusLabel,
} from "./types"

/** Global list page size (the backend caps one page at 100). */
const PAGE_LIMIT = 50

/** Reads the window query once; the page intentionally avoids useSearchParams so
 * the route needs no Suspense boundary (same pattern as the agent console). */
function readInitialParams(): { version: string; review: string } {
  if (typeof window === "undefined") return { version: "", review: "" }
  const params = new URLSearchParams(window.location.search)
  return {
    version: params.get("version") ?? "",
    review: params.get("review") ?? "",
  }
}

function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    const requestId = error.requestId === "" ? "" : `（request id: ${error.requestId}）`
    if (error.status === 0) return `无法连接 API 服务，请确认后端已启动。${requestId}`
    return `${error.message}${requestId}`
  }
  return error instanceof Error ? error.message : "未知错误"
}

function emptyMessage(status: KnowledgeStatus): string {
  if (status === "approved") return "暂无已批准知识。"
  if (status === "draft") return "暂无待审核草稿。"
  return "暂无已驳回知识。"
}

/**
 * `/knowledge` — the global, read-only approved knowledge view (design spec
 * §9.4/§11.1). Every logged-in user reads the approved entries with kind and
 * version filters plus a detail pane; administrators additionally get the
 * draft/rejected filters and the review drawer. The `?version=` and `?review=`
 * query parameters are the version and todo deep links.
 */
export function KnowledgePage() {
  const { user } = useAuth()
  const isAdmin = user?.role === "admin"
  const initial = useMemo(readInitialParams, [])

  const [status, setStatus] = useState<KnowledgeStatus>(
    initial.review !== "" ? "approved" : "approved",
  )
  const [kind, setKind] = useState<KnowledgeKind | "">("")
  const [versionFilter, setVersionFilter] = useState(initial.version)
  const [appliedVersion, setAppliedVersion] = useState(initial.version)

  const [entries, setEntries] = useState<KnowledgeEntry[]>([])
  const [selectedId, setSelectedId] = useState("")
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")

  const [reviewOpen, setReviewOpen] = useState(initial.review !== "" && isAdmin)
  const [reviewFocus, setReviewFocus] = useState(initial.review)

  const load = useCallback(async () => {
    setLoading(true)
    setError("")
    try {
      const page = await listGlobalKnowledge({
        status,
        kind: kind === "" ? undefined : kind,
        versionId: appliedVersion === "" ? undefined : appliedVersion,
        limit: PAGE_LIMIT,
      })
      setEntries(page.items)
      setSelectedId((current) =>
        page.items.some((entry) => entry.id === current) ? current : (page.items[0]?.id ?? ""),
      )
    } catch (caught) {
      setError(describeError(caught))
    } finally {
      setLoading(false)
    }
  }, [status, kind, appliedVersion])

  useEffect(() => {
    void load()
  }, [load])

  const selected = entries.find((entry) => entry.id === selectedId) ?? null

  function openReview(): void {
    setReviewFocus("")
    setReviewOpen(true)
  }

  return (
    <section data-testid="knowledge-page" className="space-y-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">知识库</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            已批准的版本知识全局只读视图；版本内可提交草稿，管理员审核后进入这里。
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {isAdmin ? (
            <button
              type="button"
              data-testid="knowledge-open-review"
              onClick={openReview}
              className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <ShieldCheck className="h-4 w-4 text-sky-500" aria-hidden="true" />
              审核队列
            </button>
          ) : null}
          <button
            type="button"
            onClick={() => void load()}
            aria-label="刷新知识库"
            disabled={loading}
            className="grid h-9 w-9 place-items-center rounded-lg border border-border bg-card text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-50"
          >
            <RefreshCw
              className={loading ? "h-4 w-4 animate-spin" : "h-4 w-4"}
              aria-hidden="true"
            />
          </button>
        </div>
      </header>

      <section
        aria-label="知识筛选"
        className="flex flex-wrap items-end gap-2 rounded-2xl border border-border bg-card p-3"
      >
        {isAdmin ? (
          <label className="text-xs font-medium">
            状态
            <select
              data-testid="knowledge-status-filter"
              value={status}
              onChange={(event) => setStatus(event.target.value as KnowledgeStatus)}
              className="mt-1 block rounded-lg border border-border bg-background px-2 py-1.5 text-sm"
            >
              {KNOWLEDGE_STATUSES.map((item) => (
                <option key={item.value} value={item.value}>
                  {item.label}
                </option>
              ))}
            </select>
          </label>
        ) : null}
        <label className="text-xs font-medium">
          类型
          <select
            data-testid="knowledge-kind-filter"
            value={kind}
            onChange={(event) => setKind(event.target.value as KnowledgeKind | "")}
            className="mt-1 block rounded-lg border border-border bg-background px-2 py-1.5 text-sm"
          >
            <option value="">全部</option>
            {KNOWLEDGE_KINDS.map((item) => (
              <option key={item.value} value={item.value}>
                {item.label}
              </option>
            ))}
          </select>
        </label>
        <form
          className="flex items-end gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            setAppliedVersion(versionFilter.trim())
          }}
        >
          <label className="text-xs font-medium">
            版本 ID
            <input
              data-testid="knowledge-version-filter"
              value={versionFilter}
              onChange={(event) => setVersionFilter(event.target.value)}
              inputMode="numeric"
              className="mt-1 block w-40 rounded-lg border border-border bg-background px-2 py-1.5 text-sm"
              placeholder="十进制版本 ID"
            />
          </label>
          <button
            type="submit"
            className="inline-flex items-center gap-1 rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm font-medium hover:bg-muted"
          >
            <Filter className="h-3.5 w-3.5" aria-hidden="true" />
            筛选
          </button>
          {appliedVersion === "" ? null : (
            <button
              type="button"
              onClick={() => {
                setVersionFilter("")
                setAppliedVersion("")
              }}
              className="rounded-lg px-2.5 py-1.5 text-sm text-muted-foreground hover:bg-muted"
            >
              清除版本筛选
            </button>
          )}
        </form>
      </section>

      {error !== "" ? (
        <div
          role="alert"
          data-testid="knowledge-error"
          className="flex flex-wrap items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <span>加载知识库失败：{error}</span>
          <button
            type="button"
            onClick={() => void load()}
            className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground"
          >
            重试
          </button>
        </div>
      ) : null}

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)]">
        <section
          aria-label="知识列表"
          className="min-h-0 rounded-2xl border border-border bg-card p-3"
        >
          {loading && entries.length === 0 ? (
            <p
              role="status"
              data-testid="knowledge-loading"
              className="flex items-center gap-2 px-2 py-6 text-sm text-muted-foreground"
            >
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
              正在加载知识库…
            </p>
          ) : entries.length === 0 ? (
            <p className="px-2 py-6 text-center text-sm text-muted-foreground">
              {emptyMessage(status)}
            </p>
          ) : (
            <ul className="space-y-1">
              {entries.map((entry) => (
                <li key={entry.id}>
                  <button
                    type="button"
                    data-testid={`knowledge-list-item-${entry.id}`}
                    onClick={() => setSelectedId(entry.id)}
                    aria-current={entry.id === selectedId ? "true" : undefined}
                    className="w-full rounded-lg border border-transparent px-2.5 py-2 text-left hover:border-border hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring aria-[current=true]:border-border aria-[current=true]:bg-muted/60"
                  >
                    <span className="flex flex-wrap items-center gap-1.5">
                      <BookOpen className="h-3.5 w-3.5 text-sky-500" aria-hidden="true" />
                      <span className="text-sm font-medium">{entry.title}</span>
                      <span className="ml-auto rounded-full border border-border bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                        {knowledgeKindLabel(entry.kind)}
                      </span>
                    </span>
                    <span className="mt-0.5 block truncate text-[11px] text-muted-foreground">
                      {entry.versionName || `版本 ${entry.versionId}`} ·{" "}
                      {knowledgeStatusLabel(entry.status)}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>

        <section aria-label="知识详情" className="rounded-2xl border border-border bg-card p-4">
          {selected === null ? (
            <p className="py-6 text-center text-sm text-muted-foreground">选择左侧条目查看详情。</p>
          ) : (
            <article data-testid="knowledge-detail" className="space-y-3">
              <header>
                <p className="text-base font-semibold">{selected.title}</p>
                <p className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted-foreground">
                  <span className="rounded-full border border-border bg-muted px-1.5 py-0.5">
                    {knowledgeKindLabel(selected.kind)}
                  </span>
                  <span className="rounded-full border border-border bg-muted px-1.5 py-0.5">
                    {knowledgeStatusLabel(selected.status)}
                  </span>
                  <span>置信度 {knowledgeConfidenceLabel(selected.confidence)}</span>
                  <span>· revision {selected.revision}</span>
                </p>
              </header>
              <p className="whitespace-pre-wrap break-words text-sm">{selected.body}</p>
              {selected.tags.length === 0 ? null : (
                <p className="flex flex-wrap gap-1">
                  {selected.tags.map((tag) => (
                    <span
                      key={tag}
                      className="rounded-full border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground"
                    >
                      #{tag}
                    </span>
                  ))}
                </p>
              )}
              {selected.sourceRefs.length === 0 ? null : (
                <p className="text-[11px] text-muted-foreground">
                  来源：{selected.sourceRefs.map((ref) => `${ref.kind} ${ref.id}`).join(" · ")}
                </p>
              )}
              {selected.reviewNote === "" ? null : (
                <p className="rounded-lg border border-border bg-muted/30 px-2.5 py-1.5 text-xs text-muted-foreground">
                  审核意见：{selected.reviewNote}
                </p>
              )}
              <Link
                href={`/versions/${selected.versionId}?tab=overview`}
                className="inline-flex items-center gap-1 text-xs font-medium text-primary"
              >
                在版本总览中查看
              </Link>
            </article>
          )}
        </section>
      </div>

      {isAdmin ? (
        <ReviewDrawer
          open={reviewOpen}
          focusId={reviewFocus}
          onClose={() => setReviewOpen(false)}
          onReviewed={() => void load()}
        />
      ) : null}
    </section>
  )
}

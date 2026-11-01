"use client"

import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { type FormEvent, useCallback, useEffect, useState } from "react"

import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { ErrorState } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Table, type TableColumn } from "@/components/ui/Table"
import { formatDateTime } from "@/lib/format"

import { getIssue } from "./api"
import { type GlobalIssueListOptions, type GlobalIssueView, listGlobalIssues } from "./global-api"
import { IssueDetailSheet } from "./IssueDetailSheet"
import {
  ISSUE_CATEGORY_LABELS,
  ISSUE_SEVERITY_LABELS,
  ISSUE_STATUS_LABELS,
  type IssueCategory,
  type IssueDetailView,
  type IssueSeverity,
  type IssueStatus,
} from "./types"

const PAGE_SIZE = 20

const ISSUE_STATUSES = Object.keys(ISSUE_STATUS_LABELS) as IssueStatus[]
const ISSUE_CATEGORIES = Object.keys(ISSUE_CATEGORY_LABELS) as IssueCategory[]
const ISSUE_SEVERITIES = Object.keys(ISSUE_SEVERITY_LABELS) as IssueSeverity[]

const STATUS_TONE: Record<IssueStatus, BadgeTone> = {
  open: "warning",
  confirmed: "success",
  dismissed: "neutral",
  merged: "info",
}

type GlobalIssueFilters = {
  status: IssueStatus | "all"
  severity: IssueSeverity | "all"
  category: IssueCategory | "all"
  q: string
  versionId: string
}
function oneOf<T extends string>(raw: string | null, allowed: readonly T[]): T | "all" {
  if (raw !== null && (allowed as readonly string[]).includes(raw)) return raw as T
  return "all"
}

function filtersFromParams(params: URLSearchParams): GlobalIssueFilters {
  return {
    status: oneOf(params.get("status"), ISSUE_STATUSES),
    severity: oneOf(params.get("severity"), ISSUE_SEVERITIES),
    category: oneOf(params.get("category"), ISSUE_CATEGORIES),
    q: params.get("q")?.trim() ?? "",
    versionId: params.get("version_id")?.trim() ?? "",
  }
}

function sameFilters(left: GlobalIssueFilters, right: GlobalIssueFilters): boolean {
  return (
    left.status === right.status &&
    left.severity === right.severity &&
    left.category === right.category &&
    left.q === right.q &&
    left.versionId === right.versionId
  )
}

function hasActiveFilters(filters: GlobalIssueFilters): boolean {
  return (
    filters.status !== "all" ||
    filters.severity !== "all" ||
    filters.category !== "all" ||
    filters.q.length > 0 ||
    filters.versionId.length > 0
  )
}

function listOptions(filters: GlobalIssueFilters, before?: string): GlobalIssueListOptions {
  const options: GlobalIssueListOptions = { limit: PAGE_SIZE }
  if (filters.status !== "all") options.status = filters.status
  if (filters.severity !== "all") options.severity = filters.severity
  if (filters.category !== "all") options.category = filters.category
  if (filters.q.length > 0) options.q = filters.q
  if (filters.versionId.length > 0) options.versionId = filters.versionId
  if (before !== undefined && before.length > 0) options.before = before
  return options
}

/**
 * P6 global issues workspace (`/issues`, spec §4.3.5/§9). Cross-version,
 * login-only and strictly read-only: filters live in the URL, rows deep-link
 * into the owning version workspace and the detail sheet hides every write
 * action. No bulk/export/merge surface exists here.
 */
export function GlobalIssuesPage() {
  const router = useRouter()
  const searchParams = useSearchParams()

  const [filters, setFilters] = useState<GlobalIssueFilters>(() => filtersFromParams(searchParams))
  const [queryDraft, setQueryDraft] = useState(filters.q)
  const [issues, setIssues] = useState<GlobalIssueView[]>([])
  const [nextCursor, setNextCursor] = useState("")
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [reloadToken, setReloadToken] = useState(0)

  const [selected, setSelected] = useState<GlobalIssueView | null>(null)
  const [detail, setDetail] = useState<IssueDetailView | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<unknown>(null)
  const [detailToken, setDetailToken] = useState(0)

  // The URL is the filter source of truth; follow back/forward changes without
  // resetting an equal filter object (which would refetch needlessly).
  useEffect(() => {
    const next = filtersFromParams(searchParams)
    setFilters((current) => (sameFilters(current, next) ? current : next))
  }, [searchParams])

  useEffect(() => {
    setQueryDraft(filters.q)
  }, [filters.q])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken forces a retry
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    void listGlobalIssues(listOptions(filters))
      .then((page) => {
        if (cancelled) return
        setIssues(page.items)
        setNextCursor(page.nextCursor)
        setHasMore(page.hasMore)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setError(cause)
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [filters, reloadToken])

  // biome-ignore lint/correctness/useExhaustiveDependencies: detailToken forces a retry
  useEffect(() => {
    if (selected === null) {
      setDetail(null)
      setDetailLoading(false)
      setDetailError(null)
      return undefined
    }
    let cancelled = false
    setDetail(null)
    setDetailLoading(true)
    setDetailError(null)
    void getIssue(selected.version_id, selected.id)
      .then((next) => {
        if (!cancelled) setDetail(next)
      })
      .catch((cause: unknown) => {
        if (!cancelled) setDetailError(cause)
      })
      .finally(() => {
        if (!cancelled) setDetailLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [selected, detailToken])

  const writeUrl = useCallback(
    (next: GlobalIssueFilters) => {
      const params = new URLSearchParams()
      if (next.status !== "all") params.set("status", next.status)
      if (next.severity !== "all") params.set("severity", next.severity)
      if (next.category !== "all") params.set("category", next.category)
      if (next.q.length > 0) params.set("q", next.q)
      if (next.versionId.length > 0) params.set("version_id", next.versionId)
      const query = params.toString()
      router.replace(query.length > 0 ? `/issues?${query}` : "/issues")
    },
    [router],
  )

  const applyFilters = useCallback(
    (next: GlobalIssueFilters) => {
      setFilters(next)
      writeUrl(next)
    },
    [writeUrl],
  )

  function updateFilter<Key extends keyof GlobalIssueFilters>(
    key: Key,
    value: GlobalIssueFilters[Key],
  ): void {
    applyFilters({ ...filters, [key]: value })
  }

  function submitQuery(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    applyFilters({ ...filters, q: queryDraft.trim() })
  }

  async function loadMore(): Promise<void> {
    if (loadingMore || !hasMore || nextCursor.length === 0) return
    setLoadingMore(true)
    try {
      const page = await listGlobalIssues(listOptions(filters, nextCursor))
      setIssues((current) => [...current, ...page.items])
      setNextCursor(page.nextCursor)
      setHasMore(page.hasMore)
    } catch (cause) {
      setError(cause)
    } finally {
      setLoadingMore(false)
    }
  }

  const columns: TableColumn<GlobalIssueView>[] = [
    {
      key: "title",
      header: "标题",
      render: (issue) => (
        <div className="min-w-0">
          <button
            type="button"
            data-testid={`global-issue-${issue.id}`}
            onClick={() => setSelected(issue)}
            className="max-w-md text-left text-xs font-medium text-foreground underline-offset-2 hover:underline"
          >
            {issue.title}
          </button>
          {issue.status === "merged" &&
          issue.merged_into_id !== null &&
          issue.merged_into_id !== undefined ? (
            <span
              data-testid={`global-issue-merged-${issue.id}`}
              className="mt-0.5 block text-[11px] text-muted-foreground"
            >
              已合并至 #{issue.merged_into_id}
            </span>
          ) : null}
        </div>
      ),
    },
    {
      key: "version",
      header: "版本",
      render: (issue) => (
        <Link
          data-testid={`global-issue-link-${issue.id}`}
          href={issue.url}
          className="inline-flex items-center gap-1 text-xs text-primary underline-offset-2 hover:underline"
        >
          {issue.version_title.length > 0 ? issue.version_title : `版本 #${issue.version_id}`}
        </Link>
      ),
    },
    {
      key: "status",
      header: "状态",
      render: (issue) => (
        <Badge tone={STATUS_TONE[issue.status]}>{ISSUE_STATUS_LABELS[issue.status]}</Badge>
      ),
    },
    {
      key: "category",
      header: "类别",
      render: (issue) => (
        <span className="text-xs text-foreground">{ISSUE_CATEGORY_LABELS[issue.category]}</span>
      ),
    },
    {
      key: "severity",
      header: "严重度",
      render: (issue) => (
        <span className="text-xs text-foreground">{ISSUE_SEVERITY_LABELS[issue.severity]}</span>
      ),
    },
    {
      key: "reopen",
      header: "重开",
      render: (issue) => (
        <span
          data-testid={`global-issue-reopen-${issue.id}`}
          className="text-xs text-muted-foreground"
        >
          重开 {issue.reopen_count} 次
        </span>
      ),
    },
    {
      key: "updated",
      header: "更新时间",
      render: (issue) => (
        <span className="text-xs text-muted-foreground">{formatDateTime(issue.updated_at)}</span>
      ),
    },
  ]

  const filtered = hasActiveFilters(filters)

  return (
    <div data-testid="global-issues-page" className="space-y-4">
      <header>
        <h1 className="text-sm font-semibold text-foreground">全局问题</h1>
        <p className="mt-0.5 text-[11px] text-muted-foreground">
          跨版本只读视图；详情在所属版本工作区处理。
        </p>
      </header>

      <form aria-label="问题筛选" className="flex flex-wrap items-end gap-3" onSubmit={submitQuery}>
        <Field id="global-issues-filter-status" label="状态" className="w-36">
          <Select
            id="global-issues-filter-status"
            value={filters.status}
            onChange={(event) =>
              updateFilter("status", event.target.value as GlobalIssueFilters["status"])
            }
          >
            <option value="all">全部状态</option>
            {ISSUE_STATUSES.map((status) => (
              <option key={status} value={status}>
                {ISSUE_STATUS_LABELS[status]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="global-issues-filter-severity" label="严重度" className="w-36">
          <Select
            id="global-issues-filter-severity"
            value={filters.severity}
            onChange={(event) =>
              updateFilter("severity", event.target.value as GlobalIssueFilters["severity"])
            }
          >
            <option value="all">全部严重度</option>
            {ISSUE_SEVERITIES.map((severity) => (
              <option key={severity} value={severity}>
                {ISSUE_SEVERITY_LABELS[severity]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="global-issues-filter-category" label="类别" className="w-36">
          <Select
            id="global-issues-filter-category"
            value={filters.category}
            onChange={(event) =>
              updateFilter("category", event.target.value as GlobalIssueFilters["category"])
            }
          >
            <option value="all">全部类别</option>
            {ISSUE_CATEGORIES.map((category) => (
              <option key={category} value={category}>
                {ISSUE_CATEGORY_LABELS[category]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="global-issues-filter-q" label="搜索标题" className="w-64">
          <input
            id="global-issues-filter-q"
            type="search"
            value={queryDraft}
            maxLength={100}
            placeholder="标题关键词（1–100 字）"
            onChange={(event) => setQueryDraft(event.target.value)}
            className="h-9 w-full rounded-md border border-border bg-input px-3 text-sm text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </Field>
        <Button type="submit" size="sm" variant="secondary" data-testid="global-issues-search">
          搜索
        </Button>
        {filters.versionId.length > 0 ? (
          <span className="inline-flex h-8 items-center gap-2 rounded-md border border-border bg-card px-2.5 text-xs text-foreground">
            版本 {filters.versionId}
            <button
              type="button"
              aria-label="清除版本筛选"
              className="text-muted-foreground hover:text-foreground"
              onClick={() => applyFilters({ ...filters, versionId: "" })}
            >
              ×
            </button>
          </span>
        ) : null}
      </form>

      {loading ? (
        <LoadingCard label="正在加载问题…" />
      ) : error !== null ? (
        <ErrorState
          error={error}
          title="问题列表加载失败"
          onRetry={() => setReloadToken((token) => token + 1)}
        />
      ) : issues.length === 0 ? (
        <EmptyState
          title={filtered ? "没有符合筛选条件的问题" : "暂无问题"}
          description={filtered ? "调整筛选条件后重试。" : "执行失败后会自动生成跨版本问题候选。"}
        />
      ) : (
        <Table
          ariaLabel="全局问题列表"
          columns={columns}
          rows={issues}
          rowKey={(issue) => issue.id}
        />
      )}

      {hasMore ? (
        <div className="flex justify-center">
          <Button
            variant="secondary"
            size="sm"
            data-testid="global-issues-load-more"
            disabled={loadingMore}
            onClick={() => {
              void loadMore()
            }}
          >
            {loadingMore ? "加载中…" : "加载更多"}
          </Button>
        </div>
      ) : null}

      {selected !== null ? (
        <IssueDetailSheet
          versionId={selected.version_id}
          issue={detail}
          loading={detailLoading}
          error={detailError}
          readOnly
          onClose={() => setSelected(null)}
          onRetry={() => setDetailToken((token) => token + 1)}
          onTransition={async () => {}}
          onMerge={() => {}}
        />
      ) : null}
    </div>
  )
}

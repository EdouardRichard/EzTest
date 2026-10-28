"use client"

import { Bug, Loader2, Search } from "lucide-react"
import { type FormEvent, useState } from "react"

import { EmptyState } from "@/components/ui/EmptyState"
import { StatusBadge } from "@/components/ui/StatusBadge"

import { useGlobalIssues } from "../hooks/useIssues"
import { useVersionOptions } from "../hooks/useVersionOptions"
import {
  ISSUE_SEVERITY_LABELS,
  ISSUE_STATUS_LABELS,
  issueStatusVariant,
  labelOf,
  severityVariant,
} from "../labels"
import type { GlobalIssueFilters, IssueDeepLink } from "../types"

export type GlobalIssuesPageProps = {
  /** Optional router hook; the default is a normal `<a>` navigation. */
  onOpenIssue?: (deepLink: IssueDeepLink) => void
}

/**
 * Read-only cross-version issue centre (spec §7.10/§10.3): version/status
 * filters and a deep link `versions/{id}?tab=issues&issue={id}` per row. IDs
 * come straight from the API as decimal strings.
 */
export function GlobalIssuesPage({ onOpenIssue }: GlobalIssuesPageProps) {
  const versions = useVersionOptions()
  const [versionId, setVersionId] = useState("")
  const [status, setStatus] = useState("")
  const [search, setSearch] = useState("")
  const [query, setQuery] = useState("")

  const page = useGlobalIssues({
    versionId,
    status: status as GlobalIssueFilters["status"],
    q: query,
  })

  function handleSearch(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    setQuery(search.trim())
  }

  return (
    <section data-testid="global-issues-page" className="space-y-4">
      <header>
        <h1 className="text-2xl font-semibold tracking-tight">问题中心</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          跨版本问题概览（只读）。点击问题深链到对应版本的「问题」Tab 查看详情。
        </p>
      </header>

      <form
        onSubmit={handleSearch}
        className="flex flex-wrap items-end gap-2 rounded-xl border border-border bg-card p-3"
      >
        <label className="grid gap-1 text-[11px] text-muted-foreground">
          <span>版本</span>
          <select
            data-testid="global-issues-version-filter"
            value={versionId}
            onChange={(event) => setVersionId(event.target.value)}
            disabled={versions.loading}
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
          >
            <option value="">全部版本</option>
            {versions.options.map((version) => (
              <option key={version.id} value={version.id}>
                {version.label === "" ? version.name : `${version.name}（${version.label}）`}
              </option>
            ))}
          </select>
        </label>

        <label className="grid gap-1 text-[11px] text-muted-foreground">
          <span>状态</span>
          <select
            data-testid="global-issues-status-filter"
            value={status}
            onChange={(event) => setStatus(event.target.value)}
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">全部状态</option>
            <option value="open">待处理</option>
            <option value="confirmed">已确认</option>
            <option value="dismissed">已驳回</option>
          </select>
        </label>

        <label className="grid min-w-[10rem] flex-1 gap-1 text-[11px] text-muted-foreground">
          <span>搜索标题</span>
          <input
            type="search"
            data-testid="global-issues-search"
            value={search}
            maxLength={100}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="输入标题关键字"
            className="rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </label>
        <button
          type="submit"
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Search className="h-4 w-4" aria-hidden="true" />
          搜索
        </button>
      </form>

      {page.loading && page.issues.length === 0 ? (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
          正在加载跨版本问题…
        </p>
      ) : page.error !== "" ? (
        <div
          role="alert"
          data-testid="global-issues-error"
          className="flex flex-wrap items-center gap-2 rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <span>加载问题失败：{page.error}</span>
          <button
            type="button"
            onClick={() => void page.refresh()}
            className="rounded-lg border border-border bg-card px-2.5 py-1 text-xs font-medium text-foreground hover:bg-muted"
          >
            重试
          </button>
        </div>
      ) : page.issues.length === 0 ? (
        <EmptyState
          icon={Bug}
          title="暂无跨版本问题"
          description="调整版本或状态筛选，或等待执行/分析生成问题。"
        />
      ) : (
        <ul className="divide-y divide-border rounded-2xl border border-border bg-card">
          {page.issues.map((issue) => {
            const deepLink: IssueDeepLink = {
              versionId: issue.deepLink.versionId,
              tab: issue.deepLink.tab,
              issueId: issue.deepLink.issueId,
            }
            const href = `/versions/${encodeURIComponent(deepLink.versionId)}?tab=${encodeURIComponent(
              deepLink.tab,
            )}&issue=${encodeURIComponent(deepLink.issueId)}`
            return (
              <li
                key={issue.id}
                data-testid={`global-issue-row-${issue.id}`}
                className="flex flex-wrap items-center gap-2 px-3 py-2"
              >
                <StatusBadge variant={severityVariant(issue.severity)}>
                  {labelOf(ISSUE_SEVERITY_LABELS, issue.severity)}
                </StatusBadge>
                <a
                  href={href}
                  onClick={(event) => {
                    if (onOpenIssue === undefined) return
                    event.preventDefault()
                    onOpenIssue(deepLink)
                  }}
                  className="min-w-0 flex-1 truncate text-sm font-medium text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
                >
                  {issue.title}
                </a>
                <span className="text-xs text-muted-foreground">{issue.versionName}</span>
                <StatusBadge variant={issueStatusVariant(issue.status)}>
                  {labelOf(ISSUE_STATUS_LABELS, issue.status)}
                </StatusBadge>
              </li>
            )
          })}
        </ul>
      )}

      {page.hasMore ? (
        <button
          type="button"
          data-testid="global-issues-load-more"
          onClick={() => void page.loadMore()}
          disabled={page.loadingMore}
          className="rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground hover:bg-muted disabled:opacity-50"
        >
          {page.loadingMore ? "加载中…" : "加载更多"}
        </button>
      ) : null}
    </section>
  )
}

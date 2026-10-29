"use client"

import { RotateCcw, Search } from "lucide-react"
import { type FormEvent, useEffect, useState } from "react"

import { BATCH_KIND_LABELS, ISSUE_CATEGORY_LABELS, ISSUE_SEVERITY_LABELS, labelOf } from "../labels"
import type { ReportBatchSummary } from "../types"

/** The five issue filters of spec §7.3/§10.3. */
export type IssueFilterValues = {
  status: string
  severity: string
  category: string
  batchId: string
  q: string
}

export const EMPTY_ISSUE_FILTERS: IssueFilterValues = {
  status: "",
  severity: "",
  category: "",
  batchId: "",
  q: "",
}

export type IssueFiltersProps = {
  value: IssueFilterValues
  batches: ReportBatchSummary[]
  onChange: (next: IssueFilterValues) => void
}

const SELECT_CLASS =
  "rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"

/**
 * Filter bar of the issue tab (spec §10.3): status, severity, category, batch
 * and a fuzzy title search. The search is submitted explicitly so typing does
 * not fire one request per keystroke; every other control applies immediately.
 */
export function IssueFilters({ value, batches, onChange }: IssueFiltersProps) {
  const [query, setQuery] = useState(value.q)

  // An external reset (or a deep link) must clear the local input too.
  useEffect(() => {
    setQuery(value.q)
  }, [value.q])

  function handleSearch(event: FormEvent<HTMLFormElement>): void {
    event.preventDefault()
    onChange({ ...value, q: query.trim() })
  }

  return (
    <form
      data-testid="issue-filters"
      onSubmit={handleSearch}
      className="flex flex-wrap items-end gap-2 rounded-xl border border-border bg-card p-3"
    >
      <label className="grid gap-1 text-[11px] text-muted-foreground">
        <span>状态</span>
        <select
          data-testid="issue-status-filter"
          value={value.status}
          onChange={(event) => onChange({ ...value, status: event.target.value })}
          className={SELECT_CLASS}
        >
          <option value="">全部状态</option>
          <option value="open">待处理</option>
          <option value="confirmed">已确认</option>
          <option value="dismissed">已驳回</option>
        </select>
      </label>

      <label className="grid gap-1 text-[11px] text-muted-foreground">
        <span>严重级</span>
        <select
          data-testid="issue-severity-filter"
          value={value.severity}
          onChange={(event) => onChange({ ...value, severity: event.target.value })}
          className={SELECT_CLASS}
        >
          <option value="">全部严重级</option>
          {Object.entries(ISSUE_SEVERITY_LABELS).map(([key, label]) => (
            <option key={key} value={key}>
              {label}
            </option>
          ))}
        </select>
      </label>

      <label className="grid gap-1 text-[11px] text-muted-foreground">
        <span>分类</span>
        <select
          data-testid="issue-category-filter"
          value={value.category}
          onChange={(event) => onChange({ ...value, category: event.target.value })}
          className={SELECT_CLASS}
        >
          <option value="">全部分类</option>
          {Object.entries(ISSUE_CATEGORY_LABELS).map(([key, label]) => (
            <option key={key} value={key}>
              {label}
            </option>
          ))}
        </select>
      </label>

      <label className="grid gap-1 text-[11px] text-muted-foreground">
        <span>批次</span>
        <select
          data-testid="issue-batch-filter"
          value={value.batchId}
          onChange={(event) => onChange({ ...value, batchId: event.target.value })}
          className={SELECT_CLASS}
        >
          <option value="">全部批次</option>
          {batches.map((batch) => (
            <option key={batch.id} value={batch.id}>
              {`Batch #${batch.batchNo}（${labelOf(BATCH_KIND_LABELS, batch.kind)}）`}
            </option>
          ))}
        </select>
      </label>

      <label className="grid min-w-[10rem] flex-1 gap-1 text-[11px] text-muted-foreground">
        <span>搜索标题</span>
        <input
          type="search"
          data-testid="issue-search"
          value={query}
          maxLength={100}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="输入标题关键字"
          className={SELECT_CLASS}
        />
      </label>

      <button
        type="submit"
        className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Search className="h-4 w-4" aria-hidden="true" />
        搜索
      </button>
      <button
        type="button"
        data-testid="issue-filters-reset"
        onClick={() => {
          // Clear the typed-but-unsubmitted search too; value.q alone cannot
          // tell the local input to reset when it is already empty.
          setQuery("")
          onChange(EMPTY_ISSUE_FILTERS)
        }}
        className="inline-flex items-center gap-1.5 rounded-lg border border-border bg-card px-3 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <RotateCcw className="h-4 w-4" aria-hidden="true" />
        重置筛选
      </button>
    </form>
  )
}

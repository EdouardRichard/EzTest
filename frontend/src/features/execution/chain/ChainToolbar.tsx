"use client"

import { RotateCcw } from "lucide-react"
import { useState } from "react"

import { cn } from "@/lib/utils"

import {
  ISSUE_SEVERITIES,
  ISSUE_SEVERITY_TOKENS,
  ISSUE_STATUS_TOKENS,
  ISSUE_STATUSES,
  RESULT_STATUS_TOKENS,
} from "./chain-tokens"
import { CHAIN_RESULT_STATUS_FILTERS } from "./chain-types"

/** Every URL/server filter plus the two view options of the chain toolbar. */
export type ChainToolbarValue = {
  requirementId: string
  resultStatus: string
  abnormalOnly: boolean
  issueSeverity: string
  issueStatus: string
  includeSupplement: boolean
  revalidationOnly: boolean
  q: string
  follow: boolean
  listView: boolean
}

/** Initial/reset value of the whole toolbar (spec §5.5 defaults). */
export const CHAIN_TOOLBAR_DEFAULTS: ChainToolbarValue = {
  requirementId: "",
  resultStatus: "",
  abnormalOnly: false,
  issueSeverity: "",
  issueStatus: "",
  includeSupplement: true,
  revalidationOnly: false,
  q: "",
  follow: false,
  listView: false,
}

export type ChainToolbarRequirementOption = { id: string; title: string }

export type ChainToolbarProps = {
  value: ChainToolbarValue
  onChange: (next: ChainToolbarValue) => void
  requirementOptions?: readonly ChainToolbarRequirementOption[]
  /** Batch scope lives beside the filters (`current` by default, `all` optional). */
  batchScope?: "current" | "all"
  batchId?: string
  onBatchScopeChange?: (scope: "current" | "all") => void
  className?: string
}

const FIELD_LABEL_CLASS =
  "flex min-h-11 flex-col justify-center text-[10px] font-medium text-muted-foreground"
const FIELD_CLASS =
  "mt-0.5 min-h-9 w-full rounded-lg border border-border bg-input px-2 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring"
const BUTTON_CLASS =
  "inline-flex min-h-11 items-center gap-1.5 rounded-lg border border-border bg-card px-3 text-xs font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const TOGGLE_CLASS =
  "inline-flex min-h-11 items-center gap-1.5 rounded-lg border px-3 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
const TOGGLE_OFF_CLASS = "border-border bg-card text-foreground hover:bg-muted"
const TOGGLE_ON_CLASS = "border-primary/40 bg-primary/10 text-primary"
const CHECKBOX_LABEL_CLASS =
  "flex min-h-11 items-center gap-1.5 rounded-lg border border-border bg-card px-2.5 text-xs font-medium text-foreground"

/**
 * Overlay toolbar of the exploration chain (spec §5.4/§5.5).
 *
 * Fully controlled: it never owns filter state, it reports the next value set
 * through `onChange`, and the composition layer maps those values onto the
 * existing `use-exploration-chain` query. All buttons are at least 44px tall
 * for touch (spec §5.5) and every control is a native form element so screen
 * readers and keyboards work without custom ARIA.
 */
export function ChainToolbar({
  value,
  onChange,
  requirementOptions = [],
  batchScope = "current",
  batchId = "",
  onBatchScopeChange,
  className,
}: ChainToolbarProps) {
  // Below `lg` the toolbar collapses to a small "筛选" button, so the full
  // filter panel never covers most of the 60vh canvas on a phone.
  const [filtersOpen, setFiltersOpen] = useState(false)

  function patch(next: Partial<ChainToolbarValue>): void {
    onChange({ ...value, ...next })
  }

  return (
    <div
      data-testid="chain-toolbar"
      role="toolbar"
      aria-label="链路筛选与视图"
      className={cn(
        "rounded-2xl border border-border bg-card/95 p-2 text-xs shadow-lg backdrop-blur",
        className,
      )}
    >
      <div className="flex items-center justify-between gap-2 lg:hidden">
        <span className="text-[10px] font-medium text-muted-foreground">筛选与视图</span>
        <button
          type="button"
          data-testid="chain-toolbar-filter-toggle"
          aria-expanded={filtersOpen}
          aria-controls="chain-toolbar-filters"
          onClick={() => setFiltersOpen((open) => !open)}
          className={cn(TOGGLE_CLASS, TOGGLE_OFF_CLASS, "lg:hidden")}
        >
          {filtersOpen ? "收起筛选" : "筛选"}
        </button>
      </div>
      <div
        id="chain-toolbar-filters"
        data-testid="chain-toolbar-filters"
        className={cn(
          "mt-1.5 flex flex-wrap items-end gap-1.5 lg:mt-0",
          !filtersOpen && "hidden lg:flex",
        )}
      >
        <label className={FIELD_LABEL_CLASS}>
          批次
          <select
            data-testid="chain-batch-filter"
            value={batchScope}
            onChange={(event) =>
              onBatchScopeChange?.(event.target.value === "all" ? "all" : "current")
            }
            className={FIELD_CLASS}
          >
            <option value="current" disabled={batchId === ""}>
              当前批次
            </option>
            <option value="all">全部批次</option>
          </select>
        </label>

        <label className={FIELD_LABEL_CLASS}>
          需求
          <select
            data-testid="exploration-filter-requirement"
            value={value.requirementId}
            onChange={(event) => patch({ requirementId: event.target.value })}
            className={FIELD_CLASS}
          >
            <option value="">全部需求</option>
            {requirementOptions.map((option) => (
              <option key={option.id} value={option.id}>
                {option.title}
              </option>
            ))}
          </select>
        </label>

        <label className={FIELD_LABEL_CLASS}>
          结果状态
          <select
            data-testid="exploration-filter-status"
            value={value.resultStatus}
            onChange={(event) => patch({ resultStatus: event.target.value })}
            className={FIELD_CLASS}
          >
            <option value="">全部状态</option>
            {CHAIN_RESULT_STATUS_FILTERS.map((status) => (
              <option key={status} value={status}>
                {RESULT_STATUS_TOKENS[status].label}
              </option>
            ))}
          </select>
        </label>

        <label className={FIELD_LABEL_CLASS}>
          问题严重级
          <select
            data-testid="exploration-filter-issue-severity"
            value={value.issueSeverity}
            onChange={(event) => patch({ issueSeverity: event.target.value })}
            className={FIELD_CLASS}
          >
            <option value="">全部严重级</option>
            {ISSUE_SEVERITIES.map((severity) => (
              <option key={severity} value={severity}>
                {ISSUE_SEVERITY_TOKENS[severity].label}
              </option>
            ))}
          </select>
        </label>

        <label className={FIELD_LABEL_CLASS}>
          问题状态
          <select
            data-testid="exploration-filter-issue-status"
            value={value.issueStatus}
            onChange={(event) => patch({ issueStatus: event.target.value })}
            className={FIELD_CLASS}
          >
            <option value="">全部问题状态</option>
            {ISSUE_STATUSES.map((status) => (
              <option key={status} value={status}>
                {ISSUE_STATUS_TOKENS[status].label}
              </option>
            ))}
          </select>
        </label>

        <label className={cn(FIELD_LABEL_CLASS, "min-w-36 flex-1")}>
          搜索需求/用例
          <input
            type="search"
            data-testid="exploration-filter-search"
            value={value.q}
            onChange={(event) => patch({ q: event.target.value })}
            placeholder="标题关键字"
            className={FIELD_CLASS}
          />
        </label>

        <label className={CHECKBOX_LABEL_CLASS}>
          <input
            type="checkbox"
            data-testid="exploration-filter-abnormal"
            checked={value.abnormalOnly}
            onChange={(event) => patch({ abnormalOnly: event.target.checked })}
            className="h-4 w-4 rounded border-border"
          />
          仅异常
        </label>

        <label className={CHECKBOX_LABEL_CLASS}>
          <input
            type="checkbox"
            data-testid="exploration-filter-supplement"
            checked={value.includeSupplement}
            onChange={(event) => patch({ includeSupplement: event.target.checked })}
            className="h-4 w-4 rounded border-border"
          />
          包含 AI 补充
        </label>

        <label className={CHECKBOX_LABEL_CLASS}>
          <input
            type="checkbox"
            data-testid="exploration-filter-revalidation"
            checked={value.revalidationOnly}
            onChange={(event) => patch({ revalidationOnly: event.target.checked })}
            className="h-4 w-4 rounded border-border"
          />
          仅需重验
        </label>

        <button
          type="button"
          data-testid="chain-toolbar-follow"
          aria-pressed={value.follow}
          onClick={() => patch({ follow: !value.follow })}
          className={cn(TOGGLE_CLASS, value.follow ? TOGGLE_ON_CLASS : TOGGLE_OFF_CLASS)}
        >
          跟随执行
        </button>

        <button
          type="button"
          data-testid="chain-toolbar-list"
          aria-pressed={value.listView}
          onClick={() => patch({ listView: !value.listView })}
          className={cn(TOGGLE_CLASS, value.listView ? TOGGLE_ON_CLASS : TOGGLE_OFF_CLASS)}
        >
          列表视图
        </button>

        <button
          type="button"
          data-testid="chain-toolbar-reset"
          onClick={() => {
            // Batch scope is not part of ChainToolbarValue; resetting the rest
            // would silently leave "全部批次" active without this callback.
            onBatchScopeChange?.("current")
            onChange({ ...CHAIN_TOOLBAR_DEFAULTS })
          }}
          className={BUTTON_CLASS}
        >
          <RotateCcw className="h-3.5 w-3.5" aria-hidden="true" />
          重置
        </button>
      </div>
    </div>
  )
}

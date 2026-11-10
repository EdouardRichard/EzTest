"use client"

import type { ReactNode } from "react"

import { cn } from "@/components/ui/cn"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import type { CaseRunVerdict } from "@/features/execution/types"

import { ALL_FILTER_VALUE, type CaseFilterValues } from "./layout"
import type { CaseVersionStatus } from "./types"

export type CaseFilterOption = { value: string; label: string }

export type CaseFilterKey = keyof CaseFilterValues

export type CaseFiltersProps = {
  values: CaseFilterValues
  requirementOptions: CaseFilterOption[]
  scenarioOptions: CaseFilterOption[]
  /** Result filtering needs execution verdicts; without them the select is disabled. */
  resultFilterEnabled: boolean
  onChange: (key: CaseFilterKey, value: string) => void
  /**
   * Short viewports (D-01): keep all five controls on one compact row, move
   * the labels to screen-reader-only and shorten the controls to `h-8`.
   * Defaults to false so the approved full layout is unchanged.
   */
  compact?: boolean
}

type FilterFieldProps = {
  id: string
  label: string
  className?: string
  compact: boolean
  children: ReactNode
}

/**
 * Wraps a filter control. The non-compact branch is the approved `Field`
 * layout; the compact branch keeps the same accessible name (sr-only label)
 * without spending a label row per control.
 */
function FilterField({ id, label, className, compact, children }: FilterFieldProps) {
  if (!compact) {
    return (
      <Field id={id} label={label} className={className}>
        {children}
      </Field>
    )
  }
  return (
    <div className={cn("flex min-w-0 items-center [&_input]:h-8 [&_select]:h-8", className)}>
      <label htmlFor={id} className="sr-only">
        {label}
      </label>
      {children}
    </div>
  )
}

/** A graph case is never `superseded`, so that status has no filter option. */
const STATUS_LABELS: Record<Exclude<CaseVersionStatus, "superseded">, string> = {
  draft: "草案",
  confirmed: "已确认",
  stale: "已过期",
}

const RESULT_LABELS: Record<CaseRunVerdict, string> = {
  passed: "通过",
  failed: "失败",
  blocked: "阻塞",
  inconclusive: "未定",
}

const STATUS_OPTIONS: CaseFilterOption[] = [
  { value: ALL_FILTER_VALUE, label: "全部状态" },
  ...Object.entries(STATUS_LABELS).map(([value, label]) => ({ value, label })),
]

const RESULT_OPTIONS: CaseFilterOption[] = [
  { value: ALL_FILTER_VALUE, label: "全部结果" },
  ...Object.entries(RESULT_LABELS).map(([value, label]) => ({ value, label })),
]

/**
 * Requirement / scenario / status / execution-result filters plus search.
 * Filtering is client-side over the graph snapshot; search only highlights and
 * fits the view, it never removes nodes (spec §9.3).
 */
export function CaseFilters({
  values,
  requirementOptions,
  scenarioOptions,
  resultFilterEnabled,
  onChange,
  compact = false,
}: CaseFiltersProps) {
  return (
    <section
      data-testid="design-case-filters"
      aria-label="用例筛选"
      className={cn(
        "flex shrink-0 flex-wrap border-b border-border bg-card",
        compact ? "items-center gap-1.5 px-2 py-1" : "items-end gap-2 px-3 py-2",
      )}
    >
      <FilterField
        id="design-search-field"
        label="搜索"
        compact={compact}
        className="min-w-[10rem] flex-1"
      >
        <Input
          id="design-search-field"
          data-testid="design-search"
          type="search"
          value={values.query}
          maxLength={100}
          placeholder="需求 / 场景 / 用例标题"
          onChange={(event) => onChange("query", event.target.value)}
        />
      </FilterField>

      <FilterField
        id="design-filter-requirement-field"
        label="需求"
        compact={compact}
        className="w-44"
      >
        <Select
          id="design-filter-requirement-field"
          data-testid="design-filter-requirement"
          value={values.requirementId}
          onChange={(event) => onChange("requirementId", event.target.value)}
        >
          <option value={ALL_FILTER_VALUE}>全部需求</option>
          {requirementOptions.map((option) => (
            <option
              key={option.value}
              value={option.value}
              data-testid={`design-filter-requirement-option-${option.value}`}
            >
              {option.label}
            </option>
          ))}
        </Select>
      </FilterField>

      <FilterField
        id="design-filter-scenario-field"
        label="场景"
        compact={compact}
        className="w-44"
      >
        <Select
          id="design-filter-scenario-field"
          data-testid="design-filter-scenario"
          value={values.scenarioId}
          onChange={(event) => onChange("scenarioId", event.target.value)}
        >
          <option value={ALL_FILTER_VALUE}>全部场景</option>
          {scenarioOptions.map((option) => (
            <option
              key={option.value}
              value={option.value}
              data-testid={`design-filter-scenario-option-${option.value}`}
            >
              {option.label}
            </option>
          ))}
        </Select>
      </FilterField>

      <FilterField id="design-filter-status-field" label="状态" compact={compact} className="w-32">
        <Select
          id="design-filter-status-field"
          data-testid="design-filter-status"
          value={values.status}
          onChange={(event) => onChange("status", event.target.value)}
        >
          {STATUS_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </FilterField>

      <FilterField
        id="design-filter-result-field"
        label="执行结果"
        compact={compact}
        className="w-32"
      >
        <Select
          id="design-filter-result-field"
          data-testid="design-filter-result"
          value={values.result}
          disabled={!resultFilterEnabled}
          title={resultFilterEnabled ? undefined : "暂无执行结果，执行后可筛选"}
          onChange={(event) => onChange("result", event.target.value)}
        >
          {RESULT_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </FilterField>
    </section>
  )
}

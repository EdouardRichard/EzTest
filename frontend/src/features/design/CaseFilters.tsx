"use client"

import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"

import { ALL_FILTER_VALUE, type CaseFilterValues } from "./layout"

export type CaseFilterOption = { value: string; label: string }

export type CaseFilterKey = keyof CaseFilterValues

export type CaseFiltersProps = {
  values: CaseFilterValues
  requirementOptions: CaseFilterOption[]
  scenarioOptions: CaseFilterOption[]
  onChange: (key: CaseFilterKey, value: string) => void
}

const STATUS_OPTIONS: CaseFilterOption[] = [
  { value: ALL_FILTER_VALUE, label: "全部状态" },
  { value: "draft", label: "草案" },
  { value: "confirmed", label: "已确认" },
  { value: "stale", label: "已过期" },
]

const RESULT_OPTIONS: CaseFilterOption[] = [
  { value: ALL_FILTER_VALUE, label: "全部结果" },
  { value: "passed", label: "通过" },
  { value: "failed", label: "失败" },
  { value: "blocked", label: "阻塞" },
  { value: "inconclusive", label: "未定" },
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
  onChange,
}: CaseFiltersProps) {
  return (
    <section
      data-testid="design-case-filters"
      aria-label="用例筛选"
      className="flex shrink-0 flex-wrap items-end gap-2 border-b border-border bg-card px-3 py-2"
    >
      <Field id="design-search-field" label="搜索" className="min-w-[10rem] flex-1">
        <Input
          id="design-search-field"
          data-testid="design-search"
          type="search"
          value={values.query}
          maxLength={100}
          placeholder="需求 / 场景 / 用例标题"
          onChange={(event) => onChange("query", event.target.value)}
        />
      </Field>

      <Field id="design-filter-requirement-field" label="需求" className="w-44">
        <Select
          id="design-filter-requirement-field"
          data-testid="design-filter-requirement"
          value={values.requirementId}
          onChange={(event) => onChange("requirementId", event.target.value)}
        >
          <option value={ALL_FILTER_VALUE}>全部需求</option>
          {requirementOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="design-filter-scenario-field" label="场景" className="w-44">
        <Select
          id="design-filter-scenario-field"
          data-testid="design-filter-scenario"
          value={values.scenarioId}
          onChange={(event) => onChange("scenarioId", event.target.value)}
        >
          <option value={ALL_FILTER_VALUE}>全部场景</option>
          {scenarioOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="design-filter-status-field" label="状态" className="w-32">
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
      </Field>

      <Field id="design-filter-result-field" label="执行结果" className="w-32">
        <Select
          id="design-filter-result-field"
          data-testid="design-filter-result"
          value={values.result}
          onChange={(event) => onChange("result", event.target.value)}
        >
          {RESULT_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>
    </section>
  )
}

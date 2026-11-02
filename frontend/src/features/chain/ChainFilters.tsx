"use client"

import { cn } from "@/components/ui/cn"
import { Field } from "@/components/ui/Field"
import { Input } from "@/components/ui/Input"
import { Select } from "@/components/ui/Select"
import type { RunWave } from "@/features/execution/types"

export type ChainFilterValues = {
  sutId: string
  pageId: string
  waveId: string
  status: string
  result: string
  q: string
  abnormalOnly: "all" | "true"
  issueStatus: string
  issueSeverity: string
  requirementLimit: string
}

export type ChainFilterOption = {
  value: string
  label: string
}

export type ChainFilterKey = keyof ChainFilterValues

export type ChainFiltersProps = {
  values: ChainFilterValues
  /** Immediate q text; the parent commits it after the 300ms debounce. */
  qInput: string
  onQInputChange: (value: string) => void
  sutOptions: ChainFilterOption[]
  pageOptions: ChainFilterOption[]
  waves: RunWave[]
  onChange: (key: ChainFilterKey, value: string) => void
}

const STATUS_OPTIONS: ChainFilterOption[] = [
  { value: "open", label: "待确认" },
  { value: "draft", label: "草案" },
  { value: "clarified", label: "已澄清" },
  { value: "confirmed", label: "已确认" },
  { value: "active", label: "活跃" },
  { value: "planned", label: "已计划" },
  { value: "running", label: "执行中" },
  { value: "paused", label: "已暂停" },
  { value: "pending", label: "等待中" },
  { value: "done", label: "已完成" },
  { value: "passed", label: "通过" },
  { value: "failed", label: "失败" },
  { value: "blocked", label: "阻塞" },
  { value: "inconclusive", label: "未定" },
  { value: "skipped", label: "已跳过" },
  { value: "interrupted", label: "已中断" },
  { value: "cancelled", label: "已取消" },
  { value: "superseded", label: "已替代" },
  { value: "merged", label: "已合并" },
  { value: "dismissed", label: "已驳回" },
  { value: "obsolete", label: "已废弃" },
  { value: "reopened", label: "已重开" },
  { value: "stale", label: "已过期" },
  { value: "published", label: "已发布" },
  { value: "closed", label: "已闭环" },
]

const RESULT_OPTIONS: ChainFilterOption[] = [
  { value: "passed", label: "通过" },
  { value: "failed", label: "失败" },
  { value: "blocked", label: "阻塞" },
  { value: "inconclusive", label: "未定" },
  { value: "skipped", label: "已跳过" },
  { value: "interrupted", label: "已中断" },
]

const ISSUE_STATUS_OPTIONS: ChainFilterOption[] = [
  { value: "open", label: "待确认" },
  { value: "confirmed", label: "已确认" },
  { value: "dismissed", label: "已驳回" },
  { value: "merged", label: "已合并" },
]

const ISSUE_SEVERITY_OPTIONS: ChainFilterOption[] = [
  { value: "major", label: "严重" },
  { value: "minor", label: "一般" },
  { value: "trivial", label: "轻微" },
]

const REQUIREMENT_LIMIT_OPTIONS: ChainFilterOption[] = [
  { value: "50", label: "50" },
  { value: "100", label: "100" },
  { value: "200", label: "200" },
  { value: "500", label: "500" },
]

/**
 * Chain filters (spec §8.3 / §10): legacy SUT/page/wave/status/result plus the
 * P6 q (debounced by the parent), abnormal-only, issue status/severity and
 * requirement limit. Filtering stays server-side so induced edges and counts
 * remain authoritative.
 */
export function ChainFilters({
  values,
  qInput,
  onQInputChange,
  sutOptions,
  pageOptions,
  waves,
  onChange,
}: ChainFiltersProps) {
  return (
    <section
      data-testid="chain-filters"
      aria-label="链路筛选"
      className="flex flex-wrap items-end gap-2"
    >
      <Field id="chain-filter-q" label="搜索" className="min-w-[12rem] flex-1">
        <Input
          id="chain-filter-q"
          type="search"
          value={qInput}
          maxLength={100}
          placeholder="需求 / 场景 / 用例标题"
          onChange={(event) => onQInputChange(event.target.value)}
        />
      </Field>

      <Field id="chain-filter-abnormal" label="仅异常" className="w-24">
        <button
          type="button"
          id="chain-filter-abnormal"
          data-testid="chain-filter-abnormal"
          aria-pressed={values.abnormalOnly === "true"}
          aria-label="仅异常"
          onClick={() => onChange("abnormalOnly", values.abnormalOnly === "true" ? "all" : "true")}
          className={cn(
            "h-9 w-full rounded-md border px-3 text-sm transition-colors",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            "motion-reduce:transition-none",
            values.abnormalOnly === "true"
              ? "border-destructive/50 bg-destructive/10 text-destructive"
              : "border-border bg-input text-foreground",
          )}
        >
          {values.abnormalOnly === "true" ? "仅异常" : "全部"}
        </button>
      </Field>

      <Field id="chain-filter-issue-status" label="问题状态" className="w-32">
        <Select
          id="chain-filter-issue-status"
          value={values.issueStatus}
          onChange={(event) => onChange("issueStatus", event.target.value)}
        >
          <option value="all">全部问题</option>
          {ISSUE_STATUS_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="chain-filter-issue-severity" label="严重级" className="w-28">
        <Select
          id="chain-filter-issue-severity"
          value={values.issueSeverity}
          onChange={(event) => onChange("issueSeverity", event.target.value)}
        >
          <option value="all">全部严重级</option>
          {ISSUE_SEVERITY_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="chain-filter-sut" label="SUT" className="w-40">
        <Select
          id="chain-filter-sut"
          value={values.sutId}
          onChange={(event) => onChange("sutId", event.target.value)}
        >
          <option value="all">全部 SUT</option>
          {sutOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="chain-filter-page" label="页面" className="w-40">
        <Select
          id="chain-filter-page"
          value={values.pageId}
          onChange={(event) => onChange("pageId", event.target.value)}
        >
          <option value="all">全部页面</option>
          {pageOptions.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="chain-filter-wave" label="波次" className="w-36">
        <Select
          id="chain-filter-wave"
          value={values.waveId}
          onChange={(event) => onChange("waveId", event.target.value)}
        >
          <option value="all">全部波次</option>
          {waves.map((wave) => (
            <option key={wave.id} value={wave.id}>
              #{wave.id}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="chain-filter-status" label="状态" className="w-32">
        <Select
          id="chain-filter-status"
          value={values.status}
          onChange={(event) => onChange("status", event.target.value)}
        >
          <option value="all">全部状态</option>
          {STATUS_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="chain-filter-result" label="结果" className="w-32">
        <Select
          id="chain-filter-result"
          value={values.result}
          onChange={(event) => onChange("result", event.target.value)}
        >
          <option value="all">全部结果</option>
          {RESULT_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="chain-filter-requirement-limit" label="需求上限" className="w-32">
        <Select
          id="chain-filter-requirement-limit"
          value={values.requirementLimit}
          onChange={(event) => onChange("requirementLimit", event.target.value)}
        >
          <option value="all">默认</option>
          {REQUIREMENT_LIMIT_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </Select>
      </Field>
    </section>
  )
}

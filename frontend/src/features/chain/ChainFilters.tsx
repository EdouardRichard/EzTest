"use client"

import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import type { RunWave } from "@/features/execution/types"

export type ChainFilterValues = {
  sutId: string
  pageId: string
  waveId: string
  status: string
  result: string
}

export type ChainFilterOption = {
  value: string
  label: string
}

export type ChainFilterKey = keyof ChainFilterValues

export type ChainFiltersProps = {
  values: ChainFilterValues
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
  { value: "running", label: "执行中" },
  { value: "paused", label: "已暂停" },
  { value: "done", label: "已完成" },
  { value: "passed", label: "通过" },
  { value: "failed", label: "失败" },
  { value: "blocked", label: "阻塞" },
  { value: "merged", label: "已合并" },
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
]

/**
 * Chain filters (spec §8.3): SUT/page/wave/status/result. Every change is sent
 * to the server; filtering never happens client-side, so induced edges and
 * counts stay authoritative.
 */
export function ChainFilters({
  values,
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
    </section>
  )
}

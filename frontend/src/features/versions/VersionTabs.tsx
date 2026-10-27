"use client"

import * as Tabs from "@radix-ui/react-tabs"
import type { ReactNode } from "react"

import { PlaceholderTab } from "./PlaceholderTab"

/** The nine P3 version-detail tabs (spec §9.1). */
export type VersionTabValue =
  | "requirements"
  | "overview"
  | "sessions"
  | "exploration"
  | "broadcast"
  | "results"
  | "issues"
  | "report"
  | "code"

export const VERSION_TABS: readonly { value: VersionTabValue; label: string }[] = [
  { value: "requirements", label: "需求与澄清" },
  { value: "overview", label: "总览" },
  { value: "sessions", label: "执行会话" },
  { value: "exploration", label: "探索链路" },
  { value: "broadcast", label: "实时播报" },
  { value: "results", label: "结果与证据" },
  { value: "issues", label: "问题" },
  { value: "report", label: "报告" },
  { value: "code", label: "代码与变更" },
]

const VERSION_TAB_VALUES = new Set<string>(VERSION_TABS.map((tab) => tab.value))

export function isVersionTab(value: string | null): value is VersionTabValue {
  return value !== null && VERSION_TAB_VALUES.has(value)
}

/** Tabs not delivered by T8; the owning phase comes from implementation-phases.md. */
const PLACEHOLDER_TABS: readonly {
  value: VersionTabValue
  label: string
  phase: string
  description: string
}[] = [
  {
    value: "sessions",
    label: "执行会话",
    phase: "P6",
    description: "版本内执行会话在 P6 阶段接入，将展示 Batch/CaseRun 与 Agent 会话记录。",
  },
  {
    value: "exploration",
    label: "探索链路",
    phase: "P6",
    description: "探索链路在 P6 阶段接入，将展示需求→用例→结果→问题的链路图。",
  },
  {
    value: "broadcast",
    label: "实时播报",
    phase: "P6",
    description: "实时播报在 P6 阶段接入，将展示全量执行事件时间线。",
  },
  {
    value: "results",
    label: "结果与证据",
    phase: "P6",
    description: "结果与证据在 P6 阶段接入，将展示步骤、断言与证据详情。",
  },
  {
    value: "issues",
    label: "问题",
    phase: "P7",
    description: "问题中心在 P7 阶段接入，将展示问题列表、确认与驳回。",
  },
  {
    value: "report",
    label: "报告",
    phase: "P6",
    description: "版本报告在 P6 Batch 1、P7 完整版接入，将展示批次报告与差异。",
  },
  {
    value: "code",
    label: "代码与变更",
    phase: "P4",
    description: "代码与变更在 P4 阶段接入，将展示 diff、影响用例与未映射变更。",
  },
]

const tabTriggerClass =
  "flex-1 whitespace-nowrap rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"

export type VersionTabsProps = {
  active: VersionTabValue
  onTabChange: (value: VersionTabValue) => void
  requirements: ReactNode
  overview: ReactNode
}

/**
 * The nine-tab version detail shell (spec §9.1): the functional requirements
 * slot, the overview skeleton, and seven clearly-labelled placeholders. Radix
 * Tabs provides keyboard navigation; the active tab itself is owned by the
 * container so it can stay in sync with `?tab=`.
 */
export function VersionTabs({ active, onTabChange, requirements, overview }: VersionTabsProps) {
  return (
    <Tabs.Root
      value={active}
      onValueChange={(value) => {
        if (isVersionTab(value)) onTabChange(value)
      }}
    >
      <Tabs.List aria-label="版本详情" className="flex flex-wrap gap-1 rounded-xl bg-muted p-1">
        {VERSION_TABS.map((tab) => (
          <Tabs.Trigger key={tab.value} value={tab.value} className={tabTriggerClass}>
            {tab.label}
          </Tabs.Trigger>
        ))}
      </Tabs.List>

      <Tabs.Content value="requirements" className="pt-4">
        {requirements}
      </Tabs.Content>

      <Tabs.Content value="overview" className="pt-4">
        {overview}
      </Tabs.Content>

      {PLACEHOLDER_TABS.map((tab) => (
        <Tabs.Content key={tab.value} value={tab.value} className="pt-4">
          <PlaceholderTab
            title={tab.label}
            phase={tab.phase}
            description={tab.description}
            onBackToRequirements={() => onTabChange("requirements")}
          />
        </Tabs.Content>
      ))}
    </Tabs.Root>
  )
}

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

/** Tabs still delivered by later phases; the owning phase comes from implementation-phases.md. */
const PLACEHOLDER_TABS: readonly {
  value: VersionTabValue
  label: string
  phase: string
  description: string
}[] = [
  {
    value: "issues",
    label: "问题",
    phase: "P7",
    description: "问题中心在 P7 阶段接入，将展示问题列表、确认与驳回。",
  },
]

const tabTriggerClass =
  "flex-1 whitespace-nowrap rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"

export type VersionTabsProps = {
  active: VersionTabValue
  onTabChange: (value: VersionTabValue) => void
  requirements: ReactNode
  overview: ReactNode
  /** Real 「代码与变更」 container (P4); required once the tab is opened. */
  code?: ReactNode
  /** Real execution tabs (P6); each is owned by the execution feature. */
  sessions?: ReactNode
  exploration?: ReactNode
  broadcast?: ReactNode
  results?: ReactNode
  report?: ReactNode
}

/**
 * The nine-tab version detail shell (spec §9.1): the functional requirements
 * slot, the overview skeleton, the five P6 execution slots, the P4 code-changes
 * slot and the one remaining P7 placeholder. Radix Tabs provides keyboard
 * navigation; the active tab itself is owned by the container so it can stay in
 * sync with `?tab=`.
 */
export function VersionTabs({
  active,
  onTabChange,
  requirements,
  overview,
  code,
  sessions,
  exploration,
  broadcast,
  results,
  report,
}: VersionTabsProps) {
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

      <Tabs.Content value="sessions" className="pt-4">
        {sessions}
      </Tabs.Content>

      <Tabs.Content value="exploration" className="pt-4">
        {exploration}
      </Tabs.Content>

      <Tabs.Content value="broadcast" className="pt-4">
        {broadcast}
      </Tabs.Content>

      <Tabs.Content value="results" className="pt-4">
        {results}
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

      <Tabs.Content value="report" className="pt-4">
        {report}
      </Tabs.Content>

      <Tabs.Content value="code" className="pt-4">
        {code}
      </Tabs.Content>
    </Tabs.Root>
  )
}

"use client"

import * as Tabs from "@radix-ui/react-tabs"
import type { ReactNode } from "react"

import { IssuesTab } from "@/features/delivery/components/IssuesTab"
import { ReportsTab } from "@/features/delivery/components/ReportsTab"
import { useReport, useReports } from "@/features/delivery/hooks/useReports"
import type { DeliveryStream } from "@/features/delivery/types"

/** The nine P3 version-detail tabs (spec §9.1); P7 keeps the count at nine. */
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

const tabTriggerClass =
  "flex-1 whitespace-nowrap rounded-lg px-3 py-2 text-sm font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[state=active]:bg-card data-[state=active]:text-foreground data-[state=active]:shadow-sm"

export type VersionTabsProps = {
  /** P3-owned version id passed to the delivery issues/report tabs. */
  versionId: string
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
  /** Version stream shared by the delivery tabs (spec §10.8). */
  stream?: DeliveryStream
  /** Controlled open issue of the `?issue=` deep link (T18). */
  issueId?: string
  onIssueChange?: (issueId: string) => void
  /** Controlled report revision of the `?report=` deep link (T18). */
  reportId?: string
  onReportChange?: (reportId: string) => void
  /** Controlled batch selection shared by the header and the report tab. */
  batchId?: string
  onBatchChange?: (batchId: string) => void
}

type ControlledIssuesTabProps = {
  versionId: string
  stream?: DeliveryStream
  issueId?: string
  onIssueChange?: (issueId: string) => void
}

/**
 * The issues tab is mounted only while its Radix content is active, so the
 * latest-report read that drives the AI-degraded hint is paid for only when the
 * tab is opened (T18 owns the actual issue projection).
 */
function ControlledIssuesTab({
  versionId,
  stream,
  issueId,
  onIssueChange,
}: ControlledIssuesTabProps) {
  const reports = useReports(versionId, { limit: 1 }, stream)
  const latestReportId = reports.reports[0]?.id ?? ""
  const latestReport = useReport(versionId, latestReportId, stream)
  return (
    <IssuesTab
      versionId={versionId}
      stream={stream}
      issueId={issueId}
      onIssueChange={onIssueChange}
      degraded={latestReport.report?.content.degraded === true}
    />
  )
}

/**
 * The nine-tab version detail shell (spec §9.1/§10.1): the functional
 * requirements slot, the overview, the five P6 execution slots, the P4 code
 * slot and the two real P7 delivery tabs (issues/reports) that replaced the
 * P3 placeholders. Radix Tabs provides keyboard navigation; the active tab is
 * owned by the container so it can stay in sync with `?tab=`.
 */
export function VersionTabs({
  versionId,
  active,
  onTabChange,
  requirements,
  overview,
  code,
  sessions,
  exploration,
  broadcast,
  results,
  stream,
  issueId,
  onIssueChange,
  reportId,
  onReportChange,
  batchId,
  onBatchChange,
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

      <Tabs.Content value="issues" className="pt-4">
        <ControlledIssuesTab
          versionId={versionId}
          stream={stream}
          issueId={issueId}
          onIssueChange={onIssueChange}
        />
      </Tabs.Content>

      <Tabs.Content value="report" className="pt-4">
        <ReportsTab
          versionId={versionId}
          stream={stream}
          batchId={batchId}
          onBatchChange={onBatchChange}
          reportId={reportId}
          onReportChange={onReportChange}
        />
      </Tabs.Content>

      <Tabs.Content value="code" className="pt-4">
        {code}
      </Tabs.Content>
    </Tabs.Root>
  )
}

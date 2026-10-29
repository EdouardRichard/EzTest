"use client"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import type { CaseRunStatus, FailureClass, ReportSummary as ReportSummaryView } from "../types"

/** The four Batch-1 verdicts shown as primary summary cards (spec §5.8, §9.3). */
export const FOUR_STATE_ORDER = ["passed", "failed", "blocked", "inconclusive"] as const

type CaseStatusMeta = {
  label: string
  variant: StatusBadgeVariant
}

/** Display metadata for every case-run status the report may surface. */
export const CASE_STATUS_META: Record<CaseRunStatus, CaseStatusMeta> = {
  pending: { label: "待执行", variant: "neutral" },
  running: { label: "执行中", variant: "info" },
  passed: { label: "通过", variant: "success" },
  failed: { label: "失败", variant: "danger" },
  blocked: { label: "阻塞", variant: "warning" },
  inconclusive: { label: "不确定", variant: "info" },
  skipped: { label: "跳过", variant: "neutral" },
  interrupted: { label: "中断", variant: "warning" },
}

/** Chinese labels for the five failure classes (spec §5.8). */
export const FAILURE_CLASS_LABELS: Record<FailureClass, string> = {
  application: "应用",
  environment: "环境",
  session: "会话",
  model: "模型",
  case: "用例",
}

/** Fixed class order so the summary never reorders between renders. */
const FAILURE_CLASS_ORDER: readonly FailureClass[] = [
  "application",
  "environment",
  "session",
  "model",
  "case",
]

/**
 * Human-readable duration used by both the summary cards and the case table.
 * Nothing below one second is rounded away, and long batches stay exact to the
 * second so two reports of the same run read identically.
 */
export function formatDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs <= 0) return "0s"
  if (durationMs < 1000) return `${Math.round(durationMs)}ms`
  if (durationMs < 60_000) return `${(durationMs / 1000).toFixed(1)}s`

  const totalSeconds = Math.round(durationMs / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  return `${minutes}分${seconds}秒`
}

type SummaryCardProps = {
  label: string
  value: string
  testId: string
  emphasis?: boolean
}

function SummaryCard({ label, value, testId, emphasis = false }: SummaryCardProps) {
  return (
    <div
      className={
        emphasis
          ? "rounded-xl border border-border bg-muted/40 px-3 py-2"
          : "rounded-xl border border-border bg-card px-3 py-2"
      }
    >
      <dt className="text-[11px] text-muted-foreground">{label}</dt>
      <dd data-testid={testId} className="mt-0.5 text-lg font-semibold tabular-nums">
        {value}
      </dd>
    </div>
  )
}

export type ReportSummaryProps = {
  summary: ReportSummaryView
}

/**
 * Report summary cards (spec §9.3): case count, four-state distribution, failure
 * classes, derived flaky count, total duration and evidence count. Counts come
 * straight from the server projection; this component only formats them.
 */
export function ReportSummary({ summary }: ReportSummaryProps) {
  const classes = FAILURE_CLASS_ORDER.filter(
    (failureClass) => (summary.by_failure_class[failureClass] ?? 0) > 0,
  )
    .map(
      (failureClass) =>
        `${FAILURE_CLASS_LABELS[failureClass]} ${summary.by_failure_class[failureClass] ?? 0}`,
    )
    .join(" · ")

  return (
    <section aria-label="报告摘要" data-testid="report-summary">
      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4">
        <SummaryCard
          label="用例总数"
          value={String(summary.case_count)}
          testId="report-summary-case-count"
          emphasis
        />
        {FOUR_STATE_ORDER.map((status) => (
          <SummaryCard
            key={status}
            label={CASE_STATUS_META[status].label}
            value={String(summary.by_status[status] ?? 0)}
            testId={`report-summary-${status}`}
          />
        ))}
        <SummaryCard
          label="疑似不稳定（flaky）"
          value={String(summary.flaky_case_ids.length)}
          testId="report-summary-flaky"
        />
        <SummaryCard
          label="总耗时"
          value={formatDuration(summary.duration_ms)}
          testId="report-summary-duration"
        />
        <SummaryCard
          label="证据数"
          value={String(summary.evidence_count)}
          testId="report-summary-evidence"
        />
        <div className="rounded-xl border border-border bg-card px-3 py-2">
          <dt className="text-[11px] text-muted-foreground">失败分类</dt>
          <dd data-testid="report-summary-classes" className="mt-0.5 text-sm font-medium">
            {classes === "" ? "无失败分类" : classes}
          </dd>
        </div>
      </dl>
    </section>
  )
}

/** Shared status pill so summary counts and case rows use identical copy. */
export function CaseStatusBadge({ status }: { status: CaseRunStatus }) {
  const meta = CASE_STATUS_META[status]
  return <StatusBadge variant={meta.variant}>{meta.label}</StatusBadge>
}

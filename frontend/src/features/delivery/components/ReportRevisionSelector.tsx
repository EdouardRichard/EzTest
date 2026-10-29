"use client"

import { labelOf, REPORT_STATUS_LABELS } from "../labels"
import type { ReportSummary } from "../types"

const SELECT_CLASS =
  "rounded-lg border border-border bg-card px-2.5 py-1.5 text-sm text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"

export type ReportRevisionSelectorProps = {
  /** Revisions of the active batch, newest first. */
  reports: ReportSummary[]
  value: string
  disabled: boolean
  onChange: (reportId: string) => void
}

/**
 * Revision dropdown of the report tab (spec §7.3): it lists every revision of
 * the active batch so the user can inspect an older revision or jump back to
 * the newest one. The page maps the change onto the `?report=` deep link.
 */
export function ReportRevisionSelector({
  reports,
  value,
  disabled,
  onChange,
}: ReportRevisionSelectorProps) {
  return (
    <label className="flex items-center gap-2 text-xs text-muted-foreground">
      <span>Revision</span>
      <select
        data-testid="report-revision-selector"
        value={value}
        onChange={(event) => onChange(event.target.value)}
        disabled={disabled || reports.length === 0}
        className={SELECT_CLASS}
      >
        {reports.map((report) => (
          <option key={report.id} value={report.id}>
            {`revision ${report.revision} · ${labelOf(REPORT_STATUS_LABELS, report.status)}`}
          </option>
        ))}
      </select>
    </label>
  )
}

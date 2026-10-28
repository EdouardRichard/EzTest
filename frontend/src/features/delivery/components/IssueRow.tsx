"use client"

import { StatusBadge } from "@/components/ui/StatusBadge"

import {
  ISSUE_CONFIDENCE_LABELS,
  ISSUE_SEVERITY_LABELS,
  ISSUE_STATUS_LABELS,
  issueStatusVariant,
  labelOf,
  severityVariant,
} from "../labels"
import type { IssueSummary } from "../types"

export type IssueRowProps = {
  issue: IssueSummary
  batchNo?: number
  onOpen: () => void
  onOpenEvidence: (evidenceId: string) => void
}

/**
 * One issue list row (spec §10.3): severity/title/status/confidence, the batch,
 * the evidence count and the code location, plus row-level deep links to the
 * evidence and code tabs.
 */
export function IssueRow({ issue, batchNo, onOpen, onOpenEvidence }: IssueRowProps) {
  const evidenceRefs = issue.refs.filter((ref) => ref.kind === "evidence")
  const firstEvidence = evidenceRefs[0]?.id ?? ""
  const firstCodeRef = issue.codeRefs[0]
  const codeLocation =
    firstCodeRef === undefined ? "—" : `${firstCodeRef.path}:${firstCodeRef.line}`
  const caseRunId = issue.caseRunId ?? issue.refs.find((ref) => ref.kind === "case_run")?.id ?? ""
  const versionBase = `/versions/${encodeURIComponent(issue.versionId)}`

  return (
    <li className="rounded-xl border border-border bg-card">
      <button
        type="button"
        data-testid={`issue-row-${issue.id}`}
        data-issue-id={issue.id}
        onClick={onOpen}
        className="grid w-full gap-1.5 rounded-xl px-3 py-2 text-left transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex flex-wrap items-center gap-2">
          <StatusBadge variant={severityVariant(issue.severity)}>
            {labelOf(ISSUE_SEVERITY_LABELS, issue.severity)}
          </StatusBadge>
          <span className="min-w-0 flex-1 truncate text-sm font-medium">{issue.title}</span>
          <StatusBadge variant={issueStatusVariant(issue.status)}>
            {labelOf(ISSUE_STATUS_LABELS, issue.status)}
          </StatusBadge>
          <StatusBadge variant="info" dot={false}>
            {labelOf(ISSUE_CONFIDENCE_LABELS, issue.confidence)}
          </StatusBadge>
        </span>
        <span className="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-muted-foreground">
          <span>{batchNo === undefined ? "Batch —" : `Batch #${batchNo}`}</span>
          <span>证据 {evidenceRefs.length}</span>
          <span className="font-mono">{codeLocation}</span>
          <span>复发 {issue.recurrenceCount} 次</span>
        </span>
      </button>
      <div className="flex flex-wrap gap-2 px-3 pb-2 text-[11px]">
        {firstEvidence === "" ? null : (
          <button
            type="button"
            data-testid={`issue-row-evidence-${issue.id}`}
            onClick={() => onOpenEvidence(firstEvidence)}
            className="text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
          >
            查看证据
          </button>
        )}
        {firstCodeRef === undefined ? null : (
          <a
            data-testid={`issue-row-code-${issue.id}`}
            href={`${versionBase}?tab=code&path=${encodeURIComponent(firstCodeRef.path)}&line=${firstCodeRef.line}`}
            className="font-mono text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
          >
            代码 {firstCodeRef.path}:{firstCodeRef.line}
          </a>
        )}
        {caseRunId === "" ? null : (
          <a
            data-testid={`issue-row-case-${issue.id}`}
            href={`${versionBase}?tab=results&caseRun=${encodeURIComponent(caseRunId)}`}
            className="text-sky-700 underline-offset-2 hover:underline dark:text-sky-300"
          >
            关联用例
          </a>
        )}
      </div>
    </li>
  )
}

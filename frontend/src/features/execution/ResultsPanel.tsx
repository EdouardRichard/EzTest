"use client"

import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { InlineError } from "@/components/ui/ErrorState"
import { ApiError } from "@/lib/api-client"
import { formatDuration } from "@/lib/format"

import { createIntent } from "./api"
import { CaseRunDetailView } from "./CaseRunDetail"
import { rerunPurpose } from "./intent-purpose"
import {
  CASE_RUN_STATUS_LABEL,
  CASE_RUN_STATUS_TONE,
  CASE_RUN_TERMINAL_STATUSES,
  failureClassLabel,
  terminalCauseLabel,
} from "./labels"
import type { CaseRun, CaseRunDetail } from "./types"

export type ResultsPanelProps = {
  versionId: string
  caseRuns: CaseRun[]
  selectedCaseRunId: string | null
  onSelect: (caseRunId: string) => void
  onPause: (caseRun: CaseRun) => void
  onResume: (caseRun: CaseRun) => void
  /** Called after a rerun intent was created so the workspace refetches. */
  onRerunComplete?: () => void
  /**
   * Opens the requirements tab on the closed requirement a retest was rejected
   * for, so the user can reopen it and retry.
   */
  onOpenRequirement?: (requirementId: string) => void
  detail: CaseRunDetail | null
  detailLoading: boolean
  detailError: unknown
  onRetryDetail: () => void
}

const PINNED_ORDER: Partial<Record<CaseRun["status"], number>> = {
  failed: 0,
  blocked: 1,
}

/**
 * The retest 409 for a closed requirement carries a stable
 * `details.reason=requirement_closed` plus the requirement ID. That is the one
 * error the UI can turn into a real recovery action instead of a dead end.
 */
function closedRequirementId(error: unknown): string | null {
  if (!(error instanceof ApiError) || error.details.reason !== "requirement_closed") {
    return null
  }
  const requirementId = error.details.requirement_id
  return typeof requirementId === "string" && requirementId.length > 0 ? requirementId : null
}

function pinOrder(status: CaseRun["status"]): number {
  return PINNED_ORDER[status] ?? 2
}

function runDuration(caseRun: CaseRun): string {
  if (caseRun.started_at === null || caseRun.finished_at === null) return "—"
  const start = Date.parse(caseRun.started_at)
  const end = Date.parse(caseRun.finished_at)
  if (Number.isNaN(start) || Number.isNaN(end)) return "—"
  return formatDuration(end - start)
}

function assertionProgress(caseRun: CaseRun): string | null {
  const total = caseRun.summary.assertion_total
  if (typeof total !== "number") return null
  const failed = caseRun.summary.assertion_failed ?? 0
  return `断言 ${Math.max(total - failed, 0)}/${total}`
}

/**
 * Right column of the execution workspace (spec §12.1): the case-run result
 * list with failed/blocked pinned on top, row expansion into the step detail,
 * case-level pause/resume and the rerun action. A failed verdict creates a
 * retest intent, while a passed case (no failed result) is rerun manually
 * (spec §5.8) so the API retest gate stays intact.
 */
export function ResultsPanel({
  versionId,
  caseRuns,
  selectedCaseRunId,
  onSelect,
  onPause,
  onResume,
  onRerunComplete,
  onOpenRequirement,
  detail,
  detailLoading,
  detailError,
  onRetryDetail,
}: ResultsPanelProps) {
  const [rerunConfirmId, setRerunConfirmId] = useState<string | null>(null)
  const [rerunningId, setRerunningId] = useState<string | null>(null)
  const [rerunError, setRerunError] = useState<unknown>(null)

  const ordered = [...caseRuns].sort(
    (left, right) => pinOrder(left.status) - pinOrder(right.status),
  )
  const closedRetestRequirement = closedRequirementId(rerunError)

  async function rerun(caseRun: CaseRun): Promise<void> {
    setRerunningId(caseRun.id)
    setRerunError(null)
    try {
      await createIntent(versionId, {
        case_version_id: caseRun.case_version_id,
        purpose: rerunPurpose(caseRun),
      })
      setRerunConfirmId(null)
      onRerunComplete?.()
    } catch (cause) {
      setRerunError(cause)
    } finally {
      setRerunningId(null)
    }
  }

  return (
    <section
      aria-label="用例结果"
      data-testid="results-panel"
      className="surface-card flex min-h-0 flex-col"
    >
      <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-3 py-2">
        <h2 className="text-sm font-semibold text-foreground">用例结果</h2>
        <span className="text-[11px] text-muted-foreground">{ordered.length} 条</span>
      </header>

      {rerunError !== null ? (
        <div className="border-b border-border px-3 py-2">
          <InlineError error={rerunError} />
          {closedRetestRequirement !== null ? (
            <div className="mt-1 flex flex-wrap items-center gap-2">
              <span className="text-[11px] text-muted-foreground">
                该用例所属需求已闭环，重跑前需先重新打开该需求。
              </span>
              {onOpenRequirement !== undefined ? (
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() => onOpenRequirement(closedRetestRequirement)}
                >
                  前往重新打开需求
                </Button>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}

      {ordered.length === 0 ? (
        <div className="p-3">
          <EmptyState
            title="暂无执行结果"
            description="确认用例后会自动入队组波，执行结果会显示在这里。"
            className="border-none bg-transparent px-3 py-8"
          />
        </div>
      ) : (
        <ul
          aria-label="用例结果列表"
          className="min-h-0 flex-1 divide-y divide-border overflow-y-auto"
        >
          {ordered.map((caseRun) => {
            const selected = selectedCaseRunId === caseRun.id
            const terminal = CASE_RUN_TERMINAL_STATUSES.includes(caseRun.status)
            const progress = assertionProgress(caseRun)
            const cause = terminalCauseLabel(caseRun.summary.terminal_cause)
            const usageTokens = caseRun.summary.usage?.total_tokens ?? 0
            return (
              <li
                key={caseRun.id}
                data-testid={`case-run-row-${caseRun.id}`}
                data-case-run-id={caseRun.id}
                className={selected ? "bg-muted/40" : undefined}
              >
                <div className="flex flex-wrap items-center gap-1.5 px-3 py-2">
                  <button
                    type="button"
                    aria-expanded={selected}
                    onClick={() => onSelect(caseRun.id)}
                    className="min-w-0 flex-1 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    <span className="flex flex-wrap items-center gap-1.5">
                      <span className="truncate text-xs font-semibold text-foreground">
                        {caseRun.title}
                      </span>
                      <Badge tone={CASE_RUN_STATUS_TONE[caseRun.status]}>
                        {CASE_RUN_STATUS_LABEL[caseRun.status]}
                      </Badge>
                      {caseRun.failure_class !== "" ? (
                        <span className="text-[11px] text-destructive">
                          {failureClassLabel(caseRun.failure_class)}
                        </span>
                      ) : null}
                      {cause !== null ? (
                        <span
                          data-testid={`case-run-cause-${caseRun.id}`}
                          className="text-[11px] text-warning"
                        >
                          原因 {cause}
                        </span>
                      ) : null}
                      <span className="ml-auto text-[10px] text-muted-foreground">
                        {selected ? "收起" : "展开"}
                      </span>
                    </span>
                    <span className="mt-1 flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
                      {caseRun.version_sut_ids.map((versionSutId) => (
                        <span key={versionSutId}>SUT {versionSutId}</span>
                      ))}
                      {progress !== null ? <span>{progress}</span> : null}
                      {usageTokens > 0 ? <span>{usageTokens} tokens</span> : null}
                      <span>证据 {caseRun.evidence_count}</span>
                      <span>{runDuration(caseRun)}</span>
                    </span>
                  </button>

                  <span className="flex shrink-0 items-center gap-1">
                    {caseRun.status === "running" || caseRun.status === "pending" ? (
                      <Button size="sm" variant="secondary" onClick={() => onPause(caseRun)}>
                        暂停
                      </Button>
                    ) : null}
                    {caseRun.status === "paused" ? (
                      <Button size="sm" onClick={() => onResume(caseRun)}>
                        恢复
                      </Button>
                    ) : null}
                    {terminal ? (
                      rerunConfirmId === caseRun.id ? (
                        <>
                          <span className="text-[11px] text-muted-foreground">确认重跑？</span>
                          <Button
                            size="sm"
                            disabled={rerunningId !== null}
                            onClick={() => {
                              void rerun(caseRun)
                            }}
                          >
                            {rerunningId === caseRun.id ? "提交中…" : "确认重跑"}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            disabled={rerunningId !== null}
                            onClick={() => setRerunConfirmId(null)}
                          >
                            取消重跑
                          </Button>
                        </>
                      ) : (
                        <Button
                          size="sm"
                          variant="secondary"
                          disabled={rerunningId !== null}
                          onClick={() => setRerunConfirmId(caseRun.id)}
                        >
                          重跑
                        </Button>
                      )
                    ) : null}
                  </span>
                </div>

                {selected ? (
                  <div className="border-t border-border px-3 py-2">
                    <CaseRunDetailView
                      versionId={versionId}
                      detail={detail}
                      loading={detailLoading}
                      error={detailError}
                      onRetry={onRetryDetail}
                    />
                  </div>
                ) : null}
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}

"use client"

import { useCallback, useEffect, useId, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"

import { listJudgments, overrideJudgment } from "./api"
import {
  canOverrideReviewState,
  REVIEW_STATES,
  reviewStateLabel,
  reviewStateTone,
  severityLabel,
  severityTone,
  verdictLabel,
} from "./judgment"
import type { JudgmentCaseView, JudgmentReportView, JudgmentView } from "./types"

export type JudgmentReportProps = {
  /** Report mode: fetch `GET /design/judgments` for this version. */
  versionId?: string
  /** Single-case mode: the case the override targets (with reviewState). */
  caseId?: string
  /**
   * Single-case mode: render just this review_state + its override action.
   * When omitted (undefined) the component runs in report mode.
   */
  reviewState?: string | null
  /** Admin-role gate for the override action (server re-checks). */
  isAdmin?: boolean
  requirementId?: string
  /** Pre-fetched report (controlled); skips the fetch when provided. */
  report?: JudgmentReportView | null
  /**
   * Single-case mode: test id for the review_state badge, so a host panel can
   * keep its own published badge selector (the case detail uses
   * `case-detail-review-state`).
   */
  badgeTestId?: string
  /** Called after a successful override so the caller refetches its snapshots. */
  onOverridden?: () => void
}

/**
 * Human override action (spec SC-E5). Renders only for an admin AND an
 * overridable review_state (escalated_fail / judge_unavailable); the server
 * re-checks both the role and the precondition. The reason is mandatory audit
 * metadata, mirroring the stage-reject dialog.
 */
function OverrideAction({
  versionId,
  caseId,
  reviewState,
  isAdmin,
  onOverridden,
}: {
  versionId?: string
  caseId?: string
  reviewState: string | null | undefined
  isAdmin: boolean
  onOverridden?: () => void
}) {
  const reasonId = useId()
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState("")
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  if (!isAdmin || !canOverrideReviewState(reviewState)) return null

  const trimmed = reason.trim()
  const submitDisabled =
    busy || trimmed.length === 0 || versionId === undefined || caseId === undefined

  async function submit(): Promise<void> {
    if (submitDisabled || versionId === undefined || caseId === undefined) return
    setBusy(true)
    setError(null)
    try {
      await overrideJudgment(versionId, caseId, trimmed)
      setOpen(false)
      setReason("")
      onOverridden?.()
    } catch (cause) {
      setError(cause)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Button
        size="sm"
        variant="danger"
        data-testid="override-btn"
        data-case-id={caseId}
        onClick={() => {
          setReason("")
          setError(null)
          setOpen(true)
        }}
      >
        人工决断
      </Button>
      <Dialog
        open={open}
        onClose={() => {
          if (!busy) setOpen(false)
        }}
        title="人工决断（override）"
        description="对升级失败或裁判不可用的用例做人工决断；决断绑定当前用例版本内容并写入审计，服务端仍会复核权限与前置条件。"
        footer={
          <>
            <Button variant="secondary" size="sm" disabled={busy} onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button
              size="sm"
              variant="danger"
              data-testid="override-submit"
              disabled={submitDisabled}
              onClick={() => {
                void submit()
              }}
            >
              {busy ? "提交中…" : "确认决断"}
            </Button>
          </>
        }
      >
        <Field
          id={reasonId}
          label="决断原因"
          required
          hint="必填（1–2000 字）；会写入 override 审计。"
        >
          <Textarea
            id={reasonId}
            value={reason}
            maxLength={2000}
            disabled={busy}
            placeholder="例如：人工复核覆盖与断言后确认通过"
            onChange={(event) => setReason(event.target.value)}
          />
        </Field>
        {error !== null ? <InlineError error={error} /> : null}
      </Dialog>
    </>
  )
}

/** The judge's AC coverage matrix is candidate content of unknown shape. */
function ACCoverageMatrix({ matrix }: { matrix: Record<string, unknown> }) {
  const entries = Object.entries(matrix)
  if (entries.length === 0) return null
  return (
    <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 text-[10px]">
      {entries.map(([key, value]) => (
        <div key={key} className="contents">
          <dt className="font-medium text-muted-foreground">{key}</dt>
          <dd className="text-foreground">
            {typeof value === "string" ? value : JSON.stringify(value)}
          </dd>
        </div>
      ))}
    </dl>
  )
}

function JudgmentCaseRow({
  item,
  versionId,
  isAdmin,
  onOverridden,
}: {
  item: JudgmentCaseView
  versionId?: string
  isAdmin: boolean
  onOverridden?: () => void
}) {
  const judgment: JudgmentView | null = item.judgment
  return (
    <article
      data-testid={`judgment-case-${item.case_id}`}
      className="space-y-1 rounded-lg border border-border bg-card px-3 py-2"
    >
      <header className="flex flex-wrap items-center gap-1.5">
        <span className="text-xs font-medium text-foreground">用例 {item.case_id}</span>
        <Badge tone={reviewStateTone(item.review_state)}>
          {reviewStateLabel(item.review_state)}
        </Badge>
        {judgment !== null ? (
          <Badge tone={judgment.verdict === "pass" ? "success" : "danger"}>
            {verdictLabel(judgment.verdict)}
          </Badge>
        ) : null}
        {judgment !== null ? (
          <span className="text-[10px] text-muted-foreground">第 {judgment.round} 轮</span>
        ) : null}
        <span className="ml-auto">
          <OverrideAction
            versionId={versionId}
            caseId={item.case_id}
            reviewState={item.review_state}
            isAdmin={isAdmin}
            onOverridden={onOverridden}
          />
        </span>
      </header>
      {judgment !== null ? (
        <>
          {judgment.findings.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">无裁判发现</p>
          ) : (
            <ul className="space-y-0.5">
              {judgment.findings.map((finding, index) => (
                <li
                  // The index is part of the key: a judge may legitimately report
                  // the same dimension/severity/message twice (e.g. one missing AC
                  // surfaced on two steps), and a content-only key then collides.
                  // biome-ignore lint/suspicious/noArrayIndexKey: findings belong to one immutable append-only judgment fact; the list is never reordered, inserted into or filtered in place, so the index is a stable discriminator for otherwise identical entries.
                  key={`${index}-${finding.dimension}-${finding.severity}-${finding.message}`}
                  className="flex flex-wrap items-center gap-1.5 text-[11px]"
                >
                  <Badge tone={severityTone(finding.severity)}>
                    {severityLabel(finding.severity)}
                  </Badge>
                  <span className="text-muted-foreground">{finding.dimension}</span>
                  <span className="text-foreground">{finding.message}</span>
                </li>
              ))}
            </ul>
          )}
          <ACCoverageMatrix matrix={judgment.ac_coverage_matrix} />
        </>
      ) : (
        <p className="text-[11px] text-muted-foreground">暂无裁判结论（待审查或裁判不可用）</p>
      )}
    </article>
  )
}

function ReportBody({
  versionId,
  requirementId,
  reportProp,
  isAdmin,
  onOverridden,
}: {
  versionId?: string
  requirementId?: string
  reportProp?: JudgmentReportView | null
  isAdmin: boolean
  onOverridden?: () => void
}) {
  const controlled = reportProp !== undefined
  const [fetched, setFetched] = useState<JudgmentReportView | null>(null)
  const [loading, setLoading] = useState(!controlled && versionId !== undefined)
  const [error, setError] = useState<unknown>(null)
  const [filter, setFilter] = useState<string>("all")
  const [reloadToken, setReloadToken] = useState(0)

  // biome-ignore lint/correctness/useExhaustiveDependencies: reloadToken refetches the report after a human override
  useEffect(() => {
    if (controlled || versionId === undefined) return
    let cancelled = false
    setLoading(true)
    setError(null)
    void listJudgments(versionId, requirementId).then(
      (next) => {
        if (cancelled) return
        setFetched(next)
        setLoading(false)
      },
      (cause: unknown) => {
        if (cancelled) return
        setError(cause)
        setLoading(false)
      },
    )
    return () => {
      cancelled = true
    }
  }, [controlled, versionId, requirementId, reloadToken])

  const handleOverridden = useCallback(() => {
    setReloadToken((token) => token + 1)
    onOverridden?.()
  }, [onOverridden])

  const report = controlled ? (reportProp ?? null) : fetched
  if (loading) return <LoadingCard label="正在加载裁判报告…" />
  if (error !== null) return <InlineError error={error} />
  if (report === null) return null

  const cases = report.cases
  const countFor = (state: string) => cases.filter((item) => item.review_state === state).length
  const filtered = filter === "all" ? cases : cases.filter((item) => item.review_state === filter)

  return (
    <section data-testid="judgment-report" aria-label="裁判报告" className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <Button
          size="sm"
          variant={filter === "all" ? "primary" : "secondary"}
          data-testid="judgment-filter-all"
          aria-pressed={filter === "all"}
          onClick={() => setFilter("all")}
        >
          全部 {cases.length}
        </Button>
        {REVIEW_STATES.map((state) => (
          <Button
            key={state}
            size="sm"
            variant={filter === state ? "primary" : "secondary"}
            data-testid={`judgment-filter-${state}`}
            aria-pressed={filter === state}
            onClick={() => setFilter(state)}
          >
            {reviewStateLabel(state)} {countFor(state)}
          </Button>
        ))}
      </div>
      {filtered.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">该审查状态下暂无用例</p>
      ) : (
        <div className="space-y-2">
          {filtered.map((item) => (
            <JudgmentCaseRow
              key={item.case_id}
              item={item}
              versionId={versionId}
              isAdmin={isAdmin}
              onOverridden={handleOverridden}
            />
          ))}
        </div>
      )}
    </section>
  )
}

/**
 * Judgment report (spec SC-E5, T18). Two modes:
 * - Report mode (`versionId`): fetches the per-case judgment report, groups /
 *   filters by review_state and shows verdict / findings / AC matrix / round.
 * - Single-case mode (`reviewState`): a compact review_state badge + override
 *   action, rendered by `CaseDetailPanel` for the selected case.
 *
 * The override action is admin-only AND state-gated (escalated_fail /
 * judge_unavailable); the server remains the authority on both.
 */
export function JudgmentReport({
  versionId,
  caseId,
  reviewState,
  isAdmin = false,
  requirementId,
  report,
  badgeTestId,
  onOverridden,
}: JudgmentReportProps) {
  if (reviewState !== undefined) {
    return (
      <section
        data-testid="judgment-report-single"
        aria-label="裁判审查状态"
        className="flex flex-wrap items-center gap-1.5"
      >
        <Badge tone={reviewStateTone(reviewState)} data-testid={badgeTestId}>
          {reviewStateLabel(reviewState)}
        </Badge>
        <OverrideAction
          versionId={versionId}
          caseId={caseId}
          reviewState={reviewState}
          isAdmin={isAdmin}
          onOverridden={onOverridden}
        />
      </section>
    )
  }

  return (
    <ReportBody
      versionId={versionId}
      requirementId={requirementId}
      reportProp={report}
      isAdmin={isAdmin}
      onOverridden={onOverridden}
    />
  )
}

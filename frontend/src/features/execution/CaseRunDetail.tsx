"use client"

import Link from "next/link"
import { useEffect, useRef, useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { Dialog } from "@/components/ui/Dialog"
import { ErrorState, InlineError } from "@/components/ui/ErrorState"
import { Field } from "@/components/ui/Field"
import { Select } from "@/components/ui/Select"
import { LoadingCard } from "@/components/ui/Spinner"
import { Textarea } from "@/components/ui/Textarea"
import { createManualIssue } from "@/features/delivery/api"
import type { IssueCategory, IssueSeverity } from "@/features/delivery/types"
import { ISSUE_CATEGORY_LABELS, ISSUE_SEVERITY_LABELS } from "@/features/delivery/types"
import { formatDuration } from "@/lib/format"

import { EvidenceViewer } from "./EvidenceViewer"
import {
  ASSERTION_KIND_LABEL,
  ASSERTION_VERDICT_LABEL,
  CASE_RUN_STATUS_LABEL,
  CASE_RUN_STATUS_TONE,
  CASE_RUN_TELEMETRY_TERMINAL_STATUSES,
  causeTextLabel,
  EVIDENCE_KIND_LABEL,
  failureClassLabel,
  isTelemetryPending,
  nextActionLabel,
  STEP_STATUS_LABEL,
  terminalCauseLabel,
} from "./labels"
import type { AssertionResult, CaseRun, CaseRunDetail, Evidence, StepResult } from "./types"

export type CaseRunDetailProps = {
  versionId: string
  detail: CaseRunDetail | null
  loading: boolean
  error: unknown
  onRetry: () => void
}

/**
 * One-shot refetches while a just-finished verdict's usage is still being
 * written: after 5s, then 15s later (at most twice). `case_run.finished` is
 * published before `Finalize` attaches usage and no event follows it, so the
 * snapshot has to be pulled again rather than pushed.
 */
const TELEMETRY_RETRY_DELAYS_MS = [5_000, 15_000] as const

function spanDuration(start: string | null, end: string | null): string {
  if (start === null || end === null) return "—"
  const startMs = Date.parse(start)
  const endMs = Date.parse(end)
  if (Number.isNaN(startMs) || Number.isNaN(endMs)) return "—"
  return formatDuration(endMs - startMs)
}

function AssertionRow({ assertion }: { assertion: AssertionResult }) {
  return (
    <div
      data-testid={`assertion-${assertion.step_seq}-${assertion.assertion_seq}`}
      className="rounded-md border border-border bg-muted/30 px-2 py-1.5 text-[11px]"
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="font-medium text-foreground">
          断言 {assertion.step_seq}.{assertion.assertion_seq}
        </span>
        <Badge tone="neutral">{ASSERTION_KIND_LABEL[assertion.kind]}</Badge>
        <Badge tone={assertion.verdict === "passed" ? "success" : "danger"}>
          {ASSERTION_VERDICT_LABEL[assertion.verdict]}
        </Badge>
      </div>
      <dl className="mt-1 space-y-0.5 text-muted-foreground">
        <div>
          <dt className="inline">期望：</dt>
          <dd className="inline text-foreground">{assertion.expected}</dd>
        </div>
        <div>
          <dt className="inline">实际：</dt>
          <dd className="inline text-foreground">{assertion.actual}</dd>
        </div>
        {assertion.reason.length > 0 ? <div>原因：{assertion.reason}</div> : null}
      </dl>
    </div>
  )
}

function StepRow({
  step,
  assertions,
  evidence,
  onOpenEvidence,
}: {
  step: StepResult
  assertions: AssertionResult[]
  evidence: Evidence[]
  onOpenEvidence: (item: Evidence) => void
}) {
  return (
    <li data-testid={`case-run-step-${step.seq}`} className="rounded-lg border border-border p-2.5">
      <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
        <span className="font-medium text-foreground">步骤 {step.seq}</span>
        <Badge
          tone={
            step.status === "passed" ? "success" : step.status === "failed" ? "danger" : "neutral"
          }
        >
          {STEP_STATUS_LABEL[step.status]}
        </Badge>
        {step.target_version_sut_id !== null ? (
          <span className="text-muted-foreground">SUT {step.target_version_sut_id}</span>
        ) : null}
        <span className="ml-auto text-muted-foreground">
          {spanDuration(step.started_at, step.finished_at)}
        </span>
      </div>

      {typeof step.observed.summary === "string" && step.observed.summary.length > 0 ? (
        <p className="mt-1 text-[11px] text-muted-foreground">{step.observed.summary}</p>
      ) : null}
      {step.error.length > 0 ? (
        <p role="alert" className="mt-1 text-[11px] text-destructive">
          {step.error}
        </p>
      ) : null}

      {assertions.length > 0 ? (
        <div className="mt-2 space-y-1.5">
          {assertions.map((assertion) => (
            <AssertionRow
              key={`${assertion.step_seq}-${assertion.assertion_seq}`}
              assertion={assertion}
            />
          ))}
        </div>
      ) : null}

      {evidence.length > 0 ? (
        <div className="mt-2 flex flex-wrap gap-1.5">
          {evidence.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => onOpenEvidence(item)}
              className="rounded-md border border-border bg-card px-2 py-1 text-[11px] text-foreground hover:bg-muted"
            >
              {EVIDENCE_KIND_LABEL[item.kind]} ·{" "}
              {item.filename.length > 0 ? item.filename : `#${item.id}`}
            </button>
          ))}
        </div>
      ) : null}
    </li>
  )
}

function issueHref(versionId: string, issueId: string): string {
  const params = new URLSearchParams({ tab: "issues", issue: issueId })
  return `/versions/${encodeURIComponent(versionId)}?${params.toString()}`
}

type CreateIssueDialogProps = {
  versionId: string
  run: CaseRun
  evidence: Evidence[]
  onClose: () => void
}

/**
 * v0.22 W2-4 manual issue entry from a failed run. The case_run (and up to five
 * evidence refs) are attached so the new issue keeps the execution deep links.
 */
function CreateIssueDialog({ versionId, run, evidence, onClose }: CreateIssueDialogProps) {
  const defaultSummary =
    causeTextLabel(run.summary.last_error) ?? causeTextLabel(run.summary.verdict_reason) ?? ""
  const evidenceIds = evidence.slice(0, 5).map((item) => item.id)

  const [title, setTitle] = useState(run.title)
  const [summary, setSummary] = useState(defaultSummary)
  const [category, setCategory] = useState<IssueCategory>("ui")
  const [severity, setSeverity] = useState<IssueSeverity>("major")
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [createdIssueId, setCreatedIssueId] = useState<string | null>(null)

  const disabled = creating || createdIssueId !== null || title.trim().length === 0

  async function submit(): Promise<void> {
    if (disabled) return
    setCreating(true)
    setError(null)
    try {
      const created = await createManualIssue(versionId, {
        title: title.trim(),
        summary: summary.trim(),
        category,
        severity,
        case_run_id: run.id,
        evidence_ids: evidenceIds,
        reason: `从用例运行 #${run.id} 手工创建`,
      })
      setCreatedIssueId(created.id)
    } catch (cause) {
      setError(cause)
    } finally {
      setCreating(false)
    }
  }

  return (
    <Dialog
      open
      onClose={onClose}
      title="创建问题"
      description={`运行 #${run.id} 的失败结果将作为 issue 的 case_run 引用。`}
      footer={
        <>
          <Button variant="secondary" size="sm" disabled={creating} onClick={onClose}>
            {createdIssueId === null ? "取消" : "关闭"}
          </Button>
          {createdIssueId === null ? (
            <Button
              size="sm"
              disabled={disabled}
              onClick={() => {
                void submit()
              }}
            >
              {creating ? "创建中…" : "创建问题"}
            </Button>
          ) : null}
        </>
      }
    >
      <div className="space-y-3">
        <Field id="manual-issue-title" label="标题" required>
          <input
            id="manual-issue-title"
            value={title}
            maxLength={500}
            disabled={creating || createdIssueId !== null}
            onChange={(event) => setTitle(event.target.value)}
            className="h-9 w-full rounded-md border border-border bg-input px-3 text-sm text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </Field>
        <Field id="manual-issue-summary" label="摘要" hint="可选；描述失败现象与影响。">
          <Textarea
            id="manual-issue-summary"
            value={summary}
            maxLength={4000}
            disabled={creating || createdIssueId !== null}
            onChange={(event) => setSummary(event.target.value)}
          />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field id="manual-issue-category" label="类别">
            <Select
              id="manual-issue-category"
              value={category}
              disabled={creating || createdIssueId !== null}
              onChange={(event) => setCategory(event.target.value as IssueCategory)}
            >
              {(Object.keys(ISSUE_CATEGORY_LABELS) as IssueCategory[]).map((value) => (
                <option key={value} value={value}>
                  {ISSUE_CATEGORY_LABELS[value]}
                </option>
              ))}
            </Select>
          </Field>
          <Field id="manual-issue-severity" label="严重度">
            <Select
              id="manual-issue-severity"
              value={severity}
              disabled={creating || createdIssueId !== null}
              onChange={(event) => setSeverity(event.target.value as IssueSeverity)}
            >
              {(Object.keys(ISSUE_SEVERITY_LABELS) as IssueSeverity[]).map((value) => (
                <option key={value} value={value}>
                  {ISSUE_SEVERITY_LABELS[value]}
                </option>
              ))}
            </Select>
          </Field>
        </div>
        {error !== null ? <InlineError error={error} /> : null}
        {createdIssueId !== null ? (
          <p className="text-xs text-success">
            问题已创建：{" "}
            <Link
              href={issueHref(versionId, createdIssueId)}
              className="text-primary underline-offset-2 hover:underline"
            >
              查看问题 #{createdIssueId}
            </Link>
          </p>
        ) : null}
      </div>
    </Dialog>
  )
}

/**
 * Expanded case-run snapshot (spec §12.1): step list in `seq` order with the
 * assertions and evidence nested under their step, plus the evidence dialog.
 * Step/assertion results come from the server snapshot; the UI never merges
 * them locally (spec §10.2).
 */
export function CaseRunDetailView({
  versionId,
  detail,
  loading,
  error,
  onRetry,
}: CaseRunDetailProps) {
  const [viewerEvidence, setViewerEvidence] = useState<Evidence | null>(null)
  const [issueDialogOpen, setIssueDialogOpen] = useState(false)

  // The parent passes an inline arrow (`() => setDetailToken(...)`), so its
  // identity changes on every render. Keep the latest callback in a ref but out
  // of the effect deps: otherwise each SSE refresh would clear and reschedule
  // the retry timer, possibly forever.
  const onRetryRef = useRef(onRetry)
  useEffect(() => {
    onRetryRef.current = onRetry
  }, [onRetry])

  const telemetryRetryRef = useRef(0)
  const telemetryRunIDRef = useRef<string | null>(null)
  const pendingRun = detail?.case_run ?? null
  const telemetryPending = pendingRun !== null && isTelemetryPending(pendingRun)

  // biome-ignore lint/correctness/useExhaustiveDependencies: switching to another pending run restarts the one-shot refetch schedule
  useEffect(() => {
    // Each pending run gets its own 5s/15s one-shot budget. Switching directly
    // from one just-finished run to another must not inherit the previous
    // run's retry count, which would shorten the second run's schedule.
    const pendingRunID = pendingRun?.id ?? null
    if (telemetryRunIDRef.current !== pendingRunID) {
      telemetryRetryRef.current = 0
      telemetryRunIDRef.current = pendingRunID
    }
    if (!telemetryPending) {
      telemetryRetryRef.current = 0
      return
    }
    const retry = (): void => {
      telemetryRetryRef.current += 1
      onRetryRef.current()
      if (telemetryRetryRef.current >= TELEMETRY_RETRY_DELAYS_MS.length) return
      timer = setTimeout(retry, TELEMETRY_RETRY_DELAYS_MS[telemetryRetryRef.current])
    }
    let timer = setTimeout(retry, TELEMETRY_RETRY_DELAYS_MS[0])
    return () => clearTimeout(timer)
  }, [telemetryPending, pendingRun?.id, pendingRun?.finished_at])

  if (loading) return <LoadingCard label="正在加载用例详情…" />

  if (error !== null) {
    return <ErrorState error={error} title="用例详情加载失败" onRetry={onRetry} />
  }

  if (detail === null) return null

  const run = detail.case_run
  const steps = [...detail.steps].sort((left, right) => left.seq - right.seq)
  const stepSeqs = new Set(steps.map((step) => step.seq))
  const orphanAssertions = detail.assertions.filter((item) => !stepSeqs.has(item.step_seq))
  const unattachedEvidence = detail.evidence.filter(
    (item) => item.step_seq === null || item.step_seq === undefined,
  )
  const terminalCause = terminalCauseLabel(run.summary.terminal_cause)
  // `verdict_reason`/`last_error` may still carry a raw stable enum on legacy
  // rows; localize a bare code before deciding whether there is prose to show.
  const verdictReason = causeTextLabel(run.summary.verdict_reason) ?? ""
  const lastError = causeTextLabel(run.summary.last_error) ?? ""
  const nextAction =
    nextActionLabel(run.summary.next_action) ??
    (run.status === "blocked" ? "先解决阻塞条件（账号/环境/数据），再重跑用例" : null)
  // `usage` is nullable on the wire (v0.22 audit §2.5-2): a terminal row settled
  // before telemetry persisted answers `null`, not an object. Never read through
  // it without checking; state "遥测不可用" instead of crashing the execution tab.
  const usage = run.summary.usage ?? null
  const hasUsage =
    usage !== null && typeof usage.total_tokens === "number" && usage.total_tokens > 0
  // The persisted explanation belongs to every terminal cause (budget/agent
  // stop, blocked, failure-derived); the inconclusive fallback keeps older rows
  // that only carried last_error visible. A blocked row without last_error falls
  // back to its verdict reason so the blocker is never hidden.
  const terminalDetail =
    terminalCause !== null || run.status === "inconclusive" || run.status === "blocked"
      ? lastError.length > 0
        ? lastError
        : run.status === "blocked"
          ? (terminalCauseLabel(verdictReason) ?? verdictReason)
          : ""
      : ""

  return (
    <div data-testid="case-run-detail" className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
        <span data-testid="case-run-status">
          <Badge tone={CASE_RUN_STATUS_TONE[run.status]}>{CASE_RUN_STATUS_LABEL[run.status]}</Badge>
        </span>
        <span data-testid="case-run-duration" className="text-muted-foreground">
          时长 {spanDuration(run.started_at, run.finished_at)}
        </span>
        <span className="text-muted-foreground">尝试 {run.attempts} 次</span>
        {run.failure_class !== "" ? (
          <span className="text-muted-foreground">
            失败分类 {failureClassLabel(run.failure_class)}
          </span>
        ) : null}
        {terminalCause !== null ? (
          <span data-testid="case-run-terminal-cause" className="font-medium text-warning">
            原因 {terminalCause}
          </span>
        ) : verdictReason.length > 0 ? (
          <span className="text-muted-foreground">原因 {verdictReason}</span>
        ) : null}
        {run.status === "failed" ? (
          <Button
            variant="secondary"
            size="sm"
            className="ml-auto"
            onClick={() => setIssueDialogOpen(true)}
          >
            创建问题
          </Button>
        ) : null}
      </div>

      {terminalDetail.length > 0 ? (
        <p
          data-testid="case-run-incomplete-detail"
          className="rounded-md border border-warning/30 bg-warning/10 px-2 py-1.5 text-[11px] leading-relaxed text-foreground"
        >
          {terminalDetail}
        </p>
      ) : null}

      {nextAction !== null ? (
        <p
          data-testid="case-run-next-action"
          className="text-[11px] leading-relaxed text-muted-foreground"
        >
          <span className="font-medium text-foreground">下一步：</span>
          {nextAction}
        </p>
      ) : null}

      {hasUsage && usage !== null ? (
        <p
          data-testid="case-run-usage"
          className="text-[11px] leading-relaxed text-muted-foreground"
        >
          <span className="font-medium text-foreground">用量：</span>
          {usage.total_tokens} tokens
          {usage.turns > 0 ? ` · ${usage.turns} 轮` : ""}
        </p>
      ) : telemetryPending ? (
        <p
          data-testid="case-run-telemetry-pending"
          className="text-[11px] leading-relaxed text-muted-foreground"
        >
          遥测写入中…
        </p>
      ) : CASE_RUN_TELEMETRY_TERMINAL_STATUSES.includes(run.status) ? (
        <p
          data-testid="case-run-telemetry-unavailable"
          className="text-[11px] leading-relaxed text-muted-foreground"
        >
          遥测不可用
        </p>
      ) : null}

      {steps.length === 0 ? (
        <p className="text-[11px] text-muted-foreground">该用例还没有步骤结果。</p>
      ) : (
        <ol aria-label="步骤结果" className="space-y-2">
          {steps.map((step) => (
            <StepRow
              key={step.seq}
              step={step}
              assertions={detail.assertions.filter((item) => item.step_seq === step.seq)}
              evidence={detail.evidence.filter((item) => item.step_seq === step.seq)}
              onOpenEvidence={setViewerEvidence}
            />
          ))}
        </ol>
      )}

      {unattachedEvidence.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {unattachedEvidence.map((item) => (
            <button
              key={item.id}
              type="button"
              onClick={() => setViewerEvidence(item)}
              className="rounded-md border border-border bg-card px-2 py-1 text-[11px] text-foreground hover:bg-muted"
            >
              {EVIDENCE_KIND_LABEL[item.kind]} ·{" "}
              {item.filename.length > 0 ? item.filename : `#${item.id}`}
            </button>
          ))}
        </div>
      ) : null}

      {orphanAssertions.length > 0 ? (
        <div className="space-y-1.5">
          {orphanAssertions.map((assertion) => (
            <AssertionRow
              key={`${assertion.step_seq}-${assertion.assertion_seq}`}
              assertion={assertion}
            />
          ))}
        </div>
      ) : null}

      <EvidenceViewer
        versionId={versionId}
        evidence={viewerEvidence}
        candidates={detail.evidence}
        open={viewerEvidence !== null}
        onClose={() => setViewerEvidence(null)}
      />

      {issueDialogOpen ? (
        <CreateIssueDialog
          versionId={versionId}
          run={run}
          evidence={detail.evidence}
          onClose={() => setIssueDialogOpen(false)}
        />
      ) : null}
    </div>
  )
}

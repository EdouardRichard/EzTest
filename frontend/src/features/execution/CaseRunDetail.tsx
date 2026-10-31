"use client"

import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { ErrorState } from "@/components/ui/ErrorState"
import { LoadingCard } from "@/components/ui/Spinner"
import { formatDuration } from "@/lib/format"

import { EvidenceViewer } from "./EvidenceViewer"
import {
  ASSERTION_KIND_LABEL,
  ASSERTION_VERDICT_LABEL,
  CASE_RUN_STATUS_LABEL,
  CASE_RUN_STATUS_TONE,
  EVIDENCE_KIND_LABEL,
  failureClassLabel,
  STEP_STATUS_LABEL,
} from "./labels"
import type { AssertionResult, CaseRunDetail, Evidence, StepResult } from "./types"

export type CaseRunDetailProps = {
  versionId: string
  detail: CaseRunDetail | null
  loading: boolean
  error: unknown
  onRetry: () => void
}

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

  return (
    <div data-testid="case-run-detail" className="space-y-2">
      <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
        <Badge tone={CASE_RUN_STATUS_TONE[run.status]}>{CASE_RUN_STATUS_LABEL[run.status]}</Badge>
        <span className="text-muted-foreground">尝试 {run.attempts} 次</span>
        {run.failure_class !== "" ? (
          <span className="text-muted-foreground">
            失败分类 {failureClassLabel(run.failure_class)}
          </span>
        ) : null}
        {typeof run.summary.verdict_reason === "string" && run.summary.verdict_reason.length > 0 ? (
          <span className="text-muted-foreground">原因 {run.summary.verdict_reason}</span>
        ) : null}
      </div>

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
        open={viewerEvidence !== null}
        onClose={() => setViewerEvidence(null)}
      />
    </div>
  )
}

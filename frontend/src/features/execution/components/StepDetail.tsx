import { Paperclip } from "lucide-react"

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import type { EvidenceView, StepResultView, StepStatus } from "../types"
import { evidenceKindLabel } from "./EvidenceViewer"

const STEP_STATUS_LABELS: Record<StepStatus, string> = {
  pending: "待执行",
  running: "执行中",
  passed: "通过",
  failed: "失败",
  interrupted: "已中断",
}

const STEP_STATUS_VARIANTS: Record<StepStatus, StatusBadgeVariant> = {
  pending: "neutral",
  running: "info",
  passed: "success",
  failed: "danger",
  interrupted: "warning",
}

export type EvidenceThumbProps = {
  evidenceId: string
  /** Evidence kind when the case-run summary knows it; "" triggers a meta read. */
  kind: string
  onOpen: (evidenceId: string) => void
}

/**
 * Clickable evidence chip (`evidence-thumb-{id}`, spec §9.4). It is used by both
 * step and assertion rows so every expected/actual pair can link to its proof.
 */
export function EvidenceThumb({ evidenceId, kind, onOpen }: EvidenceThumbProps) {
  return (
    <button
      type="button"
      data-testid={`evidence-thumb-${evidenceId}`}
      data-evidence-kind={kind === "" ? "unknown" : kind}
      data-evidence-id={evidenceId}
      title={`证据 ${evidenceId}`}
      aria-label={`查看证据 ${evidenceKindLabel(kind)} ${evidenceId}`}
      onClick={() => onOpen(evidenceId)}
      className="inline-flex items-center gap-1 rounded-full border border-border bg-muted/40 px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Paperclip className="h-3 w-3" aria-hidden="true" />
      {evidenceKindLabel(kind)}
    </button>
  )
}

export type StepDetailProps = {
  step: StepResultView
  /** Evidence summaries of the parent case run, used for kind labels. */
  evidence: EvidenceView[]
  onOpenEvidence: (evidenceId: string) => void
}

/**
 * One step row of the expanded case run (spec §9.3): action, expected vs actual,
 * status and each evidence id attached to the step.
 */
export function StepDetail({ step, evidence, onOpenEvidence }: StepDetailProps) {
  const kindById = new Map(evidence.map((item) => [item.id, item.kind]))

  return (
    <tr data-testid={`step-row-${step.seq}`} data-status={step.status} className="align-top">
      <td className="px-3 py-2 font-mono text-xs text-muted-foreground">{step.seq}</td>
      <td className="min-w-48 px-3 py-2">
        <p className="text-xs font-medium text-foreground">
          {step.action === "" ? "—" : step.action}
        </p>
        <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 text-xs">
          <dt className="text-muted-foreground">期望</dt>
          <dd className="whitespace-pre-wrap break-words">
            {step.expected === "" ? "—" : step.expected}
          </dd>
          <dt className="text-muted-foreground">实际</dt>
          <dd className="whitespace-pre-wrap break-words">
            {step.actual === "" ? "—" : step.actual}
          </dd>
        </dl>
      </td>
      <td className="px-3 py-2">
        <StatusBadge variant={STEP_STATUS_VARIANTS[step.status]}>
          {STEP_STATUS_LABELS[step.status]}
        </StatusBadge>
      </td>
      <td className="px-3 py-2">
        {step.evidence_ids.length === 0 ? (
          <span className="text-xs text-muted-foreground">—</span>
        ) : (
          <div className="flex flex-wrap gap-1">
            {step.evidence_ids.map((evidenceId) => (
              <EvidenceThumb
                key={evidenceId}
                evidenceId={evidenceId}
                kind={kindById.get(evidenceId) ?? ""}
                onOpen={onOpenEvidence}
              />
            ))}
          </div>
        )}
      </td>
    </tr>
  )
}

import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import type { AssertionVerdict, AssertionView, EvidenceView } from "../types"
import { EvidenceThumb } from "./StepDetail"

const VERDICT_LABELS: Record<AssertionVerdict, string> = {
  passed: "通过",
  failed: "失败",
  not_run: "未执行",
}

const VERDICT_VARIANTS: Record<AssertionVerdict, StatusBadgeVariant> = {
  passed: "success",
  failed: "danger",
  not_run: "neutral",
}

export type AssertionRowsProps = {
  assertions: AssertionView[]
  /** Evidence summaries of the parent case run, used for kind labels. */
  evidence: EvidenceView[]
  onOpenEvidence: (evidenceId: string) => void
}

/**
 * Assertion rows of the expanded case run (spec §9.3). Each row shows the
 * assertion kind/target, expected vs actual, verdict and its evidence ids so a
 * failed assertion is always traceable to a concrete artifact.
 */
export function AssertionRows({ assertions, evidence, onOpenEvidence }: AssertionRowsProps) {
  const kindById = new Map(evidence.map((item) => [item.id, item.kind]))

  return (
    <>
      {assertions.map((assertion) => (
        <tr
          key={assertion.id}
          data-testid={`assertion-row-${assertion.id}`}
          data-verdict={assertion.verdict}
          className="align-top"
        >
          <td className="min-w-40 px-3 py-2">
            <p className="text-xs font-medium text-foreground">
              {assertion.kind} · {assertion.target === "" ? "—" : assertion.target}
            </p>
            {assertion.step_seq === undefined ? null : (
              <p className="mt-0.5 text-[11px] text-muted-foreground">
                关联步骤 {assertion.step_seq}
              </p>
            )}
          </td>
          <td className="px-3 py-2">
            <dl className="grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 text-xs">
              <dt className="text-muted-foreground">期望</dt>
              <dd className="whitespace-pre-wrap break-words">
                {assertion.expected === "" ? "—" : assertion.expected}
              </dd>
              <dt className="text-muted-foreground">实际</dt>
              <dd className="whitespace-pre-wrap break-words">
                {assertion.actual === "" ? "—" : assertion.actual}
              </dd>
            </dl>
            {assertion.failure_reason === undefined || assertion.failure_reason === "" ? null : (
              <p className="mt-1 text-[11px] text-red-700 dark:text-red-300">
                {assertion.failure_reason}
              </p>
            )}
          </td>
          <td className="px-3 py-2">
            <StatusBadge variant={VERDICT_VARIANTS[assertion.verdict]}>
              {VERDICT_LABELS[assertion.verdict]}
            </StatusBadge>
          </td>
          <td className="px-3 py-2">
            {assertion.evidence_ids.length === 0 ? (
              <span className="text-xs text-muted-foreground">—</span>
            ) : (
              <div className="flex flex-wrap gap-1">
                {assertion.evidence_ids.map((evidenceId) => (
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
      ))}
    </>
  )
}

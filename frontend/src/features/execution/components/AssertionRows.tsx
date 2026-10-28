import { StatusBadge, type StatusBadgeVariant } from "@/components/ui/StatusBadge"

import { dbFailureLabel, isDbAssertionKind, parseDbAssertionActual } from "../db-result"
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

type AssertionRowProps = {
  assertion: AssertionView
  evidenceKindById: ReadonlyMap<string, string>
  onOpenEvidence: (evidenceId: string) => void
}

/** One assertion row; a db assertion also renders its row/elapsed/verdict meta. */
function AssertionRow({ assertion, evidenceKindById, onOpenEvidence }: AssertionRowProps) {
  const isDb = isDbAssertionKind(assertion.kind)
  const dbMeta = isDb ? parseDbAssertionActual(assertion.actual) : null
  const dbReason = isDb ? dbFailureLabel(assertion.failure_reason) : ""
  const rawReason = assertion.failure_reason ?? ""

  return (
    <tr
      data-testid={`assertion-row-${assertion.id}`}
      data-verdict={assertion.verdict}
      data-kind={assertion.kind}
      className="align-top"
    >
      <td className="min-w-40 px-3 py-2">
        <p className="flex flex-wrap items-center gap-1.5 text-xs font-medium text-foreground">
          {isDb ? (
            <span
              data-testid="assertion-db-badge"
              data-kind="db"
              className="inline-flex items-center rounded border border-sky-500/25 bg-sky-500/10 px-1.5 py-0.5 font-mono text-[10px] font-semibold uppercase text-sky-700 dark:text-sky-300"
            >
              db
            </span>
          ) : null}
          <span>
            {assertion.kind} · {assertion.target === "" ? "—" : assertion.target}
          </span>
        </p>
        {assertion.step_seq === undefined ? null : (
          <p className="mt-0.5 text-[11px] text-muted-foreground">关联步骤 {assertion.step_seq}</p>
        )}
        {dbMeta === null ? null : (
          <p
            data-testid="assertion-db-meta"
            className="mt-1 flex flex-wrap gap-x-2 text-[11px] text-muted-foreground"
          >
            {dbMeta.rowCount === null ? null : (
              <span data-testid="assertion-db-rows">{`${dbMeta.rowCount} 行`}</span>
            )}
            {dbMeta.elapsedMs === null ? null : (
              <span data-testid="assertion-db-elapsed">{`${dbMeta.elapsedMs} ms`}</span>
            )}
            {dbMeta.truncated ? (
              <span
                data-testid="assertion-db-truncated"
                className="text-amber-700 dark:text-amber-300"
              >
                结果已截断
              </span>
            ) : null}
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
        {isDb && dbReason !== "" ? (
          <p
            data-testid="assertion-db-error"
            data-reason={rawReason}
            className="mt-1 text-[11px] text-red-700 dark:text-red-300"
          >
            {dbReason}
          </p>
        ) : !isDb && rawReason !== "" ? (
          <p className="mt-1 text-[11px] text-red-700 dark:text-red-300">{rawReason}</p>
        ) : null}
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
                kind={evidenceKindById.get(evidenceId) ?? ""}
                onOpen={onOpenEvidence}
              />
            ))}
          </div>
        )}
      </td>
    </tr>
  )
}

/**
 * Assertion rows of the expanded case run (spec §9.3, P7 §10.6). Each row shows
 * the assertion kind/target, expected vs actual, verdict and its evidence ids so
 * a failed assertion is always traceable to a concrete artifact. A `kind=db`
 * row additionally shows the recorded row count, elapsed time, truncation flag
 * and the readable db failure reason (read-only violation, connection, …).
 */
export function AssertionRows({ assertions, evidence, onOpenEvidence }: AssertionRowsProps) {
  const evidenceKindById = new Map(evidence.map((item) => [item.id, item.kind]))

  return (
    <>
      {assertions.map((assertion) => (
        <AssertionRow
          key={assertion.id}
          assertion={assertion}
          evidenceKindById={evidenceKindById}
          onOpenEvidence={onOpenEvidence}
        />
      ))}
    </>
  )
}

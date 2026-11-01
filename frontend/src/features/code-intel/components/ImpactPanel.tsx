import { Alert } from "@/components/ui/Alert"
import { Badge, type BadgeTone } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"

import type {
  CodeConfidence,
  CodeImpactCandidateView,
  CodeImpactSource,
  CodeImpactSubjectKind,
} from "../types"
import {
  CODE_CONFIDENCE_LABELS,
  CODE_IMPACT_SOURCE_LABELS,
  CODE_IMPACT_SUBJECT_KIND_LABELS,
  CODE_IMPACT_SUBJECT_KIND_ORDER,
} from "../types"

export type ImpactPanelProps = {
  candidates: CodeImpactCandidateView[]
  pendingIds: Record<string, boolean>
  focusedCandidateId?: string | null
  degraded: boolean
  onToggleSelected: (candidate: CodeImpactCandidateView, selected: boolean) => void
  onFocusCandidate?: (candidateId: string) => void
}

const CONFIDENCE_TONE: Record<CodeConfidence, BadgeTone> = {
  high: "success",
  medium: "info",
  low: "neutral",
}

const SOURCE_TONE: Record<CodeImpactSource, BadgeTone> = {
  direct: "info",
  contract: "warning",
  relation: "neutral",
  ai: "warning",
}

type ImpactGroup = {
  kind: CodeImpactSubjectKind
  candidates: CodeImpactCandidateView[]
}

function groupCandidates(candidates: CodeImpactCandidateView[]): ImpactGroup[] {
  return CODE_IMPACT_SUBJECT_KIND_ORDER.map((kind) => ({
    kind,
    candidates: candidates.filter((candidate) => candidate.subject_kind === kind),
  })).filter((group) => group.candidates.length > 0)
}

/** Right column: impact candidates grouped by subject with optimistic selection. */
export function ImpactPanel({
  candidates,
  pendingIds,
  focusedCandidateId = null,
  degraded,
  onToggleSelected,
  onFocusCandidate,
}: ImpactPanelProps) {
  const groups = groupCandidates(candidates)

  return (
    <section aria-label="影响候选" className="surface-card space-y-3 p-4">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 className="text-sm font-semibold text-foreground">影响候选</h2>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            勾选后的影响会写入分析结果，供后续用例设计引用。
          </p>
        </div>
        <Badge tone="neutral">{candidates.length} 条</Badge>
      </div>

      {degraded ? (
        <Alert tone="warning" title="AI 推理降级">
          仅展示确定性候选与未映射项，可稍后重新触发分析。
        </Alert>
      ) : null}

      {candidates.length === 0 ? (
        <EmptyState
          title="暂无影响候选"
          description="分析完成后显示受影响的需求、用例版本、页面与 API。"
        />
      ) : (
        <div className="space-y-4">
          {groups.map((group) => (
            <div key={group.kind}>
              <h3 className="text-xs font-semibold text-muted-foreground">
                {CODE_IMPACT_SUBJECT_KIND_LABELS[group.kind]}
                <span className="ml-1 font-normal">({group.candidates.length})</span>
              </h3>
              <ul className="mt-1 space-y-2">
                {group.candidates.map((candidate) => {
                  const label = CODE_IMPACT_SUBJECT_KIND_LABELS[candidate.subject_kind]
                  const checkboxId = `impact-candidate-${candidate.id}`
                  const pending = pendingIds[candidate.id] === true
                  const focused = candidate.id === focusedCandidateId
                  return (
                    <li
                      key={candidate.id}
                      aria-current={focused ? "true" : undefined}
                      className={
                        focused
                          ? "rounded-lg border border-primary/40 bg-primary/5 p-2"
                          : "rounded-lg border border-border p-2"
                      }
                    >
                      <div className="flex items-start gap-2">
                        <input
                          id={checkboxId}
                          type="checkbox"
                          checked={candidate.selected}
                          disabled={pending}
                          aria-label={`选择影响候选 ${label} ${candidate.subject_id}`}
                          onChange={(event) => onToggleSelected(candidate, event.target.checked)}
                          className="mt-0.5 size-4 rounded border-border text-primary focus-visible:ring-2 focus-visible:ring-ring"
                        />
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-center gap-1.5">
                            <label
                              htmlFor={checkboxId}
                              className="text-xs font-medium text-foreground"
                            >
                              {label} · #{candidate.subject_id}
                            </label>
                            <Badge tone={CONFIDENCE_TONE[candidate.confidence]}>
                              {CODE_CONFIDENCE_LABELS[candidate.confidence]}
                            </Badge>
                            <Badge tone={SOURCE_TONE[candidate.source]}>
                              {CODE_IMPACT_SOURCE_LABELS[candidate.source]}
                            </Badge>
                            {pending ? (
                              <span className="text-[11px] text-muted-foreground">保存中…</span>
                            ) : null}
                          </div>
                          <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
                            {candidate.reason}
                          </p>
                          <div className="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
                            {candidate.evidence !== undefined && candidate.evidence.length > 0 ? (
                              <span>证据 {candidate.evidence.length} 条</span>
                            ) : (
                              <span>无证据</span>
                            )}
                            {onFocusCandidate ? (
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-6 px-2"
                                aria-label={`定位影响候选 ${label} ${candidate.subject_id}`}
                                onClick={() => onFocusCandidate(candidate.id)}
                              >
                                定位
                              </Button>
                            ) : null}
                          </div>
                        </div>
                      </div>
                    </li>
                  )
                })}
              </ul>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

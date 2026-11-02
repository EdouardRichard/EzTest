"use client"

import { useState } from "react"

import { Badge } from "@/components/ui/Badge"
import { Button } from "@/components/ui/Button"
import { EmptyState } from "@/components/ui/EmptyState"
import { CASE_RUN_STATUS_LABEL, CASE_RUN_STATUS_TONE } from "@/features/execution/labels"
import type { CaseRun } from "@/features/execution/types"

import type { VisualAssertion, VisualDiff } from "../types"
import {
  formatRatio,
  VISUAL_CLASSIFICATION_LABEL,
  VISUAL_CLASSIFICATION_TONE,
  visualReasonLabel,
} from "../types"
import { BaselineApprovalDialog } from "./BaselineApprovalDialog"
import { VisualDiffViewer } from "./VisualDiffViewer"

export type VisualComparisonCardProps = {
  versionId: string
  caseRun: CaseRun
  assertions: VisualAssertion[]
  diffs: VisualDiff[]
  isAdmin: boolean
  onRetest: (caseRun: CaseRun) => void
  onChanged: () => void
}

function visualSummary(caseRun: CaseRun): string | null {
  const passed = caseRun.summary.visual_passed
  const failed = caseRun.summary.visual_failed
  const inconclusive = caseRun.summary.visual_inconclusive
  if (passed === undefined && failed === undefined && inconclusive === undefined) return null
  return `视觉 通过 ${passed ?? 0} · 失败 ${failed ?? 0} · 未定 ${inconclusive ?? 0}`
}

function uniqueActualEvidenceIds(diffs: VisualDiff[]): string[] {
  const seen = new Set<string>()
  const ids: string[] = []
  for (const diff of [...diffs].sort((left, right) => Number(right.id) - Number(left.id))) {
    if (seen.has(diff.actual_evidence_id)) continue
    seen.add(diff.actual_evidence_id)
    ids.push(diff.actual_evidence_id)
  }
  return ids
}

/**
 * One case run inside the visual sub-view: its visual assertion configs,
 * baseline state, four-state comparisons and the admin approve/retest entries.
 */
export function VisualComparisonCard({
  versionId,
  caseRun,
  assertions,
  diffs,
  isAdmin,
  onRetest,
  onChanged,
}: VisualComparisonCardProps) {
  const [viewDiff, setViewDiff] = useState<VisualDiff | null>(null)
  const [approvalAssertion, setApprovalAssertion] = useState<VisualAssertion | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const summary = visualSummary(caseRun)
  const hasRows = assertions.length > 0 || diffs.length > 0

  return (
    <article data-testid={`visual-case-run-${caseRun.id}`} className="surface-card space-y-3 p-4">
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="text-sm font-semibold text-foreground">{caseRun.title}</h3>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            用例运行 #{caseRun.id} · 用例版本 #{caseRun.case_version_id}
            {summary !== null ? ` · ${summary}` : ""}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Badge tone={CASE_RUN_STATUS_TONE[caseRun.status]}>
            {CASE_RUN_STATUS_LABEL[caseRun.status]}
          </Badge>
          <Button variant="secondary" size="sm" aria-label="复测" onClick={() => onRetest(caseRun)}>
            复测
          </Button>
        </div>
      </header>

      {notice !== null ? (
        <p role="status" className="text-[11px] text-success">
          {notice}
        </p>
      ) : null}

      {!hasRows ? <p className="text-[11px] text-muted-foreground">该用例没有视觉断言。</p> : null}

      {assertions.map((assertion) => {
        const assertionDiffs = diffs.filter((diff) => diff.visual_assertion_id === assertion.id)
        const evidenceIds = uniqueActualEvidenceIds(assertionDiffs)
        return (
          <section
            key={assertion.id}
            data-testid={`visual-assertion-${assertion.id}`}
            className="rounded-lg border border-border bg-muted/20 p-3"
          >
            <header className="flex flex-wrap items-center gap-1.5">
              <span className="text-xs font-semibold text-foreground">
                视觉断言 {assertion.step_seq}.{assertion.assertion_seq}
              </span>
              <Badge tone="neutral">
                {assertion.selector.length > 0 ? assertion.selector : "整页 viewport"}
              </Badge>
              <span className="text-[11px] text-muted-foreground">
                {assertion.viewport_width}×{assertion.viewport_height} @
                {assertion.device_scale_factor}x · 阈值 {formatRatio(assertion.threshold)} · 配置 r
                {assertion.config_revision}
              </span>
              {assertion.baseline !== null && assertion.baseline !== undefined ? (
                <Badge tone="success">基线已批准</Badge>
              ) : (
                <Badge tone="warning">缺失基线</Badge>
              )}
              {isAdmin && evidenceIds.length > 0 ? (
                <Button
                  variant="secondary"
                  size="sm"
                  className="ml-auto"
                  onClick={() => setApprovalAssertion(assertion)}
                >
                  批准基线
                </Button>
              ) : null}
            </header>

            {assertion.baseline === null || assertion.baseline === undefined ? (
              <p className="mt-1.5 text-[11px] text-warning">
                首次执行只抓取 actual 截图，结果为未定（缺失视觉基线）；请管理员从 actual
                证据批准基线后复测。
              </p>
            ) : null}

            {assertionDiffs.length === 0 ? (
              <p className="mt-1.5 text-[11px] text-muted-foreground">尚未执行视觉比对。</p>
            ) : (
              <ul className="mt-2 space-y-1.5">
                {assertionDiffs.map((diff) => (
                  <li
                    key={diff.id}
                    data-testid={`visual-diff-${diff.id}`}
                    className="flex flex-wrap items-center gap-1.5 rounded-md border border-border bg-card px-2.5 py-2 text-[11px]"
                  >
                    <Badge tone={VISUAL_CLASSIFICATION_TONE[diff.classification]}>
                      {VISUAL_CLASSIFICATION_LABEL[diff.classification]}
                    </Badge>
                    <span className="text-foreground">{visualReasonLabel(diff.reason)}</span>
                    <span className="inline-flex items-center gap-1 text-muted-foreground">
                      <span>差异比例</span>
                      <strong className="text-foreground">{formatRatio(diff.diff_ratio)}</strong>
                      <span>·</span>
                      <span>阈值</span>
                      <strong className="text-foreground">{formatRatio(diff.threshold)}</strong>
                    </span>
                    <span className="text-muted-foreground">
                      环境{diff.environment_match ? "一致" : "不一致"}
                    </span>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="ml-auto"
                      onClick={() => setViewDiff(diff)}
                    >
                      查看差异
                    </Button>
                  </li>
                ))}
              </ul>
            )}
          </section>
        )
      })}

      {assertions.length === 0 && diffs.length > 0 ? (
        <EmptyState
          title="缺少视觉断言配置"
          description="该用例运行有视觉比对记录，但当前用例版本没有 active 的视觉断言配置（可能已归档）。"
        />
      ) : null}

      <BaselineApprovalDialog
        versionId={versionId}
        assertion={approvalAssertion ?? assertions[0] ?? visualAssertionFallback(caseRun)}
        evidenceIds={
          approvalAssertion === null
            ? []
            : uniqueActualEvidenceIds(
                diffs.filter((diff) => diff.visual_assertion_id === approvalAssertion.id),
              )
        }
        open={approvalAssertion !== null}
        onClose={() => setApprovalAssertion(null)}
        onApproved={() => {
          setNotice("基线已批准，复测后生效")
          onChanged()
        }}
      />

      <VisualDiffViewer
        versionId={versionId}
        diff={viewDiff}
        open={viewDiff !== null}
        onClose={() => setViewDiff(null)}
      />
    </article>
  )
}

/** The approval dialog is closed when it has no real assertion; this stub only satisfies types. */
function visualAssertionFallback(caseRun: CaseRun): VisualAssertion {
  return {
    id: "0",
    version_id: "",
    case_version_id: caseRun.case_version_id,
    case_assertion_id: "",
    step_seq: 0,
    assertion_seq: 0,
    version_sut_id: "",
    selector: "",
    region: null,
    threshold: 0,
    viewport_width: 1,
    viewport_height: 1,
    device_scale_factor: 1,
    wait_policy: {},
    config_revision: 1,
    status: "active",
    baseline: null,
    created_at: "",
    updated_at: "",
  }
}

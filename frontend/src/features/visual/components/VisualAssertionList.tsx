"use client"

import { EmptyState } from "@/components/ui/EmptyState"
import type { CaseRun } from "@/features/execution/types"

import type { VisualAssertion, VisualDiff } from "../types"
import { VisualComparisonCard } from "./VisualComparisonCard"

export type VisualAssertionListProps = {
  versionId: string
  caseRuns: CaseRun[]
  assertions: VisualAssertion[]
  diffs: VisualDiff[]
  isAdmin: boolean
  onRetest: (caseRun: CaseRun) => void
  onChanged: () => void
}

/**
 * Groups the version's visual assertion configs and comparisons by wave case
 * run. Rows without any visual assertion/comparison stay out of the view so a
 * wave's ordinary cases are not duplicated from the waves sub-view.
 */
export function VisualAssertionList({
  versionId,
  caseRuns,
  assertions,
  diffs,
  isAdmin,
  onRetest,
  onChanged,
}: VisualAssertionListProps) {
  if (assertions.length === 0 && diffs.length === 0) {
    return (
      <EmptyState
        title="暂无视觉断言"
        description="确认带视觉断言的用例并执行后，这里会按波次展示 actual/diff 截图与四态结果；管理员可从 actual 证据批准基线。"
      />
    )
  }

  if (caseRuns.length === 0) {
    return (
      <EmptyState
        title="暂无执行波次"
        description="视觉断言已配置，但该版本还没有用例运行；用例确认并执行后会出现四态结果与 actual 截图。"
      />
    )
  }

  const rows = caseRuns
    .map((caseRun) => ({
      caseRun,
      assertions: assertions.filter(
        (assertion) => assertion.case_version_id === caseRun.case_version_id,
      ),
      diffs: diffs.filter((diff) => diff.case_run_id === caseRun.id),
    }))
    .filter((row) => row.assertions.length > 0 || row.diffs.length > 0)

  if (rows.length === 0) {
    return (
      <EmptyState
        title="该波次暂无视觉断言"
        description="当前波次的用例没有视觉断言配置或视觉比对记录。切到其他波次，或到用例设计详情配置视觉断言。"
      />
    )
  }

  return (
    <div data-testid="visual-assertion-list" className="space-y-3">
      {rows.map((row) => (
        <VisualComparisonCard
          key={row.caseRun.id}
          versionId={versionId}
          caseRun={row.caseRun}
          assertions={row.assertions}
          diffs={row.diffs}
          isAdmin={isAdmin}
          onRetest={onRetest}
          onChanged={onChanged}
        />
      ))}
    </div>
  )
}
